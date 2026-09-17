import { expect, test, type Locator } from '@playwright/test';
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

    test('keeps the running status, composer and stop action in a stable vertical flow', async ({ page }) => {
      await loginToScientificWorkbench(page);
      llmFixtures.push(await createControlledLlmFixture(gatewayBaseUrl));
      const workspace = await createScientificWorkspace(page, `running-composer-${viewport.name}`);
      const frameId = workspace.conversationId;
      const conversationResponse = await page.request.get(`/api/conversations/${frameId}`);
      expect(conversationResponse.ok()).toBe(true);
      const conversation = (await conversationResponse.json()) as Record<string, unknown>;
      const frameResponse = await page.request.get(`/api/frames/${frameId}`);
      expect(frameResponse.ok()).toBe(true);
      const frame = (await frameResponse.json()) as Record<string, unknown>;

      await page.route(new RegExp(`/api/conversations/${frameId}$`), async (route) => {
        await route.fulfill({
          contentType: 'application/json',
          body: JSON.stringify({
            ...conversation,
            runtime: {
              state: 'running',
              can_send_message: false,
              has_task: true,
              task_status: 'running',
              is_processing: true,
              pending_confirmations: 0,
              turn_id: frameId,
            },
          }),
        });
      });
      await page.route(new RegExp(`/api/frames/${frameId}$`), async (route) => {
        await route.fulfill({
          contentType: 'application/json',
          body: JSON.stringify({
            ...frame,
            status: 'processing',
            status_description: 'Running scientific workflow',
            error: null,
            output_data: {},
          }),
        });
      });

      try {
        await page.goto(`/?v=running-composer-e2e#/conversation/${frameId}`);
        const runningStatus = page.getByTestId('synon-biomed-runtime-status');
        const sendbox = page.locator('.sendbox-panel');
        const stopButton = page.getByTestId('synon-biomed-task-pause');
        const sendButton = page.getByTestId('sendbox-send-btn');
        const composerInput = page.locator('.sendbox-panel textarea');
        const attachmentButton = page.getByTestId('conversation-attach-folder-btn');
        const modelButton = page.locator('.sendbox-model-btn').first();
        const microphoneButton = page.getByRole('button', {
          name: /开始语音输入|Start voice input/,
        });

        await expect(runningStatus).toBeVisible();
        await expect(stopButton).toBeVisible();
        await expect(sendButton).toBeVisible();
        await expect(sendButton).toBeDisabled();
        await expect(page.getByRole('button', { name: '更多发送方式' })).toHaveCount(0);
        await expect(microphoneButton).toBeVisible();
        await expect(microphoneButton).toBeEnabled();
        await assertStackedWithoutOverlap(runningStatus, sendbox);
        await assertInsideViewport(runningStatus, viewport.width, viewport.height);
        await assertInsideViewport(sendbox, viewport.width, viewport.height);
        await assertInsideViewport(stopButton, viewport.width, viewport.height);
        await assertInsideViewport(microphoneButton, viewport.width, viewport.height);

        await composerInput.fill('Queue this after the running task');
        await expect(stopButton).toBeVisible();
        await expect(sendButton).toBeVisible();
        await expect(sendButton).toBeEnabled();
        await expect(page.getByRole('button', { name: '更多发送方式' })).toHaveCount(0);
        await composerInput.click();
        await composerInput.fill('');
        await expect(composerInput).toHaveValue('');
        await expect(stopButton).toBeVisible();
        await expect(sendButton).toBeVisible();
        await expect(sendButton).toBeDisabled();

        if (viewport.name === 'desktop') {
          await expect(attachmentButton).toBeVisible();
          await expect(modelButton).toBeVisible();
          await assertInsideViewport(attachmentButton, viewport.width, viewport.height);
          await assertInsideViewport(modelButton, viewport.width, viewport.height);
          const sessionOptionsButton = page.getByRole('button', { name: '会话选项' });
          await expect(sessionOptionsButton).toBeEnabled();
          await sessionOptionsButton.click();
          const menu = page.getByRole('menu', { name: '会话选项' });
          await expect(menu.getByRole('menuitemcheckbox', { name: '委派' })).toBeVisible();
          await expect(menu.getByRole('menuitemcheckbox', { name: '自动审阅' })).toBeVisible();
          await expect(menu.getByRole('menuitemcheckbox', { name: '记忆' })).toBeVisible();
          await expect(menu.getByRole('menuitem', { name: /专家/ })).toBeVisible();
          await expect(menu.getByRole('menuitem', { name: /计算/ })).toBeVisible();
        } else {
          await page.getByTestId('sendbox-mobile-plus-btn').click();
          await expect(page.getByTestId('mobile-action-sheet-session-options')).toBeVisible();
          await expect(page.getByTestId('mobile-action-sheet-model')).toBeVisible();
        }
      } finally {
        await removeScientificWorkspace(page, workspace);
      }
    });
  });
}

async function assertStackedWithoutOverlap(upper: Locator, lower: Locator) {
  await expect
    .poll(async () => {
      const upperBox = await upper.boundingBox();
      const lowerBox = await lower.boundingBox();
      return Boolean(upperBox && lowerBox && upperBox.y + upperBox.height <= lowerBox.y);
    })
    .toBe(true);
}

async function assertInsideViewport(locator: Locator, viewportWidth: number, viewportHeight: number) {
  await expect
    .poll(async () => {
      const box = await locator.boundingBox();
      return Boolean(
        box &&
        box.x >= -1 &&
        box.y >= -1 &&
        box.x + box.width <= viewportWidth + 1 &&
        box.y + box.height <= viewportHeight + 1
      );
    })
    .toBe(true);
}
