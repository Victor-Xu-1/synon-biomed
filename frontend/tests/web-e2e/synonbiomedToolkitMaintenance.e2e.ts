import { expect, test } from '@playwright/test';
import { loginToScientificWorkbench } from './synonBiomedScientificFixture';

test('keeps connector maintenance reachable from the merged scientific toolkit', async ({ page }, testInfo) => {
  await loginToScientificWorkbench(page);
  await page.goto('/#/settings/tools', { waitUntil: 'domcontentloaded' });

  const health = page.getByTestId('synon-biomed-mcp-directory-health');
  const refresh = page.getByTestId('synon-biomed-mcp-refresh');
  const reconcile = page.getByTestId('synon-biomed-mcp-reconcile');
  await expect(health).toBeVisible();
  await expect(refresh).toBeVisible();
  await expect(reconcile).toBeVisible();
  await expect(page.getByTestId('synon-biomed-mcp-search')).toHaveCount(0);

  const refreshed = page.waitForResponse(
    (response) => response.request().method() === 'GET' && response.url().includes('/api/mcp-servers/connectors')
  );
  await refresh.click();
  expect((await refreshed).status()).toBe(200);

  const reconciled = page.waitForResponse(
    (response) => response.request().method() === 'POST' && response.url().includes('/api/mcp-servers/reconcile')
  );
  await reconcile.click();
  expect((await reconciled).status()).toBe(200);
  await expect(health).toBeVisible();

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(refresh).toBeVisible();
  await expect(reconcile).toBeVisible();
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('toolkit-maintenance-narrow.png'), fullPage: true });
});
