import { expect, test } from '@playwright/test';
import {
  createScientificWorkspace,
  loginToScientificWorkbench,
  removeScientificWorkspace,
  uploadScientificArtifact,
  type ScientificWorkspaceFixture,
} from './synonBiomedScientificFixture';

test.describe('Synon Biomed isolated MCP App viewer', () => {
  test('opens, saves, and rehydrates a real Ketcher artifact in the isolated resource origin', async ({ page }) => {
    let workspace: ScientificWorkspaceFixture | null = null;
    try {
      await loginToScientificWorkbench(page);
      workspace = await createScientificWorkspace(page, 'mcp-app');
      const artifact = await uploadScientificArtifact(page, workspace, {
        filename: 'playwright-empty.ket',
        contentType: 'application/json',
        source: '{"root":{"nodes":[]}}',
      });

      await page.goto(`/#/artifacts/${encodeURIComponent(artifact.artifactId)}`, {
        waitUntil: 'domcontentloaded',
      });
      const viewer = page.getByTestId('mcp-app-viewer');
      await expect(viewer).toHaveAttribute('data-state', 'ready', { timeout: 30_000 });
      const save = viewer.getByRole('button', { name: /^(Save|保存)$/ });
      await expect(save).toBeEnabled();
      const iframe = viewer.locator('iframe');
      await expect(iframe).toHaveAttribute('sandbox', 'allow-scripts allow-same-origin');
      await expect(iframe).toHaveAttribute('src', /http:\/\/mcp-app\.localhost:\d+\/mcp-app-resource\?ticket=/);
      await expect(iframe).toHaveAttribute('allow', /camera 'none'/);
      const appRoot = page.frameLocator('[data-testid="mcp-app-viewer"] iframe').locator('#root');
      await expect(appRoot).toBeVisible({ timeout: 30_000 });
      await expect(page.frameLocator('[data-testid="mcp-app-viewer"] iframe').locator('body')).not.toContainText(
        'Ketcher widget error'
      );

      await save.click();
      await expect(viewer).toHaveAttribute('data-state', 'saved', { timeout: 30_000 });
      await expect(viewer.locator('.synon-mcp-app-viewer__saved')).toHaveText(/^(Saved|已保存)$/);
      const versionsResponse = await page.request.get(
        `/api/artifacts/${encodeURIComponent(artifact.artifactId)}/versions`
      );
      expect(versionsResponse.status()).toBe(200);
      const versions = (await versionsResponse.json()) as Array<{ version_id?: unknown }>;
      expect(versions).toHaveLength(2);
      expect(versions.some((version) => version.version_id === artifact.versionId)).toBe(true);
      expect(
        versions.some((version) => typeof version.version_id === 'string' && version.version_id !== artifact.versionId)
      ).toBe(true);

      await page.reload({ waitUntil: 'domcontentloaded' });
      await expect(page.getByTestId('mcp-app-viewer')).toHaveAttribute('data-state', 'ready', {
        timeout: 30_000,
      });
      await expect(page.frameLocator('[data-testid="mcp-app-viewer"] iframe').locator('#root')).toBeVisible();
    } finally {
      if (workspace) await removeScientificWorkspace(page, workspace);
    }
  });
});
