import assert from 'node:assert/strict';
import { mkdtemp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { createServer } from 'vite';
import { chromium, expect } from '@playwright/test';

const artifacts = await mkdtemp(join(tmpdir(), 'synon-context-usage-'));
const snapshot = {
  sessionId: 'browser-context-usage',
  requestId: 'browser-request',
  model: 'candidate-model',
  observedAt: '2026-09-24T00:00:00Z',
  state: 'complete',
  source: 'provider',
  usedTokens: 1200,
  limitTokens: 1_000_000,
  limitSource: 'runner_default',
  outputTokens: 150,
  hasMedia: false,
  inputEstimates: [
    { key: 'systemPrompt', tokens: 300 },
    { key: 'messages', tokens: 600 },
    { key: 'toolDefinitions', tokens: 200 },
  ],
};
let mode = 'unavailable';
let vite;
let browser;
try {
  vite = await createServer({
    configFile: resolve('vite.config.ts'),
    server: { host: '127.0.0.1', port: 0, hmr: false, fs: { allow: [process.cwd()] } },
    plugins: [
      {
        name: 'context-usage-browser-page',
        configureServer(server) {
          server.middlewares.use('/__context_usage_test__', async (_request, response, next) => {
            try {
              const html = await server.transformIndexHtml(
                '/__context_usage_test__',
                `<html lang="en-US"><head><title>Context usage</title></head><body><div id="root"></div><script type="module" src="/@fs/${resolve('tests/web-e2e/contextUsagePanel.fixture.tsx')}"></script></body></html>`
              );
              response.setHeader('content-type', 'text/html; charset=utf-8');
              response.end(html);
            } catch (error) {
              next(error);
            }
          });
        },
      },
    ],
  });
  await vite.listen();
  const address = vite.httpServer.address();
  assert.equal(typeof address, 'object');
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1000, height: 750 } });
  const errors = [];
  const apiPaths = [];
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('request', (request) => {
    if (request.url().includes('/api/')) apiPaths.push(new URL(request.url()).pathname);
  });
  await page.route('**/api/conversations/browser-context-usage/context-usage', async (route) => {
    if (mode === 'error') return route.fulfill({ status: 503, json: { message: 'unavailable' } });
    const record =
      mode === 'request'
        ? { ...snapshot, state: 'request', source: 'estimated', usedTokens: 1100, outputTokens: 0 }
        : mode === 'configured'
          ? { ...snapshot, limitSource: 'configured' }
          : snapshot;
    return route.fulfill({
      status: 200,
      json: mode === 'unavailable' ? { status: 'unavailable' } : { status: 'available', snapshot: record },
    });
  });
  const origin = `http://127.0.0.1:${address.port}`;
  const trigger = page.getByTestId('synon-biomed-context-usage-trigger');
  const panel = page.getByTestId('context-usage-panel');
  await page.goto(origin + '/__context_usage_test__');
  await trigger.focus();
  await page.keyboard.press('Enter');
  await expect(panel).toContainText('No saved context record yet');
  mode = 'provider';
  await panel.getByRole('button', { name: 'Retry' }).click();
  await expect(panel).toContainText('provider-reported usage');
  await expect(panel).toContainText('not a verified model limit');
  await expect(panel.getByTestId('context-usage-percent')).toHaveCount(0);
  await expect(panel).toContainText('does not show remaining context');
  await expect(panel).toContainText('Tool definitions (including MCP)');
  await page.screenshot({ path: join(artifacts, 'desktop.png'), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await panel.getByRole('button', { name: 'Close' }).click();
  await trigger.click();
  await expect(panel).toBeVisible();
  const bounds = await panel.boundingBox();
  assert.ok(bounds && bounds.x >= 0 && bounds.x + bounds.width <= 390, JSON.stringify(bounds));
  await page.screenshot({ path: join(artifacts, 'narrow.png'), fullPage: true });
  mode = 'configured';
  await page.reload();
  await trigger.click();
  await expect(panel.getByTestId('context-usage-percent')).toContainText('0.1%');
  mode = 'request';
  await page.reload();
  await trigger.click();
  await expect(panel).toContainText('response usage is not yet available');
  await expect(panel).not.toContainText('Latest response');
  mode = 'error';
  await page.reload();
  await trigger.click();
  await expect(panel).toContainText('Could not load context usage');
  await expect(panel.getByRole('button', { name: 'Retry' })).toBeVisible();
  assert.ok(apiPaths.length >= 4);
  assert.ok(apiPaths.every((path) => path === '/api/conversations/browser-context-usage/context-usage'));
  assert.deepEqual(errors, []);
  console.log(JSON.stringify({ result: 'passed', artifacts, requests: apiPaths.length }));
} finally {
  await browser?.close();
  await vite?.close();
}
