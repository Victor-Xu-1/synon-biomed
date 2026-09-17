import { expect, test } from '@playwright/test';
import { webPassword, webUsername } from './synonGoWebCredentials';

test('shows the complete visual theme choices', async ({ page }) => {
  await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
  await page.getByRole('combobox').selectOption('zh-CN');
  await page.getByRole('textbox', { name: '用户名' }).fill(webUsername);
  await page.getByRole('textbox', { name: '密码' }).fill(webPassword);
  await page.getByRole('button', { name: '登录' }).click();
  await expect(page).toHaveURL(/#\/guid/);

  await page.goto('/#/settings/general');
  const selector = page.getByTestId('theme-family-select');
  await expect(selector).toBeVisible();
  await expect(page.getByTestId('theme-mode-select')).toHaveCount(0);

  await selector.click();
  const options = page.locator('[role="option"]:visible');
  await expect(options).toHaveCount(4);
  await expect(options).toHaveText(['冷色系', '暖色系', '纯白色系', '夜间暗色系']);

  await page.getByRole('option', { name: '暖色系' }).click();
  await expect(selector).toContainText('暖色系');
});
