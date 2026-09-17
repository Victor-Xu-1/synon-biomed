import type { Locator, Page } from '@playwright/test';
import { expect, test } from './officialChromeTest';
import { webPassword, webUsername } from './synonGoWebCredentials';

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('operates real Synon Biomed MCP controls in the native settings page', async ({ page }) => {
      test.setTimeout(120_000);
      await login(page);
      await page.goto('/#/settings/tools');

      const settings = page.getByTestId('synon-biomed-mcp-settings');
      await expect(settings).toBeVisible();
      await expect(page.getByTestId('synon-biomed-mcp-directory-health')).toContainText('已连接 24/24', {
        timeout: 60_000,
      });
      await expect(page.getByTestId('synon-biomed-mcp-pubmed')).toContainText('PubMed');
      await expect(page.getByTestId('synon-biomed-mcp-enabled-pubmed')).toHaveAttribute('aria-checked', 'true');
      await assertInsideViewport(settings, viewport.width);

      await page.getByRole('tab', { name: '在线市场' }).click();
      const marketplace = page.getByTestId('synon-biomed-mcp-marketplace');
      await expect(marketplace).toBeVisible();
      const marketplaceGrid = page.getByTestId('synon-biomed-mcp-marketplace-grid');
      await expect(marketplaceGrid).toBeVisible({ timeout: 60_000 });
      await expect
        .poll(async () => marketplaceGrid.getByRole('listitem').count(), { timeout: 60_000 })
        .toBeGreaterThan(0);
      const gridMetrics = await marketplaceGrid.evaluate((element) => {
        const styles = window.getComputedStyle(element);
        const columns = styles.gridTemplateColumns.split(' ').filter(Boolean).length;
        const rect = element.getBoundingClientRect();
        return {
          columns,
          fitsViewport: rect.left >= -1 && rect.right <= window.innerWidth + 1,
        };
      });
      expect(gridMetrics.columns).toBe(viewport.name === 'narrow' ? 1 : 3);
      expect(gridMetrics.fitsViewport).toBe(true);
      await page.getByRole('tab', { name: '已安装' }).click();

      await page.getByTestId('synon-biomed-mcp-permissions-pubmed').click();
      await expect(page.getByTestId('synon-biomed-mcp-permission-search_articles-allow')).toBeVisible();
      const permissionModal = page.locator('.arco-modal').filter({ hasText: 'PubMed 工具权限' });
      await assertInsideViewport(permissionModal, viewport.width);
      await page.locator('.arco-modal-close-icon').click();
      await expect(page.getByTestId('synon-biomed-mcp-reconcile')).toBeEnabled();

      if (viewport.name === 'desktop') {
        try {
          const enabledSwitch = page.getByTestId('synon-biomed-mcp-enabled-pubmed');
          await enabledSwitch.click();
          await expect(enabledSwitch).toHaveAttribute('aria-checked', 'false');
          await enabledSwitch.click();
          await expect(enabledSwitch).toHaveAttribute('aria-checked', 'true');

          await page.getByTestId('synon-biomed-mcp-permissions-pubmed').click();
          const deny = page.getByTestId('synon-biomed-mcp-permission-search_articles-deny');
          const allow = page.getByTestId('synon-biomed-mcp-permission-search_articles-allow');
          await deny.click();
          await expect(deny).toHaveClass(/arco-btn-primary/);
          await allow.click();
          await expect(allow).toHaveClass(/arco-btn-primary/);
          await page.locator('.arco-modal-close-icon').click();

          await page.getByTestId('synon-biomed-mcp-reconcile').click();
          await expect(page.getByText('MCP 工具已修复并刷新。')).toBeVisible();
        } finally {
          await restorePubMed(page);
        }
      }
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

async function assertInsideViewport(locator: Locator, viewportWidth: number) {
  await expect
    .poll(async () => {
      const box = await locator.boundingBox();
      return box !== null && box.x >= -1 && box.x + box.width <= viewportWidth + 1;
    })
    .toBe(true);
}

async function restorePubMed(page: Page) {
  await page.evaluate(async () => {
    const enabledResponse = await fetch('/api/mcp-servers/connectors/bundled%3Apubmed/enabled', {
      method: 'PUT',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ enabled: true }),
    });
    if (!enabledResponse.ok) {
      throw new Error(`PubMed enable restore failed: ${enabledResponse.status}`);
    }

    const permissionResponse = await fetch('/api/mcp-servers/bundled%3Apubmed/tool-grants', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ toolName: 'search_articles', decision: 'allow' }),
    });
    if (!permissionResponse.ok) {
      throw new Error(`PubMed permission restore failed: ${permissionResponse.status}`);
    }
  });
}
