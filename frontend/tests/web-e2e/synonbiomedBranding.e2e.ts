import { expect, test, type Locator } from '@playwright/test';
import { webPassword, webUsername } from './synonGoWebCredentials';

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

const expectTransparentImage = async (image: Locator, requireVisible = true) => {
  if (requireVisible) await expect(image).toBeVisible();
  else await expect(image).toBeAttached();
  await expect.poll(() => image.evaluate((node: HTMLImageElement) => node.naturalWidth)).toBeGreaterThan(0);

  const alpha = await image.evaluate((node: HTMLImageElement) => {
    const canvas = document.createElement('canvas');
    canvas.width = node.naturalWidth;
    canvas.height = node.naturalHeight;
    const context = canvas.getContext('2d', { willReadFrequently: true });
    if (!context) throw new Error('2D canvas is unavailable');
    context.drawImage(node, 0, 0);
    return context.getImageData(0, 0, 1, 1).data[3];
  });

  expect(alpha).toBe(0);
};

const expectCircuitTreeImage = async (image: Locator) => {
  await expect(image).toBeVisible();
  await expect.poll(() => image.evaluate((node: HTMLImageElement) => node.naturalWidth)).toBeGreaterThan(0);

  const pixels = await image.evaluate((node: HTMLImageElement) => {
    const canvas = document.createElement('canvas');
    canvas.width = node.naturalWidth;
    canvas.height = node.naturalHeight;
    const context = canvas.getContext('2d', { willReadFrequently: true });
    if (!context) throw new Error('2D canvas is unavailable');
    context.drawImage(node, 0, 0);
    const corner = [...context.getImageData(0, 0, 1, 1).data];
    const center = [...context.getImageData(Math.floor(canvas.width / 2), Math.floor(canvas.height / 2), 1, 1).data];
    return { corner, center };
  });

  expect(pixels.corner[3]).toBe(0);
  expect(pixels.center[3]).toBeGreaterThan(180);
  expect(pixels.center[2]).toBeGreaterThan(pixels.center[0]);
};

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('shows the transparent Synon Biomed brand before and after authentication', async ({ page }) => {
      await page.goto('/#/login', { waitUntil: 'domcontentloaded' });

      await expect(page).toHaveTitle('Synon Biomed');
      await expectCircuitTreeImage(page.locator('.login-page__logo img'));
      await expect(page.getByRole('heading', { name: 'Synon Biomed', exact: true })).toBeVisible();
      await expect(page.locator('body')).not.toContainText(/SynonAI/i);

      await page.locator('input[name="username"]').fill(webUsername);
      await page.locator('input[name="password"]').fill(webPassword);
      await page.locator('button[type="submit"]').click();
      await expect(page).toHaveURL(/#\/guid/);

      await expect(page).toHaveTitle('Synon Biomed');
      await expectTransparentImage(page.getByTestId('synon-biomed-brand-lockup'), viewport.name === 'desktop');
      await expect(page.locator('body')).not.toContainText(/SynonAI/i);
    });
  });
}
