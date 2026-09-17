import { expect, test } from './officialChromeTest';
import { webPassword, webUsername } from './synonGoWebCredentials';

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('boots the login page without private settings failures and authenticates', async ({ page }) => {
      const settingsResponses: Array<{ method: string; status: number; scope: string | null }> = [];
      const initializationErrors: string[] = [];
      const websocketURLs: string[] = [];

      page.on('response', (response) => {
        const url = new URL(response.url());
        if (url.pathname !== '/api/settings/client') return;
        settingsResponses.push({
          method: response.request().method(),
          status: response.status(),
          scope: url.searchParams.get('scope'),
        });
      });
      page.on('console', (message) => {
        if (
          message.type() === 'error' &&
          /Failed to initialize config|Failed to initialize language|init theme failed/i.test(message.text())
        ) {
          initializationErrors.push(message.text());
        }
      });
      page.on('websocket', (websocket) => websocketURLs.push(websocket.url()));

      await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
      await expect(page.locator('input[name="username"]')).toBeVisible();
      await expect(page.locator('input[name="password"]')).toBeVisible();
      await expect
        .poll(() => settingsResponses.length, {
          message: 'the login bootstrap should read public UI preferences',
        })
        .toBeGreaterThan(0);
      await page.waitForTimeout(300);

      expect(settingsResponses.every((response) => response.method === 'GET')).toBe(true);
      expect(settingsResponses.every((response) => response.status === 200)).toBe(true);
      expect(settingsResponses.every((response) => response.scope === 'ui-preferences')).toBe(true);
      expect(initializationErrors).toEqual([]);

      const publicPreferences = await page.request.get('/api/settings/client?scope=ui-preferences');
      expect(publicPreferences.status()).toBe(200);
      const privateSettings = await page.request.get('/api/settings/client');
      expect(privateSettings.status()).toBe(401);

      await page.locator('input[name="username"]').fill(webUsername);
      await page.locator('input[name="password"]').fill(webPassword);
      await page.locator('button[type="submit"]').click();
      await expect(page).toHaveURL(/#\/guid/);

      await expect
        .poll(() => websocketURLs.some((url) => new URL(url).pathname === '/api/events/ws'), {
          message: 'the authenticated desktop should establish its runtime event websocket',
        })
        .toBe(true);

      const cookies = await page.context().cookies();
      expect(cookies.find((cookie) => cookie.name === 'synon_session')?.httpOnly).toBe(true);
      expect(cookies.find((cookie) => cookie.name === 'synon_csrf')?.value).toBeTruthy();

      const continueButton = page.getByRole('button', { name: /Continue|继续/ });
      await page.waitForTimeout(300);
      if (await continueButton.isVisible()) {
        await continueButton.click();
        await expect(page.getByTestId('onboarding-network')).toBeVisible();
      } else {
        await expect(page.getByRole('textbox', { name: /Send a message|发消息/ })).toBeVisible();
      }

      const logoutStatus = await page.evaluate(async () => {
        const response = await window.fetch('/api/auth/logout', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: '{}',
        });
        return response.status;
      });
      expect(logoutStatus).toBe(200);

      const currentUserStatus = await page.evaluate(async () => (await window.fetch('/api/auth/user')).status);
      expect(currentUserStatus).toBe(401);
      await page.goto('/#/login');
      await expect(page.locator('input[name="username"]')).toBeVisible();
    });
  });
}
