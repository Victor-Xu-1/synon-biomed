import { expect, test, type Locator, type Page } from '@playwright/test';
import { webPassword, webUsername } from './synonGoWebCredentials';

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

type RuntimeFixture = {
  conversationId: string;
  projectId: string;
};

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('exposes failed runtime recovery through the unified task center', async ({ page }) => {
      await login(page);
      await withRuntimeFixture(page, `${viewport.name}-failed`, async (frameId) => {
        const conversationResponse = await page.request.get(`/api/conversations/${frameId}`);
        expect(conversationResponse.ok()).toBe(true);
        const conversation = (await conversationResponse.json()) as Record<string, unknown>;
        const frameResponse = await page.request.get(`/api/frames/${frameId}`);
        expect(frameResponse.ok()).toBe(true);
        const frame = (await frameResponse.json()) as Record<string, unknown>;
        await page.route(new RegExp(`/api/frames/${frameId}$`), async (route) => {
          await route.fulfill({
            contentType: 'application/json',
            body: JSON.stringify({
              ...frame,
              status: 'failed',
              status_description: 'Checking network bridge availability',
              runtime_failure_kind: 'network_bridge_down',
              output_data: { error: 'network bridge process exited unexpectedly' },
            }),
          });
        });
        await page.route(new RegExp(`/api/conversations/${frameId}$`), async (route) => {
          await route.fulfill({
            contentType: 'application/json',
            body: JSON.stringify({
              ...conversation,
              runtime: {
                state: 'failed',
                can_send_message: true,
                has_task: true,
                task_status: 'failed',
                is_processing: false,
                pending_confirmations: 0,
                turn_id: frameId,
              },
            }),
          });
        });
        await page.goto(`/#/conversation/${frameId}`);

        const runtimeControls = page.getByTestId('synon-biomed-runtime-status');
        await expect(runtimeControls).toContainText('任务运行失败');
        await expect(page.getByTestId('synon-biomed-task-status-action')).toHaveAccessibleName('继续运行');
        await page.getByTestId('synon-biomed-task-details-trigger').click();
        const taskCenter = page.getByTestId('synon-biomed-task-status-panel');
        await expect(taskCenter).toBeVisible();
        await expect(taskCenter).toContainText('网络桥接服务不可用');
        await expect(taskCenter).not.toContainText('network bridge process exited unexpectedly');
        await expect(taskCenter.getByTestId('synon-biomed-task-refresh')).toBeVisible();
        await assertInsideViewport(runtimeControls, viewport.width);
        await assertInsideViewport(taskCenter, viewport.width);

        const streamingBatch = await page.evaluate(async (id) => {
          const response = await fetch(`/api/frames/${encodeURIComponent(id)}/streaming-batch`, {
            method: 'POST',
            headers: { 'content-type': 'application/json' },
            body: '{}',
          });
          return { status: response.status, body: await response.json() };
        }, frameId);
        expect(streamingBatch.status).toBe(200);
        expect(streamingBatch.body).toMatchObject({ root_frame_id: frameId, buffers: [] });
      });
    });

    test('matches the v1.1 command approval scope flow without submitting the command', async ({ page }) => {
      await login(page);
      await withRuntimeFixture(page, `${viewport.name}-approval`, async (frameId) => {
        const frameResponse = await page.request.get(`/api/frames/${frameId}`);
        expect(frameResponse.ok()).toBe(true);
        const frame = (await frameResponse.json()) as Record<string, unknown>;

        await page.route(new RegExp(`/api/frames/${frameId}$`), async (route) => {
          await route.fulfill({
            contentType: 'application/json',
            body: JSON.stringify({
              ...frame,
              status: 'processing',
              status_description: 'Checking API keys for NVIDIA NIMs',
              output_data: {
                pending_input_requests: [
                  {
                    requestId: 'approval-browser-fixture',
                    tool_id: 'toolu_approval_browser_fixture',
                    kind: 'local_exec',
                    tool: 'python',
                    code: 'print("NGC_API_KEY")',
                    environment: 'python',
                    mode: 'live',
                  },
                ],
              },
            }),
          });
        });

        await page.goto(`/#/conversation/${frameId}`);
        const approval = page.getByRole('region', { name: '等待操作授权' });
        await expect(approval).toBeVisible();
        await expect(approval).toContainText('运行 Python 代码？');
        await expect(approval).toContainText('print("NGC_API_KEY")');
        await expect(approval).not.toContainText('python conda env python');
        await exerciseApprovalScopeControls(page, approval);
        await assertInsideViewport(approval, viewport.width);
        await expect(page.getByTestId('synon-biomed-task-status-panel')).toHaveCount(0);
      });
    });
  });
}

async function login(page: Page) {
  await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
  await page.getByRole('combobox').selectOption('zh-CN');
  await page.getByRole('textbox', { name: '用户名' }).fill(webUsername);
  await page.getByRole('textbox', { name: '密码' }).fill(webPassword);
  await page.getByRole('button', { name: '登录' }).click();
  await expect(page).toHaveURL(/#\/guid/);
}

async function withRuntimeFixture(page: Page, label: string, run: (frameId: string) => Promise<void>) {
  const fixture = await createRuntimeFixture(page, label);
  try {
    await run(fixture.conversationId);
  } finally {
    await cleanupRuntimeFixture(page, fixture);
  }
}

async function createRuntimeFixture(page: Page, label: string): Promise<RuntimeFixture> {
  return page.evaluate(async (fixtureLabel) => {
    const suffix = `${fixtureLabel}-${crypto.randomUUID().slice(0, 8)}`;
    const projectResponse = await fetch('/api/projects', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ name: `P4 runtime ${suffix}`, description: 'Disposable P4 browser fixture' }),
    });
    if (projectResponse.status !== 201) throw new Error(`project create failed: ${projectResponse.status}`);
    const project = (await projectResponse.json()) as { project_id?: string };
    if (!project.project_id) throw new Error('project create response has no project_id');

    const conversationResponse = await fetch('/api/conversations', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        name: `P4 runtime ${suffix}`,
        assistant: { id: 'synonbiomed:OPERON', locale: 'zh-CN', conversation_overrides: {} },
        extra: { project_id: project.project_id, project_name: `P4 runtime ${suffix}` },
      }),
    });
    if (conversationResponse.status !== 201) {
      await fetch(`/api/projects/${encodeURIComponent(project.project_id)}`, { method: 'DELETE' });
      throw new Error(
        `conversation create failed: ${conversationResponse.status} ${await conversationResponse.text()}`
      );
    }
    const conversation = (await conversationResponse.json()) as { id?: string };
    if (!conversation.id) throw new Error('conversation create response has no id');
    return { conversationId: conversation.id, projectId: project.project_id };
  }, label);
}

async function cleanupRuntimeFixture(page: Page, fixture: RuntimeFixture) {
  const statuses = await page.evaluate(async ({ conversationId, projectId }) => {
    const conversation = await fetch(`/api/conversations/${encodeURIComponent(conversationId)}`, { method: 'DELETE' });
    const project = await fetch(`/api/projects/${encodeURIComponent(projectId)}`, { method: 'DELETE' });
    return [conversation.status, project.status];
  }, fixture);
  expect(statuses).toEqual([200, 200]);
}

async function exerciseApprovalScopeControls(page: Page, approval: Locator) {
  const scopeToggle = approval.getByRole('button', { name: '更改允许范围' });
  await expect(scopeToggle).toBeVisible();
  await scopeToggle.click();

  const projectScope = page.getByRole('menuitemradio', { name: '本项目 在本项目中记住' });
  await expect(page.getByRole('menuitemradio')).toHaveCount(4);
  await expect(page.getByRole('menuitemradio', { name: '本次 仅本次调用' })).toHaveAttribute('aria-checked', 'true');
  await projectScope.click();
  await expect(approval.getByRole('button', { name: '允许 本项目' })).toBeVisible();
  await expect(approval).toBeVisible();

  await scopeToggle.click();
  await page.getByRole('menuitemradio', { name: '本会话 直到本次会话结束' }).click();
  await expect(approval.getByRole('button', { name: '允许 本会话' })).toBeVisible();
}

async function assertInsideViewport(locator: Locator, viewportWidth: number) {
  await expect
    .poll(async () => {
      const box = await locator.boundingBox();
      return box !== null && box.x >= -1 && box.x + box.width <= viewportWidth + 1;
    })
    .toBe(true);
}
