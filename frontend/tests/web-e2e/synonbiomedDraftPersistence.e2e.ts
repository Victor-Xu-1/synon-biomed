import { expect, test, type Page } from '@playwright/test';
import {
  createScientificWorkspace,
  loginToScientificWorkbench,
  removeScientificWorkspace,
} from './synonBiomedScientificFixture';
const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('preserves and clears an unsent conversation draft across SPA navigation', async ({ page }) => {
      await loginToScientificWorkbench(page);
      const workspace = await createScientificWorkspace(page, `draft-${viewport.name}`);
      try {
        await page.goto(`/#/conversation/${workspace.conversationId}`);

        const input = page.getByTestId('sendbox-input');
        await expect(input).toBeVisible();
        await input.click();
        await input.fill(`未发送草稿-${viewport.name}`);

        await navigateHash(page, '#/settings/general');
        await navigateHash(page, `#/conversation/${workspace.conversationId}`);
        await expect(page.getByTestId('sendbox-input')).toHaveValue(`未发送草稿-${viewport.name}`);

        const restoredInput = page.getByTestId('sendbox-input');
        await restoredInput.click();
        await restoredInput.fill('');
        await expect(restoredInput).toHaveValue('');
        await navigateHash(page, '#/settings/general');
        await navigateHash(page, `#/conversation/${workspace.conversationId}`);
        await expect(page.getByTestId('sendbox-input')).toHaveValue('');
      } finally {
        await removeScientificWorkspace(page, workspace);
      }
    });
  });
}

async function navigateHash(page: Page, hash: string) {
  await page.evaluate((nextHash) => {
    window.location.hash = nextHash;
  }, hash);
  await expect(page).toHaveURL(new RegExp(hash.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
}
