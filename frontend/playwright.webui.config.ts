import { defineConfig } from '@playwright/test';

const requestedChannel = process.env.SYNON_GO_E2E_BROWSER_CHANNEL?.trim();
if (requestedChannel && requestedChannel !== 'chrome') {
  throw new Error('SYNON_GO_E2E_BROWSER_CHANNEL must be chrome when configured');
}

export default defineConfig({
  testDir: './tests/web-e2e',
  testMatch: '**/*.e2e.ts',
  globalSetup: './tests/web-e2e/globalSetup.ts',
  timeout: 60_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  retries: 0,
  workers: 1,
  reporter: [['list']],
  outputDir: 'test-results/web',
  use: {
    baseURL: process.env.SYNON_GO_WEB_URL ?? 'http://127.0.0.1:8080',
    headless: true,
    channel: requestedChannel === 'chrome' ? 'chrome' : undefined,
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
    launchOptions: {
      args: ['--enable-webgl', '--ignore-gpu-blocklist', '--use-angle=swiftshader'],
    },
  },
});
