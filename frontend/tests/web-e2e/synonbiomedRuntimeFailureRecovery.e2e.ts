import { expect, test, type Locator, type Page } from '@playwright/test';
import { createControlledLlmFixture, type ControlledLlmFixture } from '../integration/synonbiomedControlledLlmFixture';
import {
  createScientificWorkspace,
  loginToScientificWorkbench,
  removeScientificWorkspace,
} from './synonBiomedScientificFixture';
import { applySynonGoFrameFixture, synonGoFrameFixtureUnavailableReason } from './synonGoFrameFixture';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
const llmFixtures: ControlledLlmFixture[] = [];
const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

test.skip(Boolean(synonGoFrameFixtureUnavailableReason), synonGoFrameFixtureUnavailableReason);
test.afterEach(async () => {
  for (const fixture of llmFixtures.splice(0)) await fixture.dispose();
});

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('renders real model, safety, and overload recovery states without invoking an LLM', async ({ page }) => {
      await loginToScientificWorkbench(page);
      llmFixtures.push(await createControlledLlmFixture(gatewayBaseUrl));
      const workspace = await createScientificWorkspace(page, `runtime-failure-${viewport.name}`);
      const frameId = workspace.conversationId;
      try {
        seedRuntimeFailure(frameId, { error_kind: 'model_not_found' }, 'e2e-unavailable-model');
        await page.goto(`/#/conversation/${encodeURIComponent(frameId)}`);

        const modelStatus = page.getByTestId('synon-biomed-runtime-status');
        await expect(modelStatus).toBeVisible();
        await expect(modelStatus).toHaveAttribute('data-failure-kind', 'model_not_found');
        await expect(modelStatus).toContainText('模型不可用');
        await expect(modelStatus.getByRole('button', { name: /改用 .+ 继续/ })).toBeVisible();
        await assertInsideViewport(modelStatus, viewport.width);
        await assertNoHorizontalPageOverflow(page);
        await expect(modelStatus).toHaveScreenshot(`runtime-recovery-model-unavailable-${viewport.name}.png`, {
          animations: 'disabled',
          maxDiffPixelRatio: 0.02,
        });
        await modelStatus.getByTestId('synon-biomed-task-details-trigger').click();
        const modelPanel = page.getByTestId('synon-biomed-task-status-panel');
        await expect(modelPanel.getByTestId('synon-biomed-task-center-failure-detail')).toContainText(/原模型已不可用/);
        if (viewport.name === 'narrow') {
          await modelPanel.getByRole('button', { name: '选择其他模型' }).click();
          await expect(page.getByTestId('mobile-action-sheet-model')).toBeVisible();
        } else {
          await expect(page.getByTestId('acp-model-selector')).toBeVisible();
        }

        seedRuntimeFailure(frameId, { stop_reason: 'refusal' }, 'e2e-unavailable-model');
        await page.reload();
        const safetyStatus = page.getByTestId('synon-biomed-runtime-status');
        await expect(safetyStatus).toHaveAttribute('data-failure-kind', 'safety_refusal');
        await expect(safetyStatus).toContainText('会话已安全暂停');
        await expect(safetyStatus.getByRole('button', { name: /改用 .+ 继续/ })).toBeVisible();
        await assertInsideViewport(safetyStatus, viewport.width);
        await assertNoHorizontalPageOverflow(page);
        await expect(safetyStatus).toHaveScreenshot(`runtime-recovery-safety-refusal-${viewport.name}.png`, {
          animations: 'disabled',
          maxDiffPixelRatio: 0.02,
        });

        seedRuntimeFailure(frameId, { error_status: 529 }, 'e2e-unavailable-model');
        await page.reload();
        const overloadStatus = page.getByTestId('synon-biomed-runtime-status');
        await expect(overloadStatus).toHaveAttribute('data-failure-kind', 'model_overloaded');
        await expect(overloadStatus).toContainText('模型暂时繁忙');
        await expect(overloadStatus.getByRole('button', { name: '稍后重试' })).toBeVisible();
        await assertInsideViewport(overloadStatus, viewport.width);
        await assertNoHorizontalPageOverflow(page);
        await expect(overloadStatus).toHaveScreenshot(`runtime-recovery-model-overloaded-${viewport.name}.png`, {
          animations: 'disabled',
          maxDiffPixelRatio: 0.02,
        });
      } finally {
        await removeScientificWorkspace(page, workspace);
      }
    });
  });
}

function seedRuntimeFailure(frameId: string, outputData: Record<string, unknown>, model: string): void {
  applySynonGoFrameFixture({
    frameId,
    status: 'failed',
    outputData,
    model,
    statusDescription: 'Runtime recovery browser fixture',
  });
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
