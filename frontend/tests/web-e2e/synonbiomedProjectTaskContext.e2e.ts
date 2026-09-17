import { expect, test, type Page } from '@playwright/test';
import { webPassword, webUsername } from './synonGoWebCredentials';
import {
  createScientificWorkspace,
  removeScientificWorkspace,
  type ScientificWorkspaceFixture,
} from './synonBiomedScientificFixture';

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('uses the selected v1.1 project for task filtering and new-task context', async ({ page }) => {
      await login(page);
      let fixture: ScientificWorkspaceFixture | undefined;
      try {
        fixture = await createScientificWorkspace(page, `project-task-${viewport.name}`);
        const benchesResponse = await page.request.get(
          `/api/projects/${encodeURIComponent(fixture.projectId)}/benches`
        );
        if (!benchesResponse.ok()) {
          throw new Error(`Project benches failed: ${benchesResponse.status()} ${await benchesResponse.text()}`);
        }
        const benches = (await benchesResponse.json()) as Array<{ id?: string; root_frame_id?: string }>;
        expect(benches.some((bench) => (bench.root_frame_id || bench.id) === fixture!.conversationId)).toBe(true);
        const project = {
          projectId: fixture.projectId,
          name: fixture.projectName,
          benchCount: benches.length,
          latestConversationId: fixture.conversationId,
        };
        await page.reload({ waitUntil: 'domcontentloaded' });
        await expect(page).toHaveURL(/#\/guid/);

        if (viewport.name === 'narrow') {
          await page.getByTestId('sider-toggle').click();
        }

        await expect(page.getByTestId('sider-project-header')).toBeVisible();
        await expect(page.getByTestId('sider-new-chat')).toBeVisible();
        await page
          .getByTestId('sider-project-section')
          .getByRole('button', { name: project.name, exact: true })
          .click();
        await expect(page).toHaveURL(new RegExp(`#\\/conversation\\/${escapeRegExp(project.latestConversationId)}$`));

        if (viewport.name === 'desktop') {
          const workspaceToggle = page.getByTestId('workspace-toggle');
          await expect(workspaceToggle).toHaveCount(1);
          await expect(workspaceToggle).toBeVisible();
          await expect(workspaceToggle).toBeEnabled();

          const initialWorkspaceLabel = await workspaceToggle.getAttribute('aria-label');
          expect(['展开工作区', '收起工作区']).toContain(initialWorkspaceLabel);
          await workspaceToggle.click();
          await expect(workspaceToggle).toHaveAttribute(
            'aria-label',
            initialWorkspaceLabel === '收起工作区' ? '展开工作区' : '收起工作区'
          );
          await workspaceToggle.click();
          await expect(workspaceToggle).toHaveAttribute('aria-label', initialWorkspaceLabel!);
        }

        const taskSection = page.getByTestId('sider-task-section');
        await expect(taskSection).toHaveAttribute('data-project-id', project.projectId);
        await expect(
          page.getByTestId('sider-project-section').getByRole('button', { name: project.name, exact: true })
        ).toBeVisible();
        await expect(taskSection.locator('.chat-history__item')).toHaveCount(project.benchCount);

        if (viewport.name === 'narrow' && !(await taskSection.isVisible())) {
          await page.getByTestId('sider-toggle').click();
        }

        await page.getByTestId('sider-new-chat').click();
        await expect(page).toHaveURL(/#\/guid/);
        await expect(page.getByTestId('guid-page')).toHaveAttribute(
          'data-workspace',
          `synonbiomed://project/${encodeURIComponent(project.projectId)}`
        );
      } finally {
        if (fixture) await removeScientificWorkspace(page, fixture);
      }
    });
  });
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

async function login(page: Page) {
  await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
  await page.locator('input[name="username"]').fill(webUsername);
  await page.locator('input[name="password"]').fill(webPassword);
  await page.locator('button[type="submit"]').click();
  await expect(page).toHaveURL(/#\/guid/);
}
