import { expect, test, type Locator, type Page } from '@playwright/test';
import { webPassword, webUsername } from './synonGoWebCredentials';

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('shows the complete migrated third-party license inventory', async ({ page }) => {
      await login(page);
      await page.goto('/#/settings/general');

      const apiResponse = await page.request.get('/api/synonbiomed/licenses/third-party');
      expect(apiResponse.status()).toBe(200);
      const payload = (await apiResponse.json()) as { bytes: number; sha256: string; content: string };
      expect(payload.bytes).toBeGreaterThan(60_000);
      expect(payload.sha256).toMatch(/^[a-f0-9]{64}$/);
      expect(payload.content).toContain('### Ketcher');
      expect(payload.content).toContain('### GNU General Public License v3.0');

      await page.getByTestId('third-party-licenses-open').click();
      const modal = page.getByTestId('third-party-licenses-modal');
      await expect(modal).toBeVisible();
      await expect(modal).toContainText('11 个章节');
      await expect(modal).toContainText('runtime/assets/skills/THIRD_PARTY_LICENSES.md');
      await expect(page.getByTestId('third-party-license-content')).toContainText('### Ketcher');
      await assertInsideViewport(page.locator('.arco-modal').filter({ hasText: '第三方许可证' }), viewport.width);
      await assertNoHorizontalPageOverflow(page);

      await page.getByRole('textbox', { name: '搜索第三方许可证' }).fill('micromamba');
      await expect(page.getByTestId('third-party-license-content')).toContainText('BSD 3-Clause License');
      await assertInsideViewport(page.locator('.arco-modal').filter({ hasText: '第三方许可证' }), viewport.width);
      await assertNoHorizontalPageOverflow(page);

      await expect(page).toHaveScreenshot(`third-party-licenses-${viewport.name}.png`, {
        animations: 'disabled',
        maxDiffPixelRatio: 0.01,
      });
    });
  });
}

async function login(page: Page) {
  await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
  await page.getByRole('combobox').selectOption('zh-CN');
  await expect(page.locator('label[for="username"]')).toHaveText('用户名');
  await page.locator('#username').fill(webUsername);
  await page.locator('#password').fill(webPassword);
  await page.locator('button[type="submit"]').click();
  await expect(page).toHaveURL(/#\/guid/);
  await expect
    .poll(async () => {
      const response = await page.request.get('/api/settings/client?scope=ui-preferences');
      if (!response.ok()) return null;
      const payload = (await response.json()) as {
        data?: { language?: string };
        language?: string;
      };
      return payload.data?.language ?? payload.language ?? null;
    })
    .toBe('zh-CN');
  await page.reload({ waitUntil: 'domcontentloaded' });
  await expect(page).toHaveURL(/#\/guid/);
  const narrowSiderToggle = page.locator('button.app-titlebar__button');
  const shouldOpenNarrowSider = (page.viewportSize()?.width ?? 0) < 600;
  if (shouldOpenNarrowSider) {
    await expect(narrowSiderToggle).toHaveCount(1);
    await narrowSiderToggle.click();
  }
  await page.getByRole('button', { name: '打开账户与设置' }).click();
  const accountMenu = page.getByRole('menu', { name: '账户与设置' });
  await expect(accountMenu.getByRole('menuitem', { name: '设置' })).toBeVisible();
  await expect(accountMenu.getByRole('menuitem', { name: '套餐与用量' })).toBeVisible();
  await expect(accountMenu.getByRole('menuitem', { name: '退出登录' })).toBeVisible();
  await expect(accountMenu.getByRole('menuitem', { name: '检查更新' })).toBeVisible();
  await page.keyboard.press('Escape');
  if (shouldOpenNarrowSider) {
    const closeNarrowSider = page.getByTestId('mobile-sider-close');
    await expect(closeNarrowSider).toHaveCount(1);
    await closeNarrowSider.click();
  }
}

async function assertInsideViewport(locator: Locator, viewportWidth: number) {
  await expect
    .poll(async () => {
      const box = await locator.boundingBox();
      return box !== null && box.x >= -1 && box.x + box.width <= viewportWidth + 1;
    })
    .toBe(true);
}

async function assertNoHorizontalPageOverflow(page: Page) {
  const clientWidth = await page.evaluate(() => document.documentElement.clientWidth);
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth)).toBe(clientWidth);
}
