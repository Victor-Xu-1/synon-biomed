import { expect, test, type Locator, type Page } from '@playwright/test';
import { webPassword, webUsername } from './synonGoWebCredentials';

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('renders and operates the complete native Skills workspace', async ({ page }) => {
      await login(page);
      await page.goto('/#/settings/skills');

      const workspace = page.getByTestId('synon-biomed-skills-section');
      await expect(workspace).toBeVisible();
      await expect(page.getByTestId('add-skill-button')).toBeVisible();
      await expect(page.getByRole('tab', { name: /推荐/ })).toBeVisible();
      await expect(page.getByRole('tab', { name: /已导入/ })).toBeVisible();
      await expect(page.getByRole('tab', { name: /个人/ })).toBeVisible();
      await expect(page.getByTestId('skill-category-filter')).toBeVisible();
      const recommendedGrid = page.getByTestId('synon-biomed-skill-grid');
      await expect(recommendedGrid.locator('[data-testid^="synon-biomed-skill-row-"]').first()).toBeVisible();
      const allRecommendedCount = await recommendedGrid.locator('[data-testid^="synon-biomed-skill-row-"]').count();
      await page.getByTestId('skill-category-filter-drug-discovery').click();
      await expect(recommendedGrid.locator('[data-testid^="synon-biomed-skill-row-"]').first()).toBeVisible();
      await expect
        .poll(() => recommendedGrid.locator('[data-testid^="synon-biomed-skill-row-"]').count())
        .toBeLessThan(allRecommendedCount);
      await page.getByTestId('skill-category-filter-all').click();
      await assertNoHorizontalPageOverflow(page);

      const recommendedRow = workspace.locator('[data-testid^="synon-biomed-skill-row-"]').first();
      await recommendedRow.click();
      const detailModal = page.getByTestId('skill-detail-modal');
      await expect(detailModal).toBeVisible();
      await expect(detailModal.getByTestId('skill-markdown')).toContainText('AlphaFold2');
      await expect(detailModal.getByRole('textbox', { name: 'Skill 文件内容' })).toHaveCount(0);
      const detailDialog = page.locator('.arco-modal').filter({ has: detailModal });
      await expect(detailDialog.getByRole('button', { name: '创建可编辑副本' })).toBeVisible();
      await expect(detailDialog.getByText('Synon Biomed', { exact: true })).toBeVisible();
      await assertInsideViewport(detailDialog, viewport);
      await assertModalPartsInsideViewport(detailDialog, viewport);
      await detailDialog.getByLabel('Close').click();

      const personalTab = page.getByRole('tab', { name: /个人/ });
      await personalTab.click();
      await expect(personalTab).toHaveAttribute('aria-selected', 'true');

      await page.getByTestId('add-skill-button').click();
      await page.getByText('创建个人 Skill', { exact: true }).click();
      const createModal = page.getByTestId('create-personal-skill-modal');
      await expect(createModal).toBeVisible();
      await expect(createModal.getByRole('textbox', { name: 'Skill 名称' })).toBeVisible();
      const createDialog = page.locator('.arco-modal').filter({ has: createModal });
      await assertInsideViewport(createDialog, viewport);
      await createDialog.getByLabel('Close').click();

      const marketplaceTab = page.getByRole('tab', { name: /在线市场/ });
      await marketplaceTab.click();
      const marketplace = page.getByTestId('synon-biomed-skill-market');
      await expect(marketplace).toBeVisible();
      const marketplaceGrid = page.getByTestId('synon-biomed-skill-market-grid');
      const unavailableNotice = marketplace.getByText('部分来源暂不可用，已加载的 Skill 仍可使用。');
      await expect
        .poll(
          async () => {
            if (await marketplaceGrid.isVisible()) return 'ready';
            if (await unavailableNotice.isVisible()) return 'unavailable';
            return 'loading';
          },
          { timeout: 15_000 }
        )
        .not.toBe('loading');
      await expect(page.getByTestId('synon-biomed-skill-market-results')).toContainText('生物医药 Skill');
      if (await marketplaceGrid.isVisible()) {
        await expect(marketplace.getByText('中文介绍').first()).toBeVisible();
        await expect(marketplace.getByText('英文原文').first()).toBeVisible();
        const initialMarketCardTexts = await marketplaceGrid
          .locator('[data-testid^="synon-biomed-skill-market-card-"]')
          .evaluateAll((cards) => cards.map((card) => card.textContent ?? ''));
        expect(initialMarketCardTexts.length).toBeGreaterThan(0);
        expect(initialMarketCardTexts.every((text) => text.includes('Bioconductor'))).toBe(true);
        const gridColumns = await marketplaceGrid.evaluate(
          (element) => getComputedStyle(element).gridTemplateColumns.split(' ').filter(Boolean).length
        );
        expect(gridColumns).toBe(viewport.name === 'narrow' ? 1 : 4);
        const firstMarketCard = marketplaceGrid.locator('[data-testid^="synon-biomed-skill-market-card-"]').first();
        await expect(firstMarketCard).toBeVisible();
        await expect
          .poll(async () => {
            const box = await firstMarketCard.boundingBox();
            return box !== null && box.x >= -1 && box.width <= viewport.width + 1;
          })
          .toBe(true);
      } else {
        await expect(unavailableNotice).toBeVisible();
        await expect(marketplace.getByText('暂不可用').first()).toBeVisible();
        await expect(marketplace.getByRole('button', { name: '重试' })).toBeVisible();
        await expect(marketplaceGrid).toHaveCount(0);
      }
      await assertNoHorizontalPageOverflow(page);

      await page.getByTestId('add-skill-button').click();
      await page.getByText('从 GitHub 导入', { exact: true }).click();
      const githubModal = page.getByTestId('github-skill-import-modal');
      await expect(githubModal).toBeVisible();
      await expect(githubModal.getByRole('textbox', { name: 'GitHub 仓库' })).toBeVisible();
      await assertInsideViewport(page.locator('.arco-modal').filter({ has: githubModal }), viewport);
    });
  });
}

async function assertNoHorizontalPageOverflow(page: Page) {
  await expect
    .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1))
    .toBe(true);
}

async function login(page: Page) {
  await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
  await page.getByRole('combobox').selectOption('zh-CN');
  await page.getByRole('textbox', { name: '用户名' }).fill(webUsername);
  await page.getByRole('textbox', { name: '密码' }).fill(webPassword);
  await page.getByRole('button', { name: '登录' }).click();
  await expect(page).toHaveURL(/#\/guid/);
}

async function assertInsideViewport(locator: Locator, viewport: { width: number; height: number }) {
  await expect
    .poll(async () => {
      const box = await locator.boundingBox();
      return (
        box !== null &&
        box.x >= -1 &&
        box.y >= -1 &&
        box.x + box.width <= viewport.width + 1 &&
        box.y + box.height <= viewport.height + 1
      );
    })
    .toBe(true);
}

async function assertModalPartsInsideViewport(dialog: Locator, viewport: { width: number; height: number }) {
  await expect
    .poll(async () => {
      const dialogBox = await dialog.boundingBox();
      const contentBox = await dialog.locator('.arco-modal-content').boundingBox();
      return (
        dialogBox !== null &&
        contentBox !== null &&
        contentBox.x >= dialogBox.x - 1 &&
        contentBox.y >= dialogBox.y - 1 &&
        contentBox.x + contentBox.width <= dialogBox.x + dialogBox.width + 1 &&
        contentBox.y + contentBox.height <= dialogBox.y + dialogBox.height + 1 &&
        contentBox.x + contentBox.width <= viewport.width + 1 &&
        contentBox.y + contentBox.height <= viewport.height + 1
      );
    })
    .toBe(true);
}
