import { createControlledLlmFixture, type ControlledLlmFixture } from '../integration/synonbiomedControlledLlmFixture';
import {
  createScientificWorkspace,
  loginToScientificWorkbench,
  removeScientificWorkspace,
} from './synonBiomedScientificFixture';
import { expect, test } from './officialChromeTest';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
const llmFixtures: ControlledLlmFixture[] = [];
const CONTROLLED_PROGRESS_CHUNKS = Array.from(
  { length: 20 },
  (_, index) => `公开进度-${String(index + 1).padStart(2, '0')}：正在核对可复现证据。\n`
);

test.afterEach(async () => {
  for (const fixture of llmFixtures.splice(0)) await fixture.dispose();
});

test('streams controlled-provider public progress through the real runner before a required tool and final', async ({
  page,
}, testInfo) => {
  test.setTimeout(120_000);
  const markers = CONTROLLED_PROGRESS_CHUNKS.map((chunk) => chunk.trim());
  await loginToScientificWorkbench(page);
  const fixture = await createControlledLlmFixture(gatewayBaseUrl, {
    honorRequiredAskUser: true,
    publicProgressChunks: CONTROLLED_PROGRESS_CHUNKS,
    publicProgressChunkDelayMs: 60,
  });
  llmFixtures.push(fixture);
  const workspace = await createScientificWorkspace(page, 'initial-message-stream');
  const prompt = `Controlled stream gate ${workspace.conversationId}`;

  try {
    await page.goto(`/#/conversation/${encodeURIComponent(workspace.conversationId)}`, {
      waitUntil: 'domcontentloaded',
    });
    await expect(page.getByTestId('message-list-scroller')).toBeVisible();
    await page.evaluate((expectedMarkers) => {
      const arrivals: Record<string, number> = {};
      Object.defineProperty(window, '__synonControlledProgressArrivals', {
        value: arrivals,
        configurable: false,
        writable: false,
      });
      const collectVisibleText = (node: Node): string => {
        if (node.nodeType === Node.TEXT_NODE) return node.nodeValue ?? '';
        let text = '';
        if (node instanceof Element && node.shadowRoot) text += collectVisibleText(node.shadowRoot);
        for (const child of node.childNodes) text += collectVisibleText(child);
        return text;
      };
      const observe = () => {
        const visibleText = collectVisibleText(document);
        const observedAt = Date.now();
        for (const marker of expectedMarkers) {
          if (arrivals[marker] === undefined && visibleText.includes(marker)) arrivals[marker] = observedAt;
        }
      };
      const timer = window.setInterval(observe, 10);
      Object.defineProperty(window, '__synonControlledProgressTimer', {
        value: timer,
        configurable: false,
        writable: false,
      });
    }, markers);
    const composer = page.getByTestId('sendbox-input');
    await expect(composer).toBeEnabled();
    await composer.fill(prompt);
    const sendButton = page.getByTestId('sendbox-send-btn');
    await expect(sendButton).toBeEnabled();
    const accepted = page.waitForResponse(
      (response) =>
        response.request().method() === 'POST' &&
        response.url().includes(`/api/conversations/${workspace.conversationId}/messages`)
    );
    await sendButton.click();
    expect((await accepted).status()).toBe(202);

    await expect(page.getByText(prompt, { exact: false }).first()).toBeVisible({ timeout: 30_000 });
    await expect(page.locator('p').filter({ hasText: markers[0] }).first()).toBeVisible({ timeout: 30_000 });
    await expect(
      page
        .locator('p')
        .filter({ hasText: markers.at(-1)! })
        .first()
    ).toBeVisible({ timeout: 30_000 });
    const arrivals = await page.evaluate(() => {
      const instrumentedWindow = window as Window & {
        __synonControlledProgressArrivals?: Record<string, number>;
        __synonControlledProgressTimer?: number;
      };
      if (instrumentedWindow.__synonControlledProgressTimer !== undefined) {
        window.clearInterval(instrumentedWindow.__synonControlledProgressTimer);
      }
      return instrumentedWindow.__synonControlledProgressArrivals ?? {};
    });
    expect(fixture.publicProgressObservations).toHaveLength(markers.length);
    const latencies = fixture.publicProgressObservations.map((observation) => {
      const arrivedAt = arrivals[observation.text.trim()];
      if (typeof arrivedAt !== 'number') {
        throw new Error(`Missing DOM arrival for ${observation.text.trim()}`);
      }
      return arrivedAt - observation.sentAtUnixMs;
    });
    const ordered = latencies.toSorted((left, right) => left - right);
    const percentile = (fraction: number) => ordered[Math.max(0, Math.ceil(ordered.length * fraction) - 1)];
    const metrics = {
      samples: ordered.length,
      p50Ms: percentile(0.5),
      p95Ms: percentile(0.95),
      maxMs: ordered.at(-1)!,
    };
    testInfo.annotations.push({ type: 'public-progress-latency', description: JSON.stringify(metrics) });
    console.info(`[public-progress-latency] ${JSON.stringify(metrics)}`);
    expect(Math.min(...ordered)).toBeGreaterThanOrEqual(0);
    expect(metrics.p95Ms).toBeLessThanOrEqual(500);
    expect(metrics.maxMs).toBeLessThanOrEqual(1_000);

    const intake = page.getByTestId('synon-biomed-ask-user-card');
    await expect(intake).toBeVisible({ timeout: 30_000 });
    await expect(intake.getByRole('heading', { name: '这次任务应按哪个验收边界继续？' })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath('controlled-public-progress.png') });
    await intake.getByRole('radio', { name: /完整链路（推荐）/ }).click();

    const finalAnswer = page.getByText('Provider reachable.', { exact: true });
    await expect(finalAnswer).toHaveCount(1, { timeout: 30_000 });
    await expect(finalAnswer).toBeVisible();

    const response = await page.request.get(
      `/api/conversations/${encodeURIComponent(workspace.conversationId)}/messages?limit=50`
    );
    expect(response.status()).toBe(200);
    const persisted = await response.text();
    expect(persisted).toContain(prompt);
    expect(persisted.match(/Provider reachable\./g) ?? []).toHaveLength(1);
    for (const marker of markers) {
      expect(persisted.split(marker)).toHaveLength(2);
    }
  } finally {
    await removeScientificWorkspace(page, workspace);
  }
});

test('renders a routed initial message, public progress, required tool, and final without a refresh', async ({
  page,
}) => {
  test.setTimeout(90_000);
  await loginToScientificWorkbench(page);
  const initialProgress = '正在准备初始任务的可复现验收。';
  llmFixtures.push(
    await createControlledLlmFixture(gatewayBaseUrl, {
      honorRequiredAskUser: true,
      publicProgressChunks: [initialProgress],
    })
  );
  const workspace = await createScientificWorkspace(page, 'initial-message-route');
  const prompt = `Initial stream gate ${workspace.conversationId}`;

  try {
    await page.evaluate(
      ({ conversationId, initialMessage }) => {
        window.sessionStorage.setItem(`acp_initial_message_${conversationId}`, JSON.stringify(initialMessage));
      },
      {
        conversationId: workspace.conversationId,
        initialMessage: { input: prompt, files: [], session_options: {} },
      }
    );
    await page.goto(`/#/conversation/${encodeURIComponent(workspace.conversationId)}`, {
      waitUntil: 'domcontentloaded',
    });

    await expect(page.getByText(prompt, { exact: false }).first()).toBeVisible({ timeout: 30_000 });
    await expect(page.getByText(initialProgress, { exact: true })).toBeVisible({ timeout: 30_000 });
    const intake = page.getByTestId('synon-biomed-ask-user-card');
    await expect(intake).toBeVisible({ timeout: 30_000 });
    await intake.getByRole('radio', { name: /完整链路（推荐）/ }).click();
    await expect(page.getByText('Provider reachable.', { exact: true })).toHaveCount(1, { timeout: 30_000 });

    const response = await page.request.get(
      `/api/conversations/${encodeURIComponent(workspace.conversationId)}/messages?limit=50`
    );
    expect(response.status()).toBe(200);
    const persisted = await response.text();
    expect(persisted.split(initialProgress)).toHaveLength(2);
    expect(persisted.match(/Provider reachable\./g) ?? []).toHaveLength(1);
  } finally {
    await removeScientificWorkspace(page, workspace);
  }
});

test('suppresses exact public-progress replay while privately repairing an invalid tool call', async ({ page }) => {
  test.setTimeout(90_000);
  await loginToScientificWorkbench(page);
  const progress = ['正在核对第一项证据。', '正在核对第二项证据。', '正在准备可审计的下一步。'];
  const fixture = await createControlledLlmFixture(gatewayBaseUrl, {
    honorRequiredAskUser: true,
    publicProgressChunks: progress,
    publicProgressChunkDelayMs: 20,
    invalidAskUserAttempts: 1,
  });
  llmFixtures.push(fixture);
  const workspace = await createScientificWorkspace(page, 'private-tool-repair');
  const prompt = `Private repair replay gate ${workspace.conversationId}`;

  try {
    await page.goto(`/#/conversation/${encodeURIComponent(workspace.conversationId)}`, {
      waitUntil: 'domcontentloaded',
    });
    await expect(page.getByTestId('message-list-scroller')).toBeVisible();
    const composer = page.getByTestId('sendbox-input');
    await composer.fill(prompt);
    const accepted = page.waitForResponse(
      (response) =>
        response.request().method() === 'POST' &&
        response.url().includes(`/api/conversations/${workspace.conversationId}/messages`)
    );
    await page.getByTestId('sendbox-send-btn').click();
    expect((await accepted).status()).toBe(202);

    const intake = page.getByTestId('synon-biomed-ask-user-card');
    await expect(intake).toBeVisible({ timeout: 30_000 });
    expect(fixture.publicProgressObservations).toHaveLength(progress.length * 2);
    const paragraph = page.locator('p').filter({ hasText: progress[0] }).first();
    await expect(paragraph).toBeVisible();
    const visibleProgress = (await paragraph.textContent()) ?? '';
    for (const marker of progress) {
      expect(visibleProgress.split(marker)).toHaveLength(2);
    }

    await intake.getByRole('radio', { name: /完整链路（推荐）/ }).click();
    await expect(page.getByText('Provider reachable.', { exact: true })).toHaveCount(1, { timeout: 30_000 });
    const response = await page.request.get(
      `/api/conversations/${encodeURIComponent(workspace.conversationId)}/messages?limit=50`
    );
    expect(response.status()).toBe(200);
    const persisted = await response.text();
    for (const marker of progress) {
      expect(persisted.split(marker)).toHaveLength(2);
    }
  } finally {
    await removeScientificWorkspace(page, workspace);
  }
});
