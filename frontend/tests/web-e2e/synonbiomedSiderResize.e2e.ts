import { expect, test, type Locator, type Page } from '@playwright/test';
import { webPassword, webUsername } from './synonGoWebCredentials';

test.use({ viewport: { width: 1440, height: 900 } });

test('resizes the desktop sidebar within bounds, collapses at the threshold, and persists its width', async ({
  page,
}) => {
  await login(page);

  const sider = page.locator('.layout-sider');
  const handle = page.getByTestId('sider-resize-handle');
  await expect(handle).toBeVisible();
  await expectWidth(sider, 260);

  await dragBy(page, handle, 100);
  await expectWidth(sider, 360);

  await dragBy(page, handle, 200);
  await expectWidth(sider, 420);

  await dragBy(page, handle, -200);
  await expectWidth(sider, 220);

  await dragBy(page, handle, -60);
  await expect(sider).toHaveClass(/collapsed/);
  await expect(page.getByTestId('sider-toggle')).toHaveAttribute('aria-expanded', 'false');

  await page.getByTestId('sider-toggle').click();
  await expect(sider).not.toHaveClass(/collapsed/);
  await expectWidth(sider, 220);

  await dragBy(page, handle, 120);
  await expectWidth(sider, 340);
  await page.reload({ waitUntil: 'domcontentloaded' });
  await expectWidth(page.locator('.layout-sider'), 340);
});

async function dragBy(page: Page, handle: Locator, deltaX: number) {
  const box = await handle.boundingBox();
  expect(box, 'resize handle must have a layout box').not.toBeNull();
  const startX = box!.x + box!.width / 2;
  const startY = box!.y + Math.min(100, box!.height / 2);
  await page.mouse.move(startX, startY);
  await page.mouse.down();
  await page.mouse.move(startX + deltaX, startY, { steps: 8 });
  await page.mouse.up();
}

async function expectWidth(sider: Locator, expectedWidth: number) {
  await expect
    .poll(
      async () => Math.round(await sider.evaluate((element) => Number.parseFloat(getComputedStyle(element).width))),
      {
        message: `sidebar width should be ${expectedWidth}px`,
      }
    )
    .toBe(expectedWidth);
}

async function login(page: Page) {
  await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
  await page.locator('input[name="username"]').fill(webUsername);
  await page.locator('input[name="password"]').fill(webPassword);
  await page.locator('button[type="submit"]').click();
  await expect(page).toHaveURL(/#\/guid/);
}
