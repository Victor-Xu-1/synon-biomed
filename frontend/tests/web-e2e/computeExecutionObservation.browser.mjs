import assert from 'node:assert/strict';
import { mkdtemp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve, join } from 'node:path';
import { createServer } from 'vite';
import { chromium, expect } from '@playwright/test';

const target = new URL(process.env.SYNON_OBSERVATION_TEST_API ?? '');
assert.equal(target.hostname, '127.0.0.1');
const artifacts =
  process.env.SYNON_OBSERVATION_TEST_ARTIFACTS || (await mkdtemp(join(tmpdir(), 'synon-execution-observation-')));
let vite;
let browser;
const pageErrors = [];
try {
  vite = await createServer({
    configFile: resolve('vite.config.ts'),
    server: {
      host: '127.0.0.1',
      port: 0,
      strictPort: false,
      hmr: false,
      fs: { allow: [process.cwd()] },
      proxy: { '/api': { target: target.origin, headers: { 'X-Synon-User-Id': 'observation-owner' } } },
    },
    plugins: [
      {
        name: 'execution-observation-test-page',
        configureServer(server) {
          server.middlewares.use('/__observation_test__', async (_request, response, next) => {
            try {
              const html = await server.transformIndexHtml(
                '/__observation_test__',
                `<html lang="zh-CN"><head><title>Execution observation integration</title></head><body><div id="root" style="width:100%;min-height:600px"></div><script type="module" src="/@fs/${resolve('tests/web-e2e/computeExecutionObservation.fixture.tsx')}"></script></body></html>`
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
  const origin = `http://127.0.0.1:${address.port}`;
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 520, height: 850 } });
  page.on('pageerror', (error) => pageErrors.push(error.message));
  const post = async (path, data) => {
    const response = await page.request.post(origin + path, { data });
    assert.equal(response.status(), 200, await response.text());
    return response.json();
  };
  const inventory = async () => {
    const response = await page.request.get(origin + '/api/kernels');
    assert.equal(response.status(), 200);
    return (await response.json()).kernels;
  };
  const start = (command) =>
    post('/api/frames/observation-frame/kernel-exec', {
      language: 'python',
      environment: 'chem',
      code: `import subprocess\nsubprocess.run(${JSON.stringify(command)}, check=True)`,
    });
  const stop = async (execution) => {
    await post(`/api/frames/observation-frame/kernel-exec/${execution.exec_id}/interrupt`, {});
    await expect.poll(async () => (await inventory()).some((kernel) => kernel.busy), { timeout: 15_000 }).toBe(false);
  };
  const first = await start(['/bin/sleep', '60']);
  await expect
    .poll(
      async () =>
        (await inventory())
          .flatMap((kernel) => kernel.execution_observation?.processes ?? [])
          .map((process) => process.name),
      { timeout: 15_000 }
    )
    .toContain('sleep');
  await page.goto(origin + '/__observation_test__');
  const row = page.getByTestId('kernel-row');
  await expect(row.locator('[title="sleep"]')).toBeVisible({ timeout: 30_000 });
  await expect(row).toContainText('环境：chem');
  await row.getByRole('button').first().click();
  await expect(row).toContainText('PID');
  await expect(row.getByTestId('kernel-memory-pressure')).toContainText('所在资源组内存');
  await page.screenshot({ path: join(artifacts, 'running-sleep.png'), fullPage: true });
  await page.reload();
  await expect(row.locator('[title="sleep"]')).toBeVisible();
  await stop(first);
  await expect(row.locator('[title="sleep"]')).toHaveCount(0, { timeout: 10_000 });
  const second = await start(['/usr/bin/tail', '-f', '/dev/null']);
  await expect(row.locator('[title="tail"]')).toBeVisible({ timeout: 15_000 });
  await expect(row.locator('[title="sleep"]')).toHaveCount(0);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.screenshot({ path: join(artifacts, 'switched-tail.png'), fullPage: true });
  await stop(second);
  await expect(row.locator('[title="tail"]')).toHaveCount(0, { timeout: 10_000 });
  assert.deepEqual(pageErrors, []);
  console.log(
    JSON.stringify({
      result: 'passed',
      verified: [
        'real confined subprocess',
        'real resource API',
        'observed memory metrics',
        'renderer process name',
        'refresh',
        'execution switch',
        'interrupt and clear active label',
      ],
      artifacts,
    })
  );
} finally {
  await browser?.close();
  await vite?.close();
}
