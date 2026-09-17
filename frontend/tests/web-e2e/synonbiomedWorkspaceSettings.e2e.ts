import { expect, test, type Locator, type Page } from '@playwright/test';
import { webPassword, webUsername } from './synonGoWebCredentials';

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('covers the real workspace settings and account support flows', async ({ page }) => {
      await login(page);
      const dataDirectory = await loadStateDirectory(page);
      const stateDirectory = dataDirectory.current;

      await page.goto('/#/settings/credentials');
      const credentials = page.getByTestId('synon-credentials-settings');
      await expect(credentials).toBeVisible();
      await Promise.all(
        ['aws', 'github', 'gcp', 'literature', 'azure', 'modal', 'nvidia'].map((provider) =>
          expect(page.getByTestId(`credential-provider-${provider}`)).toBeVisible()
        )
      );
      await assertInsideViewport(credentials, viewport.width);
      await assertNoHorizontalPageOverflow(page);

      const aws = page.getByTestId('credential-provider-aws');
      await aws.getByRole('button', { name: '连接' }).click();
      const credentialEditor = page.getByTestId('credential-editor');
      await expect(credentialEditor).toBeVisible();
      await expect(page.getByRole('textbox', { name: '访问密钥 ID' })).toBeVisible();
      await expect(page.getByLabel('秘密访问密钥')).toBeVisible();
      await expect(page.getByRole('textbox', { name: '区域' })).toBeVisible();
      await expect(page.getByRole('textbox', { name: 'S3 存储桶' })).toBeVisible();
      await expect(page.getByRole('textbox', { name: '兼容 S3 的端点' })).toBeVisible();
      await assertInsideViewport(page.locator('.arco-modal').filter({ hasText: '连接 AWS' }), viewport.width);
      await page.locator('.arco-modal-close-icon').click();

      await page.getByRole('button', { name: '添加自定义凭证' }).click();
      await expect(page.getByRole('textbox', { name: '名称' })).toBeVisible();
      await expect(page.getByLabel('值')).toBeVisible();
      await assertInsideViewport(page.locator('.arco-modal').filter({ hasText: '连接 自定义' }), viewport.width);
      await page.locator('.arco-modal-close-icon').click();

      await page.goto('/#/settings/storage');
      const storage = page.getByTestId('synon-storage-settings');
      await expect(storage).toBeVisible();
      await expect(storage.getByText(stateDirectory, { exact: true })).toBeVisible();
      await expect(storage.getByRole('heading', { name: '磁盘用量' })).toBeVisible();
      await expect(storage.getByRole('heading', { name: '云存储' })).toBeVisible();
      await assertInsideViewport(storage, viewport.width);
      await assertNoHorizontalPageOverflow(page);

      const changeLocation = storage.getByRole('button', { name: '更改位置' });
      if (dataDirectory.activeFrames > 0) {
        await expect(changeLocation).toBeDisabled();
        await expect(changeLocation).toHaveAttribute('title', '停止所有运行任务后才能更改');
      } else if (dataDirectory.pendingMove) {
        await expect(changeLocation).toBeDisabled();
        await expect(changeLocation).toHaveAttribute('title', '已有迁移等待重启完成');
      } else {
        await changeLocation.click();
        const locationModal = page.locator('.arco-modal').filter({ hasText: '更改数据位置' });
        await expect(locationModal).toBeVisible();
        await expect(page.getByRole('textbox', { name: '新位置' })).toHaveValue(stateDirectory);
        await assertInsideViewport(locationModal, viewport.width);
        await page.locator('.arco-modal-close-icon').click();
      }

      await page.goto('/#/settings/general');
      const general = page.getByTestId('synon-general-settings');
      await expect(general).toBeVisible();
      await Promise.all(
        ['语言', '外观', '消息渠道', '联系邮箱', '关于'].map((section) =>
          expect(general.getByText(section, { exact: true }).first()).toBeVisible()
        )
      );
      await expect(general.getByRole('combobox', { name: '语言' })).toBeVisible();
      await expect(general.getByRole('combobox', { name: '视觉风格' })).toBeVisible();
      await expect(general.getByRole('heading', { name: '飞书' })).toBeVisible();
      await expect(general.getByRole('heading', { name: '微信' })).toBeVisible();
      await expect(general.getByText('仅配置飞书和微信。', { exact: false })).toBeVisible();
      await expect(general.getByRole('button', { name: '第三方许可证' })).toBeVisible();
      await assertInsideViewport(general, viewport.width);
      await assertNoHorizontalPageOverflow(page);

      if (viewport.name === 'narrow') {
        await page.getByTitle('账户与设置').click();
        await page.getByRole('complementary').getByRole('button', { name: '打开账户与设置' }).click();
      } else {
        await page.getByRole('button', { name: '打开账户与设置' }).click();
      }
      const accountMenu = page.getByRole('menu', { name: '账户与设置' });
      await expect(accountMenu.getByTestId('synon-account-profile')).toContainText('Victor');
      await expect(accountMenu.getByRole('menuitem', { name: '返回聊天' })).toBeVisible();
      await expect(accountMenu.getByRole('menuitem', { name: '套餐与用量' })).toBeVisible();
      await expect(accountMenu.getByRole('menuitem', { name: '检查更新' })).toBeVisible();
      await expect(accountMenu.getByRole('menuitem', { name: /深色|浅色/ })).toBeVisible();
      await expect(accountMenu.getByRole('menuitem', { name: '退出登录' })).toBeVisible();
      await assertInsideViewport(accountMenu, viewport.width);

      await accountMenu.getByTestId('synon-account-profile').click();
      const account = page.getByTestId('synon-account-settings');
      await expect(account).toBeVisible();
      await expect(account.getByRole('heading', { name: '个人账户' })).toBeVisible();
      await expect(account.getByRole('button', { name: '修改用户名' })).toBeVisible();
      await expect(account.getByRole('button', { name: '检查更新' })).toBeVisible();
      await assertInsideViewport(account, viewport.width);
      await assertNoHorizontalPageOverflow(page);
    });
  });
}

async function loadStateDirectory(
  page: Page
): Promise<{ current: string; source: string; activeFrames: number; pendingMove: boolean }> {
  const response = await page.request.get('/api/settings/data-dir');
  expect(response.ok(), `Data directory lookup failed: ${response.status()} ${await response.text()}`).toBe(true);
  const payload = (await response.json()) as {
    current?: unknown;
    source?: unknown;
    active_frames?: unknown;
    activeFrames?: unknown;
    pending_move?: unknown;
    pendingMove?: unknown;
    data?: {
      current?: unknown;
      source?: unknown;
      active_frames?: unknown;
      activeFrames?: unknown;
      pending_move?: unknown;
      pendingMove?: unknown;
    };
  };
  const current = payload.current ?? payload.data?.current;
  const source = payload.source ?? payload.data?.source;
  const activeFrames =
    payload.active_frames ?? payload.activeFrames ?? payload.data?.active_frames ?? payload.data?.activeFrames;
  const pendingMove =
    payload.pendingMove !== undefined
      ? payload.pendingMove
      : payload.pending_move !== undefined
        ? payload.pending_move
        : payload.data?.pendingMove !== undefined
          ? payload.data.pendingMove
          : (payload.data?.pending_move ?? null);
  expect(typeof current).toBe('string');
  expect((current as string).trim()).not.toBe('');
  expect(typeof source).toBe('string');
  expect(typeof activeFrames).toBe('number');
  expect(Number.isInteger(activeFrames) && (activeFrames as number) >= 0).toBe(true);
  expect(pendingMove === null || (typeof pendingMove === 'object' && !Array.isArray(pendingMove))).toBe(true);
  return {
    current: current as string,
    source: source as string,
    activeFrames: activeFrames as number,
    pendingMove: pendingMove !== null,
  };
}

async function login(page: Page) {
  await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
  await page.getByRole('combobox').selectOption('zh-CN');
  await page.getByRole('textbox', { name: '用户名' }).fill(webUsername);
  await page.getByRole('textbox', { name: '密码' }).fill(webPassword);
  await page.getByRole('button', { name: '登录' }).click();
  await expect(page).toHaveURL(/#\/guid/);
}

async function assertInsideViewport(locator: Locator, viewportWidth: number) {
  await expect
    .poll(async () => {
      const box = await locator.boundingBox();
      return box !== null && box.x >= -1 && box.x + box.width <= viewportWidth + 1;
    })
    .toBe(true);
}

async function assertNoHorizontalPageOverflow(page: Page) {
  await expect
    .poll(() =>
      page.evaluate(() => ({
        clientWidth: document.documentElement.clientWidth,
        scrollWidth: document.documentElement.scrollWidth,
      }))
    )
    .toEqual({
      clientWidth: await page.evaluate(() => document.documentElement.clientWidth),
      scrollWidth: await page.evaluate(() => document.documentElement.clientWidth),
    });
}
