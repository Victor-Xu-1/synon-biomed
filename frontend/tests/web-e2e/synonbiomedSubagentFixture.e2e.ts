import { expect, test, type Locator, type Page } from '@playwright/test';
import { removeSynonGoDelegateFixture, seedSynonGoDelegateFixture } from './synonGoFrameFixture';
import { webPassword, webUsername } from './synonGoWebCredentials';

const PARENT_FRAME_ID = 'delegate-fixture-parent';
const CHILD_FRAME_ID = 'delegate-fixture-child';

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test.afterEach(() => {
      removeSynonGoDelegateFixture();
    });

    test('renders real child notifications and opens their child frame', async ({ page }) => {
      await login(page);
      const fixture = seedSynonGoDelegateFixture();
      expect(fixture.parentFrameId).toBe(PARENT_FRAME_ID);
      expect(fixture.childFrameId).toBe(CHILD_FRAME_ID);
      const fixtureResponse = await page.request.get(
        `/api/frames/${PARENT_FRAME_ID}/trace-shallow?include_messages=true`
      );
      expect(fixtureResponse.status()).toBe(200);
      await page.goto(`/#/conversation/${PARENT_FRAME_ID}`);

      const delegation = page.getByRole('button', { name: '打开子 Agent 1 literature-review' });
      await expect(delegation).toBeVisible();
      await expect(delegation).toContainText('子 Agent 1 · literature-review');
      await expect(delegation).toContainText('Delegate a focused literature review');
      await expect(delegation).toContainText('已完成 · 4 条消息');
      await assertInsideViewport(delegation, viewport.width);

      const group = page.getByRole('button', { name: '1 条消息 · 1 个子 Agent 完成' });
      const question = page.getByRole('button', { name: '打开 literature-review 的提问' });
      await expect(group).toBeVisible();
      await expect(group).toHaveAttribute('aria-expanded', 'false');
      await expect(question).toBeVisible();
      await expect(question).toContainText('Should the review include adjacent therapeutic targets?');
      await expect(page.getByText('Reviewed deterministic fixture evidence.')).toBeHidden();
      await assertInsideViewport(group, viewport.width);
      await assertInsideViewport(question, viewport.width);

      const summary = page.getByRole('button', { name: '2 个子任务，打开列表' });
      await expect(summary).toBeVisible();
      await expect(summary).toContainText('1 个等待操作');
      await assertInsideViewport(summary, viewport.width);
      await summary.click();
      const childList = page.getByRole('list', { name: '子任务' });
      const childRows = childList.getByRole('button');
      await expect(childRows).toHaveCount(2);
      await expect(childRows.nth(0)).toContainText('子 Agent 2 · evidence-check');
      await expect(childRows.nth(1)).toContainText('子 Agent 1 · literature-review');
      await expect(childRows.nth(1)).toContainText('1 个子任务');
      await summary.click();

      await group.click();
      await expect(group).toHaveAttribute('aria-expanded', 'true');
      await expect(page.getByText('Reviewed deterministic fixture evidence.')).toBeVisible();
      await expect(page.getByText('Returned two source-backed findings.')).toBeVisible();
      await expect(page.getByText('The deterministic evidence set contains two retained sources.')).toBeVisible();
      await expect(page.getByText('4秒')).toBeVisible();

      await question.click();
      await expect(page).toHaveURL(new RegExp(`#/conversation/${CHILD_FRAME_ID}$`));
      await expect(page.getByLabel('任务层级')).toContainText('Delegate parent fixture/literature-review');
      await expect(page.getByRole('button', { name: '返回父任务 Delegate parent fixture' })).toBeVisible();
      await expect(
        page.getByTestId('message-tool_call-left').getByRole('button', { name: '打开子 Agent 3 citation-audit' })
      ).toBeVisible();
      await expect(
        page.getByTestId('synonbiomed-delegate-dock').getByRole('button', { name: '打开子 Agent 3 citation-audit' })
      ).toBeVisible();
      await expect(page.getByText('Review deterministic fixture evidence.')).toBeVisible();
      await expect(page.getByText('Fixture review complete.')).toBeVisible();
    });
  });
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
