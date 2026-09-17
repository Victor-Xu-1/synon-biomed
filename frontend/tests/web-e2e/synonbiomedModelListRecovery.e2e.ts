import { expect, test, type Locator, type Page } from '@playwright/test';
import { createControlledLlmFixture, type ControlledLlmFixture } from '../integration/synonbiomedControlledLlmFixture';
import {
  createScientificWorkspace,
  loginToScientificWorkbench,
  removeScientificWorkspace,
} from './synonBiomedScientificFixture';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
const llmFixtures: ControlledLlmFixture[] = [];

test.afterEach(async () => {
  for (const fixture of llmFixtures.splice(0)) await fixture.dispose();
});

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('shows a persistent model-list failure and recovers through the real runtime endpoint', async ({ page }) => {
      const pageErrors: string[] = [];
      await loginToScientificWorkbench(page);
      page.on('pageerror', (error) => pageErrors.push(error.message));
      llmFixtures.push(await createControlledLlmFixture(gatewayBaseUrl));
      const workspace = await createScientificWorkspace(page, `model-list-${viewport.name}`);
      const frameId = workspace.conversationId;
      const runtimeEnsurePath = `/api/conversations/${encodeURIComponent(frameId)}/runtime/ensure`;
      let failRuntimeEnsure = true;
      await page.route(new RegExp(`${runtimeEnsurePath}$`), async (route) => {
        if (failRuntimeEnsure) {
          await route.fulfill({
            status: 502,
            contentType: 'application/json',
            body: JSON.stringify({
              code: 'LLM_PROVIDER_UNAVAILABLE',
              error: 'provider failed with api_key=browser-secret-value',
            }),
          });
          return;
        }
        await route.continue();
      });

      try {
        const degradedResponse = page.waitForResponse(
          (response) => new URL(response.url()).pathname === runtimeEnsurePath && response.status() === 502
        );
        await page.goto(`/#/conversation/${encodeURIComponent(frameId)}`, { waitUntil: 'domcontentloaded' });
        await expect(page).toHaveURL(new RegExp(`#/conversation/${frameId}$`));
        await degradedResponse;
        if (viewport.name === 'narrow') {
          const mobileActions = page.getByTestId('sendbox-mobile-plus-btn');
          await expect(mobileActions).toBeVisible();
          await mobileActions.click();
        }
        const degraded =
          viewport.name === 'narrow'
            ? page.getByTestId('mobile-action-sheet-model')
            : page.getByTestId('model-picker-degraded-hint');
        await expect(degraded).toBeVisible();
        await expect(degraded).toContainText('模型列表不可用');
        await expect(page.getByText(/browser-secret-value/)).toHaveCount(0);
        await assertInsideViewport(degraded, viewport.width);
        await assertNoHorizontalPageOverflow(page);
        await expect(degraded).toHaveScreenshot(`model-list-unavailable-${viewport.name}.png`, {
          animations: 'disabled',
          maxDiffPixelRatio: 0.02,
        });

        failRuntimeEnsure = false;
        const recovered = page.waitForResponse(
          (response) => new URL(response.url()).pathname === runtimeEnsurePath && response.status() === 200
        );
        if (viewport.name === 'narrow') {
          await degraded.click();
          await recovered;
          await page.getByTestId('sendbox-mobile-plus-btn').click();
          const modelEntry = page.getByTestId('mobile-action-sheet-model');
          await expect(modelEntry).toBeVisible();
          await expect(modelEntry).toContainText('模型');
          await expect(modelEntry).not.toContainText('不可用');
        } else {
          await page.getByRole('button', { name: '重试加载模型列表' }).click();
          await recovered;
          await expect(page.getByTestId('model-picker-degraded-hint')).toHaveCount(0);
          await expect(page.getByTestId('acp-model-selector')).toBeVisible();
        }
        expect(pageErrors).toEqual([]);
      } finally {
        await removeScientificWorkspace(page, workspace);
      }
    });
  });
}

async function assertInsideViewport(locator: Locator, viewportWidth: number) {
  const box = await locator.boundingBox();
  expect(box).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(viewportWidth);
}

async function assertNoHorizontalPageOverflow(page: Page) {
  const dimensions = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
  expect(dimensions.scrollWidth).toBeLessThanOrEqual(dimensions.clientWidth);
}
