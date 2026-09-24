import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import { expect, test } from '@playwright/test';
import {
  createScientificWorkspace,
  csrfHeaders,
  loginToScientificWorkbench,
  removeScientificWorkspace,
} from './synonBiomedScientificFixture';

test('optimizes and reverts only the current conversation draft', async ({ page }) => {
  let calls = 0;
  let releaseSecond: (() => void) | null = null;
  const upstream = createServer(async (request, response) => {
    if (request.method !== 'POST' || request.url !== '/v1/chat/completions') {
      response.writeHead(404).end();
      return;
    }
    calls += 1;
    if (calls === 2) await new Promise<void>((resolve) => (releaseSecond = resolve));
    response.writeHead(200, { 'content-type': 'application/json' });
    response.end(
      JSON.stringify({
        choices: [
          {
            finish_reason: 'stop',
            message: { role: 'assistant', content: calls === 1 ? '清晰的科研任务指令' : '过期的优化结果' },
          },
        ],
        model: 'local-optimizer-test',
      })
    );
  });
  await new Promise<void>((resolve) => upstream.listen(0, '127.0.0.1', resolve));
  const address = upstream.address() as AddressInfo;

  await loginToScientificWorkbench(page);
  const workspace = await createScientificWorkspace(page, 'prompt-optimizer');
  let providerId = '';
  try {
    const headers = await csrfHeaders(page);
    const providerResponse = await page.request.post('/api/llm/providers', {
      headers,
      data: {
        name: 'Disposable local optimizer',
        provider: 'openai',
        baseUrl: `http://127.0.0.1:${address.port}/v1`,
        model: 'local-optimizer-test',
        apiKey: 'disposable-test-key',
      },
    });
    expect(providerResponse.status(), await providerResponse.text()).toBe(200);
    const created = (await providerResponse.json()) as { profile?: { id?: string } };
    providerId = created.profile?.id ?? '';
    expect(providerId).not.toBe('');

    await page.goto(`/#/conversation/${workspace.conversationId}`, { waitUntil: 'domcontentloaded' });
    const composer = page.getByTestId('sendbox-input');
    await expect(composer).toBeVisible();
    await composer.fill('帮我整理这个研究问题');
    const draftState = await page.evaluate(() => {
      for (let index = 0; index < localStorage.length; index += 1) {
        const key = localStorage.key(index);
        if (!key?.startsWith('synonbiomed.composer.draft.v1')) continue;
        const parsed = JSON.parse(localStorage.getItem(key) ?? '{}') as { draft?: { content?: string } };
        return { key, content: parsed.draft?.content };
      }
      return null;
    });
    expect(draftState?.content).toBe('帮我整理这个研究问题');
    const firstResponse = page.waitForResponse(
      (response) => response.request().method() === 'POST' && response.url().includes('/api/llm/optimize-prompt')
    );
    await page.getByTestId('synon-biomed-optimize-prompt-trigger').click();
    const optimized = await firstResponse;
    expect(optimized.status(), await optimized.text()).toBe(200);
    await expect(composer).toHaveValue('清晰的科研任务指令');
    expect(calls).toBe(1);

    await page.getByTestId('synon-biomed-optimize-prompt-revert').click();
    await expect(composer).toHaveValue('帮我整理这个研究问题');

    await page.getByTestId('synon-biomed-optimize-prompt-trigger').click();
    await expect.poll(() => calls).toBe(2);
    await composer.fill('我刚刚改过的草稿');
    releaseSecond?.();
    await expect(page.getByTestId('synon-biomed-optimize-prompt-spinner')).toHaveCount(0);
    await expect(composer).toHaveValue('我刚刚改过的草稿');
    await expect(page.getByTestId('synon-biomed-optimize-prompt-revert')).toHaveCount(0);
  } finally {
    releaseSecond?.();
    if (providerId) {
      await page.request.delete(`/api/llm/providers/${encodeURIComponent(providerId)}`, {
        headers: await csrfHeaders(page),
      });
    }
    await removeScientificWorkspace(page, workspace);
    await new Promise<void>((resolve, reject) => upstream.close((error) => (error ? reject(error) : resolve())));
  }
});
