import { expect, test, type Page } from '@playwright/test';
import { webPassword, webUsername } from './synonGoWebCredentials';

async function login(page: Page): Promise<void> {
  await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
  await page.locator('input[name="username"]').fill(webUsername);
  await page.locator('input[name="password"]').fill(webPassword);
  await page.locator('button[type="submit"]').click();
  await expect(page).toHaveURL(/#\/guid/);
}

test.describe('Synon Biomed account security', () => {
  test('shows OIDC capability and revokes durable browser sessions without losing the current session', async ({
    browser,
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 1440, height: 1100 });
    await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
    await expect(page.getByRole('button', { name: /Google/ })).toBeVisible();
    const loginCard = page.locator('.login-page__card');
    await expect
      .poll(() =>
        loginCard.evaluate((element) =>
          element.getAnimations().every((animation) => animation.playState === 'finished')
        )
      )
      .toBe(true);
    const loginScreenshot = testInfo.outputPath('google-oidc-login.png');
    await page.screenshot({ path: loginScreenshot, fullPage: true });
    await testInfo.attach('google-oidc-login', {
      path: loginScreenshot,
      contentType: 'image/png',
    });
    await login(page);

    const otherContext = await browser.newContext({ viewport: { width: 1440, height: 1100 } });
    const currentPage = await otherContext.newPage();
    try {
      await login(currentPage);
      const securityResponse = currentPage.waitForResponse(
        (response) => response.url().endsWith('/api/account/security') && response.request().method() === 'GET'
      );
      await currentPage.goto('/#/settings/account', { waitUntil: 'domcontentloaded' });
      expect((await securityResponse).status()).toBe(200);

      await expect(currentPage.getByRole('heading', { name: /账户安全|Account security/ })).toBeVisible();
      await expect(currentPage.locator('.account-security__method')).toContainText(/本地密码|Local password/);
      await expect(currentPage.locator('.account-security__current')).toHaveCount(1);
      await expect.poll(() => currentPage.locator('.account-security__session').count()).toBeGreaterThan(1);

      const desktopScreenshot = testInfo.outputPath('account-security-desktop.png');
      await currentPage.screenshot({ path: desktopScreenshot, fullPage: true });
      await testInfo.attach('account-security-desktop', {
        path: desktopScreenshot,
        contentType: 'image/png',
      });

      await currentPage.getByRole('button', { name: /撤销其他会话|Revoke other sessions/ }).click();
      await expect(currentPage.locator('.account-security__session')).toHaveCount(1);
      await expect.poll(() => page.evaluate(async () => (await window.fetch('/api/auth/user')).status)).toBe(401);
      await expect
        .poll(() => currentPage.evaluate(async () => (await window.fetch('/api/auth/user')).status))
        .toBe(200);

      await currentPage.setViewportSize({ width: 760, height: 1000 });
      expect(
        await currentPage.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)
      ).toBe(true);
      const compactScreenshot = testInfo.outputPath('account-security-compact.png');
      await currentPage.screenshot({ path: compactScreenshot, fullPage: true });
      await testInfo.attach('account-security-compact', {
        path: compactScreenshot,
        contentType: 'image/png',
      });
    } finally {
      await otherContext.close();
    }
  });
});
