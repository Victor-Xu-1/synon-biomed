import { createServer as createHTTPServer } from 'node:http';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { chromium, type Browser } from '@playwright/test';
import { createServer } from 'vite';
import { expect, it } from 'vitest';

it('isolates active charts and copied sources while preserving the controlled inspection bridge', async () => {
  const cacheDir = await mkdtemp(join(tmpdir(), 'synon-html-browser-'));
  let trackingRequests = 0;
  const vite = await createServer({
    configFile: false,
    root: resolve('.'),
    cacheDir,
    server: { middlewareMode: true },
    optimizeDeps: { noDiscovery: true, include: ['dompurify'] },
    appType: 'custom',
  });
  const server = createHTTPServer((request, response) => {
    if (request.url === '/') {
      response.setHeader('Content-Type', 'text/html');
      response.end(`<html><body><script type="module">
        import * as preview from '/packages/desktop/src/renderer/pages/conversation/Preview/components/renderers/isolatedHtmlDocument.ts';
        import {generateInspectScript} from '/packages/desktop/src/renderer/pages/conversation/Preview/components/renderers/htmlInspectScript.ts';
        window.preview = preview; window.generateInspectScript = generateInspectScript;
      </script></body></html>`);
    } else if (request.url === '/tracking') {
      trackingRequests++;
      response.end('');
    } else vite.middlewares(request, response);
  });
  await new Promise<void>((ready) => server.listen(0, '127.0.0.1', ready));
  const address = server.address();
  if (!address || typeof address === 'string') throw new Error('missing browser test listener');
  let browser: Browser | undefined;
  try {
    browser = await chromium.launch({ headless: true });
    const page = await browser.newPage();
    const browserErrors: string[] = [];
    page.setDefaultTimeout(5000);
    page.on('pageerror', (error) => browserErrors.push(error.message));
    await page.goto(`http://127.0.0.1:${address.port}`);
    await page.waitForFunction(() => Boolean((window as any).preview), undefined, { timeout: 5000 });
    const setup = async (passive: boolean) =>
      page.evaluate((passiveSource) => {
        const root = window as any;
        root.pwned = false;
        root.receipts = [];
        document.querySelectorAll('iframe').forEach((frame) => frame.remove());
        const instance = crypto.randomUUID();
        const frame = document.createElement('iframe');
        frame.id = 'preview';
        frame.setAttribute('sandbox', root.preview.HTML_PREVIEW_SANDBOX);
        frame.style.width = '600px';
        frame.style.height = '400px';
        frame.srcdoc = root.preview.isolatedHtmlDocument(
          `<html><body><h1 id="source">Source record</h1><button id="chart">Plot interaction</button><script>
        try { parent.pwned=true } catch (_) { document.body.dataset.originBlocked='yes' }
        document.getElementById('chart').onclick=()=>document.body.dataset.chart='interactive';
      </script><img src="${location.origin}/tracking" onerror="parent.pwned=true"></body></html>`,
          instance,
          passiveSource
        );
        root.currentInstance = instance;
        addEventListener('message', (event) => {
          const payload = root.preview.isolatedPreviewMessage(event, frame, instance);
          if (payload) root.receipts.push(payload);
        });
        frame.addEventListener('load', () =>
          root.preview.sendPreviewScripts(frame, instance, [root.generateInspectScript(false)])
        );
        document.body.append(frame);
      }, passive);

    await setup(true);
    let frame = page.frameLocator('#preview');
    await frame.locator('#source').waitFor();
    await expect.poll(() => frame.locator('body').evaluate(() => document.readyState)).toBe('complete');
    expect(await page.evaluate(() => (window as any).pwned)).toBe(false);
    expect(browserErrors.filter((message) => !message.includes('Blocked a frame with origin "null"'))).toEqual([]);
    expect(await frame.locator('body').getAttribute('data-origin-blocked')).toBeNull();
    expect(trackingRequests).toBe(0);
    await page.evaluate(() => {
      const root = window as any;
      root.preview.sendPreviewScripts(document.querySelector('iframe'), root.currentInstance, [
        root.generateInspectScript(true),
      ]);
    });
    await frame.locator('#inspect-mode-style').waitFor({ state: 'attached' });
    await frame.locator('#source').hover();
    await frame.locator('#source').click();
    await page.waitForFunction(
      () =>
        (window as any).receipts.some((value: any) =>
          value.__SYNON_AI_INSPECT_ELEMENT__?.text?.includes('Source record')
        ),
      undefined,
      { timeout: 5000 }
    );
    expect(await page.evaluate(() => document.querySelector('iframe')?.contentDocument === null)).toBe(true);
    await page.evaluate(() => {
      const root = window as any;
      root.preview.sendPreviewScripts(document.querySelector('iframe'), root.currentInstance, [
        root.generateInspectScript(false),
      ]);
      root.preview.sendPreviewScripts(document.querySelector('iframe'), 'stale-instance', [
        'document.body.dataset.stale="executed"',
      ]);
    });
    await frame.locator('#source').evaluate((element) => {
      const range = document.createRange();
      range.selectNodeContents(element);
      const selection = getSelection();
      selection?.removeAllRanges();
      selection?.addRange(range);
      element.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
    });
    await page.waitForFunction(
      () => (window as any).receipts.some((value: any) => value.selection?.text === 'Source record'),
      undefined,
      { timeout: 5000 }
    );
    expect(await frame.locator('body').getAttribute('data-stale')).toBeNull();

    // A copied/re-saved source may lose its passive metadata. Active HTML
    // still cannot acquire the application's origin; legitimate local chart
    // interaction remains available inside the isolated document.
    await setup(false);
    frame = page.frameLocator('#preview');
    await frame.locator('#chart').click();
    expect(await frame.locator('body').getAttribute('data-chart')).toBe('interactive');
    expect(await frame.locator('body').getAttribute('data-origin-blocked')).toBe('yes');
    expect(await page.evaluate(() => (window as any).pwned)).toBe(false);
  } finally {
    await browser?.close();
    await vite.close();
    await new Promise<void>((done, reject) => server.close((error) => (error ? reject(error) : done())));
    await rm(cacheDir, { recursive: true });
  }
}, 30000);
