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
  usedTokens: 348_200,
  limitTokens: 300_000,
  limitSource: 'runner_default',
  outputTokens: 0,
  hasMedia: false,
  inputEstimates: [
    { key: 'systemPrompt', tokens: 17_400 },
    { key: 'tools', tokens: 34_200 },
    { key: 'messages', tokens: 290_600 },
    { key: 'mcp', tokens: 300 },
    { key: 'skills', tokens: 5_700 },
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
                `<html lang="zh-CN"><head><title>Context usage</title></head><body><div id="root"></div><script type="module" src="/@fs/${resolve('tests/web-e2e/contextUsagePanel.fixture.tsx')}"></script></body></html>`
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
  const page = await browser.newPage({ viewport: { width: 594, height: 650 } });
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
        ? { ...snapshot, state: 'request', source: 'estimated' }
        : mode === 'configured'
          ? { ...snapshot, limitSource: 'configured' }
          : mode === 'zero'
            ? {
                ...snapshot,
                usedTokens: 0,
                source: 'estimated',
                inputEstimates: snapshot.inputEstimates.map((row) => ({ ...row, tokens: 0 })),
              }
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
  await expect(panel).toContainText('此会话尚无上下文记录');
  mode = 'provider';
  await panel.getByRole('button', { name: '重试' }).click();
  await expect(panel.getByTestId('context-usage-percent')).toContainText('116.1%');
  await expect(panel.getByTestId('context-usage-legend')).toContainText('工具及子智能体');
  await expect(panel.getByTestId('context-usage-legend')).toContainText('连接器及MCP');
  for (const percent of ['5.8%', '11.4%', '96.9%', '0.1%', '1.9%']) {
    await expect(panel.getByTestId('context-usage-legend')).toContainText(percent);
  }
  const visibleText = await panel.innerText();
  assert.ok(visibleText.includes('已使用 348.2K / 300.0K'));
  await expect(panel.locator('p')).toHaveCount(0);
  const summary = panel.getByRole('group');
  const descriptionId = await summary.getAttribute('aria-describedby');
  assert.ok(descriptionId);
  const desktopBounds = await panel.boundingBox();
  assert.ok(desktopBounds && Math.abs(desktopBounds.width - 560) <= 1, JSON.stringify(desktopBounds));
  assert.ok(Math.abs(desktopBounds.height - 486) <= 1, JSON.stringify(desktopBounds));
  const visualTokens = await panel.evaluate((root) => {
    const header = root.firstElementChild?.firstElementChild;
    const bigPercent = root.querySelector('[data-testid="context-usage-percent"]');
    const legend = root.querySelector('[data-testid="context-usage-legend"]');
    const firstRow = legend?.firstElementChild;
    if (!header || !bigPercent || !firstRow || !legend) throw new Error('Context usage visual elements missing');
    return {
      radius: getComputedStyle(root).borderRadius,
      headerFont: getComputedStyle(header).fontSize,
      headerLine: getComputedStyle(header).lineHeight,
      bigFont: getComputedStyle(bigPercent).fontSize,
      bigLine: getComputedStyle(bigPercent).lineHeight,
      legendFont: getComputedStyle(firstRow).fontSize,
      legendLine: getComputedStyle(firstRow).lineHeight,
      dots: [...legend.querySelectorAll('i')].map((dot) => getComputedStyle(dot).backgroundColor),
    };
  });
  assert.deepEqual(visualTokens, {
    radius: '30px',
    headerFont: '24px',
    headerLine: '32px',
    bigFont: '44px',
    bigLine: '52px',
    legendFont: '26px',
    legendLine: '36px',
    dots: ['rgb(99, 102, 241)', 'rgb(16, 185, 129)', 'rgb(245, 158, 11)', 'rgb(139, 92, 246)', 'rgb(236, 72, 153)'],
  });
  const closeStroke = async () =>
    panel.getByRole('button', { name: '关闭' }).evaluate((button) => {
      const path = button.querySelector('svg path');
      if (!path) throw new Error('Close icon path missing');
      return getComputedStyle(path).stroke;
    });
  assert.equal(await closeStroke(), 'rgb(76, 76, 76)');
  await page.screenshot({ path: join(artifacts, 'desktop.png'), fullPage: true });
  await panel.screenshot({ path: join(artifacts, 'card.png') });
  await summary.focus();
  await expect(page.locator(`#${descriptionId}`)).toContainText('默认上下文预算');
  await expect(page.locator('.arco-tooltip-content:visible')).toContainText('默认上下文预算');
  await page.setViewportSize({ width: 390, height: 844 });
  await panel.getByRole('button', { name: '关闭' }).click();
  await trigger.click();
  await expect(panel).toBeVisible();
  const bounds = await panel.boundingBox();
  assert.ok(bounds && bounds.x >= 0 && bounds.x + bounds.width <= 390, JSON.stringify(bounds));
  const narrowContrast = async () =>
    panel.evaluate((root) => {
      const used = root.querySelector('[role="group"] span:nth-child(2)');
      const legend = root.querySelector('[data-testid="context-usage-legend"] > div > span:last-child');
      if (!used || !legend) throw new Error('Context usage labels missing');
      const luminance = (color) => {
        const channels = color
          .match(/[\d.]+/g)
          ?.slice(0, 3)
          .map(Number);
        if (!channels || channels.length !== 3) throw new Error(`Invalid color: ${color}`);
        const linear = channels.map((value) => {
          const normalized = value / 255;
          return normalized <= 0.04045 ? normalized / 12.92 : ((normalized + 0.055) / 1.055) ** 2.4;
        });
        return linear[0] * 0.2126 + linear[1] * 0.7152 + linear[2] * 0.0722;
      };
      const background = luminance(getComputedStyle(root).backgroundColor);
      return [used, legend].map((element) => {
        const foreground = luminance(getComputedStyle(element).color);
        return (Math.max(foreground, background) + 0.05) / (Math.min(foreground, background) + 0.05);
      });
    });
  const narrowLightContrast = await narrowContrast();
  assert.ok(
    narrowLightContrast.every((ratio) => ratio >= 4.5),
    JSON.stringify(narrowLightContrast)
  );
  await page.screenshot({ path: join(artifacts, 'narrow.png'), fullPage: true });
  await page.evaluate(() => {
    document.documentElement.setAttribute('data-theme', 'dark');
    document.body.setAttribute('arco-theme', 'dark');
  });
  const narrowDarkContrast = await narrowContrast();
  assert.ok(
    narrowDarkContrast.every((ratio) => ratio >= 4.5),
    JSON.stringify(narrowDarkContrast)
  );
  assert.equal(await closeStroke(), 'rgb(197, 192, 184)');
  await page.screenshot({ path: join(artifacts, 'narrow-dark.png'), fullPage: true });
  mode = 'zero';
  await page.reload();
  await trigger.click();
  await expect(panel.getByTestId('context-usage-percent')).toContainText('0.0%');
  await expect(panel.getByTestId('context-usage-legend')).toContainText('0.0%');
  mode = 'configured';
  await page.reload();
  await trigger.click();
  await expect(panel.getByTestId('context-usage-percent')).toContainText('116.1%');
  mode = 'request';
  await page.reload();
  await trigger.click();
  await expect(panel.getByRole('group')).toBeVisible();
  const pendingDescriptionId = await panel.getByRole('group').getAttribute('aria-describedby');
  await expect(page.locator(`#${pendingDescriptionId}`)).toContainText('响应用量尚不可用');
  mode = 'error';
  await page.reload();
  await trigger.click();
  await expect(panel).toContainText('上下文用量加载失败');
  await expect(panel.getByRole('button', { name: '重试' })).toBeVisible();
  assert.ok(apiPaths.length >= 4);
  assert.ok(apiPaths.every((path) => path === '/api/conversations/browser-context-usage/context-usage'));
  assert.deepEqual(errors, []);
  console.log(JSON.stringify({ result: 'passed', artifacts, requests: apiPaths.length }));
} finally {
  await browser?.close();
  await vite?.close();
}
