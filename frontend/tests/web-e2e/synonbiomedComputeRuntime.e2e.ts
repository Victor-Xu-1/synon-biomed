import { expect, test } from '@playwright/test';
import { webPassword, webUsername } from './synonGoWebCredentials';

test('shows a compute resource and its current function call in Synon Biomed', async ({ page }) => {
  await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
  await page.getByRole('combobox').selectOption('zh-CN');
  await page.getByRole('textbox', { name: '用户名' }).fill(webUsername);
  await page.getByRole('textbox', { name: '密码' }).fill(webPassword);
  await page.getByRole('button', { name: '登录' }).click();
  await expect(page).toHaveURL(/#\/guid/);

  const csrfToken = (await page.context().cookies()).find((cookie) => cookie.name === 'synon_csrf')?.value;
  expect(csrfToken).toBeTruthy();
  const writeHeaders = { 'X-Synon-CSRF-Token': csrfToken! };
  const projectResponse = await page.request.post('/api/projects', {
    headers: writeHeaders,
    data: {
      name: `Compute runtime ${Date.now()}`,
      description: 'Disposable compute side-panel fixture',
      context: '',
    },
  });
  expect(projectResponse.status()).toBe(201);
  const project = (await projectResponse.json()) as { project_id?: string };
  expect(project.project_id).toBeTruthy();
  const conversationResponse = await page.request.post('/api/conversations', {
    headers: writeHeaders,
    data: {
      name: 'Compute runtime browser fixture',
      assistant: {
        id: 'synonbiomed:OPERON',
        locale: 'zh-CN',
        conversation_overrides: {},
      },
      extra: {
        project_id: project.project_id,
        project_name: 'Compute runtime browser fixture',
      },
    },
  });
  const conversation = (await conversationResponse.json()) as { id?: string; message?: string };
  expect(conversationResponse.status(), JSON.stringify(conversation)).toBe(201);
  expect(conversation.id).toBeTruthy();

  try {
    await page.route(/\/api\/kernels$/, async (route) => {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          kernels: [
            {
              frame_id: conversation.id,
              kernel_id: 'kernel-browser-fixture',
              language: 'python',
              environment: 'synon-biomed-python',
              agent_name: 'OPERON',
              busy: true,
              starting: false,
              execution_count: 3,
              cell_count: 2,
              current_cell: {
                source: 'print("compute resource call")',
                origin: 'agent',
                started_at: '2026-08-21T00:00:00Z',
              },
            },
          ],
          has_history: true,
        }),
      });
    });
    await page.route(
      new RegExp(`/api/conversations/${conversation.id}/workspace\\?path=project-files.*`),
      async (route) => {
        await route.fulfill({
          contentType: 'application/json',
          body: JSON.stringify({
            contract: 'synon.project-artifact-page.v1',
            items: [
              {
                name: 'fixture.csv',
                type: 'file',
                relative_path: 'project-files/fixture.csv',
                read_only: false,
                can_rename: true,
                can_delete: true,
                artifact_id: 'artifact-compute-fixture',
                version_id: 'version-compute-fixture',
                content_type: 'text/csv',
                size_bytes: 32,
                content_url: '/api/artifacts/artifact-compute-fixture/versions/version-compute-fixture',
              },
            ],
            next_cursor: null,
            has_more: false,
            total: 1,
          }),
        });
      }
    );
    await page.goto(`/#/conversation/${conversation.id}`);
    await page.evaluate(
      (conversationId) => localStorage.setItem(`workspace-preference-${conversationId}`, 'expanded'),
      conversation.id
    );
    await page.reload({ waitUntil: 'domcontentloaded' });
    const workspaceToggle = page.getByRole('button', { name: '展开工作区' });
    if (await workspaceToggle.isVisible()) {
      await workspaceToggle.click();
    }
    await expect(page.getByTestId('notebook-dock-badge')).toHaveCount(0);
    await expect(page.getByRole('tab', { name: '笔记本' })).toHaveCount(0);
    const computeTab = page.locator('.chat-workspace [role="tab"]').filter({ hasText: '\u8BA1\u7B97' });
    await expect(computeTab).toBeVisible();
    await computeTab.click();

    const panel = page.locator('.chat-layout-right-sider [data-testid="synon-biomed-compute-runtime-panel"]');
    await expect(panel).toBeVisible();
    await expect(
      page.locator('[data-testid="chat-preview-pane"] [data-testid="synon-biomed-compute-runtime-panel"]')
    ).toHaveCount(0);
    await expect(panel).toContainText('compute resource call');
    await expect(panel).toContainText('运行中');
    await expect(panel.getByRole('meter', { name: 'kernel 内存使用' })).toBeVisible();
    await expect(panel.getByRole('meter', { name: 'kernel CPU 使用' })).toBeVisible();
  } finally {
    await page.close();
    const cleanupResults = await Promise.allSettled([
      page.request.delete(`/api/conversations/${conversation.id}`, {
        headers: writeHeaders,
        timeout: 5_000,
      }),
      page.request.delete(`/api/projects/${project.project_id}`, {
        headers: writeHeaders,
        timeout: 5_000,
      }),
    ]);
    for (const result of cleanupResults) {
      if (result.status === 'rejected') {
        console.warn('[compute e2e] fixture cleanup did not complete before timeout');
      }
    }
  }
});
