import assert from 'node:assert/strict';
import { mkdtemp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { createServer } from 'vite';
import { chromium, expect } from '@playwright/test';

// Production component, isolated transport/LLM-free state transitions. This
// tests the presentation contract without touching a user's scientific run.
const artifacts = await mkdtemp(join(tmpdir(), 'synon-phase-lifecycle-'));
let server;
let browser;
try {
  server = await createServer({
    configFile: resolve('vite.config.ts'),
    server: { host: '127.0.0.1', port: 0, hmr: false, fs: { allow: [process.cwd()] } },
    plugins: [
      {
        name: 'phase-lifecycle-test-page',
        configureServer(vite) {
          vite.middlewares.use('/__phase_test__', async (_request, response, next) => {
            try {
              const html = await vite.transformIndexHtml(
                '/__phase_test__',
                `<html lang="zh-CN"><head><title>Phase lifecycle</title></head><body><div id="root"></div><script type="module" src="/@fs/${resolve('tests/web-e2e/toolPhaseLifecycle.fixture.tsx')}"></script></body></html>`
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
  await server.listen();
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1000, height: 800 } });
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.goto(`http://127.0.0.1:${server.httpServer.address().port}/__phase_test__`);
  const detail = page.getByTestId('tool-public-detail');
  const timeline = page.getByTestId('tool-chip');
  await expect(timeline).toContainText('1 / 8');
  await expect(timeline).toContainText('5:00');
  await expect(timeline).not.toContainText('13%');
  await timeline.click();
  await page.getByRole('button', { name: 'Fail operation', exact: true }).click();
  await expect(timeline).not.toContainText('当前阶段');
  const toggle = page.getByTestId('show-output-toggle');
  if ((await toggle.getAttribute('aria-expanded')) === 'false') await toggle.click();
  await expect(detail).toContainText('transfer failed: short body');
  await expect(detail).not.toContainText('当前阶段');
  await expect(detail).not.toContainText('/tmp/private');
  await expect(detail).not.toContainText('sk-fixture-secret123456');
  await toggle.click();
  await expect(toggle).toHaveAttribute('aria-expanded', 'false');
  await toggle.focus();
  await page.keyboard.press('Enter');
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  await page.screenshot({ path: join(artifacts, 'terminal-desktop.png'), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole('button', { name: 'English', exact: true }).click();
  await expect(detail).toContainText('transfer failed: short body');
  await expect(detail).not.toContainText('Current phase');
  await page.screenshot({ path: join(artifacts, 'terminal-narrow.png'), fullPage: true });
  await page.reload();
  await expect(timeline).toContainText('1 / 8');
  assert.deepEqual(errors, []);
  console.log(
    JSON.stringify({
      result: 'passed',
      artifacts,
      verified: [
        'production component',
        'running-to-terminal',
        'diagnostic redaction',
        'ordinal progress',
        'disclosure keyboard',
        'refresh',
        'two viewports',
      ],
    })
  );
} finally {
  await browser?.close();
  await server?.close();
}
