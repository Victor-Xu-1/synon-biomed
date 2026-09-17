import type { Locator, Page } from '@playwright/test';
import { expect, test } from './officialChromeTest';
import {
  createScientificWorkspace,
  loginToScientificWorkbench,
  removeScientificWorkspace,
} from './synonBiomedScientificFixture';
import {
  appendSynonGoTranscriptDeltas,
  beginSynonGoTranscriptStream,
  seedSynonGoScrollHistory,
  type TranscriptStreamRun,
} from './synonGoFrameFixture';

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('keeps one stable Claude-style scroll authority while messages stream', async ({ page }, testInfo) => {
      await loginToScientificWorkbench(page);
      const workspace = await createScientificWorkspace(page, `scroll-${viewport.name}`);

      try {
        const seededMessageIds = seedSynonGoScrollHistory(workspace.conversationId);
        const stream = beginSynonGoTranscriptStream(workspace.conversationId);
        await page.goto(`/?v=claude-scroll-e2e#/conversation/${workspace.conversationId}`);

        const scroller = page.getByTestId('message-list-scroller');
        await expect(scroller).toBeVisible();
        await expect.poll(() => getScrollMetrics(page)).toMatchObject({ scrollable: true });
        await expect.poll(async () => (await getScrollMetrics(page)).bottomGap).toBeLessThanOrEqual(20);

        const initialMessageIds = await page
          .locator('[data-source-message-id]')
          .evaluateAll((rows) =>
            rows
              .map((row) => row.getAttribute('data-source-message-id'))
              .filter((id): id is string => typeof id === 'string' && id.length > 0)
          );
        expect(initialMessageIds.length).toBeGreaterThan(2);
        expect(initialMessageIds).toContain(seededMessageIds.at(-1));
        await expect(page.getByTestId('jump-to-last-seen')).toHaveCount(0);

        emitAssistantDeltas(
          stream,
          Array.from({ length: 40 }, (_, index) => `Assistant streamed evidence line ${index + 1}.\n\n`)
        );
        await expect.poll(async () => (await getScrollMetrics(page)).bottomGap).toBeLessThanOrEqual(20);

        const lastPromptControl = page.getByTestId('jump-to-last-prompt');
        await expect(lastPromptControl).toHaveAttribute('aria-hidden', 'false');
        expect(await getControlStyle(lastPromptControl)).toMatchObject({
          height: 32,
          backgroundColor: 'rgb(255, 255, 255)',
          borderRadius: '9999px',
          fontSize: '14px',
          padding: '0px 12px 0px 10px',
          transitionDuration: '0.2s',
        });
        await lastPromptControl.click();
        await expect.poll(async () => (await getScrollMetrics(page)).bottomGap).toBeGreaterThan(100);
        await expect(page.locator('[data-last-user-msg]').last()).toBeVisible();

        await page.evaluate(() => {
          const element = document.querySelector<HTMLDivElement>('[data-testid="message-list-scroller"]');
          if (!element) throw new Error('message scroller unavailable');
          element.dispatchEvent(new WheelEvent('wheel', { bubbles: true, deltaY: -240 }));
          element.scrollTop = Math.max(0, element.scrollHeight - element.clientHeight - 180);
          element.dispatchEvent(new Event('scroll', { bubbles: true }));
        });

        const bottomControl = page.locator('button[aria-label="滚动到对话底部"]');
        await expect(bottomControl).toHaveAttribute('aria-hidden', 'false');
        await expect(bottomControl).toHaveClass(/conversation-scroll-control--visible/);
        const bottomControlBox = await bottomControl.boundingBox();
        expect(bottomControlBox).not.toBeNull();
        expect(bottomControlBox?.width).toBe(32);
        expect(bottomControlBox?.height).toBe(32);
        expect(await getControlStyle(bottomControl)).toMatchObject({
          height: 32,
          backgroundColor: 'rgb(255, 255, 255)',
          borderRadius: '9999px',
          fontSize: '14px',
          padding: '0px',
          transitionDuration: '0.2s',
        });

        const unpinnedAnchor = await getViewportAnchor(page);
        emitAssistantDeltas(stream, ['Unpinned streamed message.\n\n']);
        await page.waitForTimeout(100);
        await expect.poll(() => getViewportAnchor(page)).toEqual(unpinnedAnchor);

        await bottomControl.click();
        await expect.poll(async () => (await getScrollMetrics(page)).bottomGap).toBeLessThanOrEqual(20);
        await expect(bottomControl).toHaveAttribute('aria-hidden', 'true');

        emitAssistantDeltas(stream, ['Pinned streamed message.']);
        await expect.poll(async () => (await getScrollMetrics(page)).bottomGap).toBeLessThanOrEqual(20);
        await page.screenshot({
          path: testInfo.outputPath(`conversation-scroll-${viewport.name}.png`),
        });
      } finally {
        await removeScientificWorkspace(page, workspace);
      }
    });
  });
}

async function getScrollMetrics(page: Page) {
  return page.getByTestId('message-list-scroller').evaluate((element) => ({
    scrollable: element.scrollHeight > element.clientHeight + 100,
    scrollTop: element.scrollTop,
    bottomGap: Math.max(0, element.scrollHeight - element.clientHeight - element.scrollTop),
  }));
}

async function getViewportAnchor(page: Page) {
  return page.getByTestId('message-list-scroller').evaluate((element) => {
    const top = element.getBoundingClientRect().top;
    const row = Array.from(element.querySelectorAll<HTMLElement>('[data-source-message-id]')).find(
      (candidate) => candidate.getBoundingClientRect().bottom > top
    );
    if (!row) return null;
    return {
      id: row.dataset.sourceMessageId ?? null,
      offset: Math.round(row.getBoundingClientRect().top - top),
    };
  });
}

async function getControlStyle(control: Locator) {
  return control.evaluate((element) => {
    const style = getComputedStyle(element);
    return {
      height: Math.round(element.getBoundingClientRect().height),
      backgroundColor: style.backgroundColor,
      borderRadius: style.borderRadius,
      fontSize: style.fontSize,
      padding: style.padding,
      transitionDuration: style.transitionDuration,
    };
  });
}

function emitAssistantDeltas(stream: TranscriptStreamRun, chunks: string[]): void {
  appendSynonGoTranscriptDeltas(stream, chunks);
}
