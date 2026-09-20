import type { Page } from '@playwright/test';
import { expect, test } from './officialChromeTest';
import { webPassword, webUsername } from './synonGoWebCredentials';

test('aborts a pending usage request on client-side navigation', async ({ page }) => {
  await login(page);
  let release!: () => void;
  const pause = new Promise<void>((resolve) => {
    release = resolve;
  });
  const aborted: string[] = [];
  page.on('requestfailed', (request) => {
    aborted.push(request.url());
  });
  await page.route('**/api/preferences/disk-usage', async (route) => {
    const response = await route.fetch();
    await pause;
    if (!aborted.includes(route.request().url())) await route.fulfill({ response });
  });
  try {
    await page.goto('/#/settings/storage');
    await expect(page.getByText('正在扫描文件… 其他设置仍可使用。')).toBeVisible();
    await page.evaluate(() => {
      window.location.hash = '#/settings/credentials';
    });
    await expect(page.getByTestId('synon-credentials-settings')).toBeVisible();
    await expect.poll(() => aborted.some((url) => url.endsWith('/api/preferences/disk-usage'))).toBe(true);
  } finally {
    release();
    await page.unrouteAll({ behavior: 'ignoreErrors' });
  }
});

async function login(page: Page) {
  await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
  await page.getByRole('combobox').selectOption('zh-CN');
  await page.getByRole('textbox', { name: '用户名' }).fill(webUsername);
  await page.getByRole('textbox', { name: '密码' }).fill(webPassword);
  await page.getByRole('button', { name: '登录' }).click();
  await expect(page).toHaveURL(/#\/guid/);
}

for (const viewport of [
  { width: 1440, height: 1000 },
  { width: 1000, height: 800 },
  { width: 390, height: 844 },
]) {
  test.describe(`storage at ${viewport.width}px`, () => {
    test.use({ viewport });
    test('reads real storage, expands details and opens safe dialogs without modifying data', async ({
      page,
    }, info) => {
      await login(page);
      const metadataResponse = await page.request.get('/api/settings/data-dir?includeUsage=false');
      expect(metadataResponse.ok()).toBe(true);
      const metadata = await metadataResponse.json();
      expect(metadata.usageIncluded).toBe(false);
      expect(metadata.usageBytes).toBeNull();
      const before = await (await page.request.get('/api/settings/storage-rules')).json();
      await page.goto('/#/settings/storage');
      const storage = page.getByTestId('synon-storage-settings');
      await expect(storage.getByText(metadata.current, { exact: true })).toBeVisible();
      await expect(storage.getByText('已统计分类合计')).toBeVisible();
      await expect(storage.locator('.storage-cloud-empty')).toBeVisible();
      await expect(storage.locator('.storage-usage-summary time')).toHaveAttribute('datetime', /T/);
      const runtimePanel = storage.locator('.storage-runtime-panel');
      await expect(runtimePanel.getByRole('heading', { name: '科研软件' })).toBeVisible();
      await expect(runtimePanel.getByRole('button', { name: '选择软件' })).toBeEnabled();
      await runtimePanel.locator('.storage-runtime-disclosure summary').click();
      await expect(runtimePanel.locator('.storage-runtime-row')).toHaveCount(13);
      await runtimePanel.getByRole('button', { name: '选择软件' }).click();
      const softwareDialog = page.getByRole('dialog');
      await expect(softwareDialog.getByText('保存后会后台下载所选工具。', { exact: false })).toBeVisible();
      await expect(softwareDialog.getByRole('checkbox')).toHaveCount(13);
      await page.locator('.arco-modal-close-icon').click();
      await storage.getByRole('heading', { name: '存储', exact: true }).scrollIntoViewIfNeeded();
      await page.screenshot({ path: info.outputPath('storage-overview.png'), fullPage: true });
      const layout = await storage.evaluate((element) => {
        const box = element.getBoundingClientRect();
        return {
          width: box.width,
          right: box.right,
          left: box.left,
          transform: getComputedStyle(element).transform,
          sections: Array.from(element.querySelectorAll('.settings-section')).map((section) => ({
            client: section.clientHeight,
            scroll: section.scrollHeight,
          })),
        };
      });
      expect(layout.transform).toBe('none');
      expect(layout.left).toBeGreaterThanOrEqual(-1);
      expect(layout.right).toBeLessThanOrEqual(viewport.width + 1);
      for (const section of layout.sections) expect(section.scroll).toBeLessThanOrEqual(section.client + 1);
      await storage.getByRole('button', { name: '运行环境' }).click();
      await expect(storage.locator('#storage-environment-details')).toBeVisible();
      await expect(storage.getByText('软件包缓存', { exact: true })).toBeVisible();
      await expect(storage.getByText('.generations', { exact: true })).toHaveCount(0);
      await storage.getByText('查看完整路径', { exact: true }).first().click();
      await expect(storage.getByText(before.paths.taskArtifacts, { exact: true })).toBeVisible();
      await page.screenshot({ path: info.outputPath('storage-expanded.png'), fullPage: true });
      await storage.getByRole('button', { name: '编辑规则' }).click();
      await expect(page.getByRole('dialog')).toBeVisible();
      await expect(page.getByRole('textbox', { name: '任务生成文件' })).toBeVisible();
      await page.locator('.arco-modal-close-icon').click();
      await storage.getByRole('button', { name: '更改位置' }).click();
      const modal = page.getByRole('dialog');
      await expect(modal.getByRole('textbox', { name: '新位置' })).toHaveValue(metadata.current);
      await expect(modal.getByRole('button', { name: '更改位置' })).toBeDisabled();
      await page.screenshot({ path: info.outputPath('location-confirmation.png'), fullPage: true });
      await page.locator('.arco-modal-close-icon').click();
      await page.reload();
      await expect(storage.getByText(metadata.current, { exact: true })).toBeVisible();
      await expect(storage.getByText('已统计分类合计')).toBeVisible();
      expect(await (await page.request.get('/api/settings/storage-rules')).json()).toEqual(before);
      expect(
        (await (await page.request.get('/api/settings/data-dir?includeUsage=false')).json()).pendingMove
      ).toBeNull();
    });
  });
}

test('keeps metadata usable during a delayed real scan and retains values after refresh failure', async ({
  page,
}, info) => {
  await login(page);
  let release!: () => void;
  const pause = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route('**/api/preferences/disk-usage', async (route) => {
    const response = await route.fetch();
    await pause;
    await route.fulfill({ response });
  });
  await page.goto('/#/settings/storage');
  const storage = page.getByTestId('synon-storage-settings');
  try {
    await expect(storage.getByRole('button', { name: '更改位置' })).toBeEnabled();
    await expect(storage.getByText('正在扫描文件… 其他设置仍可使用。')).toBeVisible();
    await expect(storage.getByRole('button', { name: '编辑规则' })).toBeEnabled();
    await page.screenshot({ path: info.outputPath('independent-loading.png'), fullPage: true });
  } finally {
    release();
  }
  await expect(storage.getByText('已统计分类合计')).toBeVisible();
  const total = await storage.locator('.storage-usage-summary strong').textContent();
  await page.route('**/api/preferences/disk-usage?refresh=true', (route) =>
    route.fulfill({ status: 503, json: { error: 'controlled scan failure' } })
  );
  await storage.locator('.storage-usage-panel').getByRole('button', { name: '刷新', exact: true }).click();
  await expect(storage.getByText('刷新失败，暂保留上次结果。')).toBeVisible();
  await expect(storage.locator('.storage-usage-summary strong')).toHaveText(total!);
  await page.unroute('**/api/preferences/disk-usage?refresh=true');
  await storage.getByRole('button', { name: '重试' }).click();
  await expect(storage.getByText('刷新失败，暂保留上次结果。')).toHaveCount(0);
  await expect(
    storage.locator('.storage-usage-panel').getByRole('button', { name: '刷新', exact: true })
  ).toBeEnabled();
});
