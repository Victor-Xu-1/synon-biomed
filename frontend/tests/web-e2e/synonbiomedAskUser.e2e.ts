import type { Locator, Page } from '@playwright/test';
import { expect, test } from './officialChromeTest';
import {
  createScientificWorkspace,
  loginToScientificWorkbench,
  removeScientificWorkspace,
} from './synonBiomedScientificFixture';
import {
  applySynonGoFrameFixture,
  seedSynonGoAskUserFixture,
  synonGoFrameFixtureUnavailableReason,
} from './synonGoFrameFixture';

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

test.skip(Boolean(synonGoFrameFixtureUnavailableReason), synonGoFrameFixtureUnavailableReason);

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('renders a quiet single-surface pending ask_user card', async ({ page }) => {
      await loginToScientificWorkbench(page);
      const workspace = await createScientificWorkspace(page, `ask-user-visual-${viewport.name}`);
      const frameId = workspace.conversationId;
      try {
        seedPendingQuestion(frameId);
        await page.goto(`/#/conversation/${encodeURIComponent(frameId)}`);

        const card = page.getByTestId('synon-biomed-ask-user-card');
        await expect(card).toBeVisible();
        await assertSingleSurfaceCard(card);
        await assertInsideViewport(card, viewport.width);
        await assertNoHorizontalPageOverflow(page);
        await expect(card).toHaveScreenshot(`ask-user-typed-${viewport.name}.png`, {
          animations: 'disabled',
          maxDiffPixelRatio: 0.02,
        });

        await page.evaluate(() => {
          document.documentElement.dataset.theme = 'dark';
          document.body.setAttribute('arco-theme', 'dark');
        });
        await assertSingleSurfaceCard(card);
        await expect(card).toHaveScreenshot(`ask-user-typed-${viewport.name}-dark.png`, {
          animations: 'disabled',
          maxDiffPixelRatio: 0.02,
        });
      } finally {
        await removeScientificWorkspace(page, workspace);
      }
    });

    test('renders and operates a real typed pending ask_user selection', async ({ page }) => {
      await loginToScientificWorkbench(page);
      const workspace = await createScientificWorkspace(page, `ask-user-${viewport.name}`);
      const frameId = workspace.conversationId;
      try {
        seedPendingQuestion(frameId);
        await page.goto(`/#/conversation/${encodeURIComponent(frameId)}`);

        const card = page.getByTestId('synon-biomed-ask-user-card');
        await expect(card).toBeVisible();
        await expect(card.getByRole('heading', { name: '请选择需要进入下一轮验证的候选化合物' })).toBeVisible();
        await expect(card.getByText('You decide for me')).toHaveCount(0);
        await assertInsideViewport(card, viewport.width);
        await assertNoHorizontalPageOverflow(page);
        await expect(card).toHaveScreenshot(`ask-user-typed-${viewport.name}.png`, {
          animations: 'disabled',
          maxDiffPixelRatio: 0.02,
        });

        await card.getByRole('checkbox', { name: /乙醇对照/ }).click();
        await expect(card.getByRole('checkbox', { name: /乙醇对照/ })).toHaveAttribute('aria-checked', 'true');
        await card.getByRole('textbox', { name: '自定义回答' }).fill('保留阿司匹林对照');
        await card.getByRole('button', { name: '发送回答' }).click();
        await expect(card.getByLabel('自定义选择')).toContainText('保留阿司匹林对照');
        await expect(card.getByRole('button', { name: '提交' })).toBeEnabled();
        await card.getByRole('button', { name: '提交' }).click();
        const history = page.getByTestId('synon-biomed-ask-user-history');
        await expect(history).toBeVisible();
        const editAnswer = history.getByRole('button', { name: '修改答案' });
        await expect(editAnswer).toBeVisible();
        await expect(editAnswer).toBeEnabled();

        await page.reload();
        const restoredHistory = page.getByTestId('synon-biomed-ask-user-history');
        await expect(restoredHistory).toBeVisible();
        const restoredEditAnswer = restoredHistory.getByRole('button', { name: '修改答案' });
        await expect(restoredEditAnswer).toBeEnabled();
        await restoredEditAnswer.click();
        const restoredEditor = page.getByTestId('synon-biomed-ask-user-history-editing');
        await expect(restoredEditor).toBeVisible();
        await expect(restoredEditor).toContainText('修改答案后，将从这个问题重新运行并创建新的会话分支。');
        await assertInsideViewport(restoredEditor, viewport.width);
        await assertNoHorizontalPageOverflow(page);
      } finally {
        await removeScientificWorkspace(page, workspace);
      }
    });
  });
}

function seedPendingQuestion(frameId: string): void {
  applySynonGoFrameFixture({ frameId, status: 'processing' });
  seedSynonGoAskUserFixture({
    frameId,
    toolId: requestToolID(frameId),
    questions: [
      {
        header: '候选化合物',
        question: '请选择需要进入下一轮验证的候选化合物',
        multi_select: true,
        options: [
          {
            label: '乙醇对照',
            description: '用于验证小分子结构缩略图。',
          },
          {
            label: '阿司匹林',
            description: '用于验证芳香环与官能团渲染。',
          },
          {
            label: '无效结构',
            description: '验证无效 SMILES 的稳定降级。',
          },
          {
            label: 'You decide for me',
            description: '后端特殊选项，不应与内置选择重复显示。',
          },
        ],
      },
    ],
  });
}

function requestToolID(frameId: string): string {
  return `toolu_ask_user_rdkit_${frameId}`;
}

async function assertInsideViewport(locator: Locator, viewportWidth: number) {
  await expect
    .poll(async () => {
      const box = await locator.boundingBox();
      return box !== null && box.x >= -1 && box.x + box.width <= viewportWidth + 1;
    })
    .toBe(true);
}

async function assertSingleSurfaceCard(card: Locator) {
  const contract = await card.evaluate((root) => {
    const borderWidth = (element: Element | null) => (element ? getComputedStyle(element).borderTopWidth : null);
    return {
      card: borderWidth(root),
      composer: borderWidth(root.querySelector('.synon-ask-user-card__composer')),
      input: borderWidth(root.querySelector('.synon-ask-user-card__input')),
      buttons: Array.from(root.querySelectorAll('.arco-btn')).map((button) => borderWidth(button)),
    };
  });
  expect(contract.card).toBe('0px');
  expect(contract.composer).toBe('0px');
  expect(contract.input).toBe('0px');
  expect(contract.buttons.length).toBeGreaterThan(0);
  expect(contract.buttons.every((width) => width === '0px')).toBe(true);
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
