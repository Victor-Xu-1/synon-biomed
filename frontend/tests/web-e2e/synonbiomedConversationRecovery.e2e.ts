import { expect, test, type Page } from '@playwright/test';
import { webPassword, webUsername } from './synonGoWebCredentials';

const missingConversationId = 'e2e-conversation-that-does-not-exist';
const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('replaces a real missing conversation with the new-task screen', async ({ page }) => {
      await login(page);

      const apiStatus = await page.evaluate(async (conversationId) => {
        const response = await fetch(`/api/conversations/${encodeURIComponent(conversationId)}`, {
          headers: { accept: 'application/json' },
        });
        return response.status;
      }, missingConversationId);
      expect(apiStatus).toBe(404);

      await page.goto(`/#/conversation/${missingConversationId}`);
      await expect(page).toHaveURL(/#\/guid$/);
      await expect(page.getByTestId('conversation-deleted-tombstone')).toHaveCount(0);
      await expect(page.getByTestId('conversation-load-error')).toHaveCount(0);
      await assertNoHorizontalPageOverflow(page);
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

async function assertNoHorizontalPageOverflow(page: Page) {
  await expect
    .poll(() =>
      page.evaluate(() => ({
        clientWidth: document.documentElement.clientWidth,
        scrollWidth: document.documentElement.scrollWidth,
      }))
    )
    .toEqual({
      clientWidth: await page.evaluate(() => document.documentElement.clientWidth),
      scrollWidth: await page.evaluate(() => document.documentElement.clientWidth),
    });
}
