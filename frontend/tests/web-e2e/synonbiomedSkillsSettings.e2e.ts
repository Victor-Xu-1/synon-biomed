import { expect, test, type Locator, type Page } from '@playwright/test';
import { webPassword, webUsername } from './synonGoWebCredentials';

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'annotation', width: 1380, height: 1100 },
  { name: 'medium', width: 1024, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('anchors pagination while every catalog page keeps complete readable cards', async ({ page }) => {
      await login(page);
      await page.goto('/#/settings/skills');
      const pager = page.getByRole('navigation', { name: '技能列表分页' });
      await expect(pager).toBeVisible();
      const scroll = page.getByTestId('skill-library-scroll');
      const footer = page.locator('.settings-skill-library-footer');
      const initialBox = await footer.boundingBox();
      expect(initialBox).not.toBeNull();
      const pageButtons = pager.getByRole('button', { name: /^技能列表分页 \d+$/ });
      const pageCount = await pageButtons.count();
      for (let index = 0; index < pageCount; index += 1) {
        await pageButtons.nth(index).click();
        await expect(pageButtons.nth(index)).toHaveAttribute('aria-current', 'page');
        await expect.poll(() => scroll.evaluate((element) => element.scrollTop)).toBe(0);
        const box = await footer.boundingBox();
        expect(Math.abs(box!.y - initialBox!.y)).toBeLessThanOrEqual(1);
        await assertInsideViewport(footer, viewport);
        const clipped = await page.locator('.settings-skill-card').evaluateAll((cards) =>
          cards.flatMap((card) => {
            const bounds = card.getBoundingClientRect();
            return [
              ...card.querySelectorAll(
                '.settings-skill-card__title, .settings-skill-card__description, .settings-skill-card__footer'
              ),
            ]
              .filter((element) => {
                const child = element.getBoundingClientRect();
                return (
                  child.bottom > bounds.bottom + 1 ||
                  child.right > bounds.right + 1 ||
                  element.scrollHeight > element.clientHeight + 1
                );
              })
              .map((element) => element.textContent);
          })
        );
        expect(clipped).toEqual([]);
        await scroll.evaluate((element) => {
          element.scrollTop = element.scrollHeight;
        });
        expect(Math.abs((await footer.boundingBox())!.y - initialBox!.y)).toBeLessThanOrEqual(1);
      }
      await page.getByTestId('input-search-synon-biomed-skills').fill('alphafold');
      expect(Math.abs((await footer.boundingBox())!.y - initialBox!.y)).toBeLessThanOrEqual(1);
      await assertNoHorizontalPageOverflow(page);
    });

    test('renders and operates the complete native Skills workspace', async ({ page }) => {
      await login(page);
      await page.goto('/#/settings/skills');

      const workspace = page.getByTestId('synon-biomed-skills-section');
      await expect(workspace).toBeVisible();
      await expect(page.getByTestId('add-skill-button')).toBeVisible();
      await expect(page.getByRole('tablist')).toHaveCount(0);
      await expect(page.getByRole('heading', { name: /技能/ })).toBeVisible();
      await expect(page.getByRole('button', { name: '筛选', exact: true })).toBeVisible();
      await assertInsideViewport(page.getByRole('search', { name: '技能' }), viewport);
      await expect(page.getByTestId('skill-category-filter')).toBeVisible();
      const recommendedGrid = page.getByTestId('synon-biomed-skill-grid');
      await expect(recommendedGrid.locator('[data-testid^="synon-biomed-skill-row-"]').first()).toBeVisible();
      await page.getByRole('combobox', { name: '科研领域' }).selectOption('clinical-regulatory');
      await expect(recommendedGrid.locator('[data-testid^="synon-biomed-skill-row-"]').first()).toBeVisible();
      await expect(recommendedGrid.locator('[data-testid^="synon-biomed-skill-row-"]')).toHaveCount(7);
      for (const row of await recommendedGrid.locator('[data-testid^="synon-biomed-skill-row-"]').all()) {
        await expect(row).toContainText('临床开发、注册与上市后');
      }
      await page.getByRole('combobox', { name: '科研领域' }).selectOption('all');
      await assertNoHorizontalPageOverflow(page);

      const recommendedRow = workspace.locator('[data-testid^="synon-biomed-skill-row-"]').first();
      await recommendedRow.click();
      const detailModal = page.getByTestId('skill-detail-modal');
      await expect(detailModal).toBeVisible();
      await expect(detailModal.getByTestId('skill-markdown')).toContainText('AlphaFold2');
      await expect(detailModal.getByRole('textbox', { name: 'Skill 文件内容' })).toHaveCount(0);
      const detailDialog = page.locator('.arco-modal').filter({ has: detailModal });
      await expect(detailDialog.getByRole('button', { name: '创建可编辑副本' })).toBeVisible();
      await expect(detailDialog.getByText('内置', { exact: true })).toBeVisible();
      await expect(detailDialog.getByText('结构生物学与蛋白质工程', { exact: true })).toBeVisible();
      await assertInsideViewport(detailDialog, viewport);
      await assertModalPartsInsideViewport(detailDialog, viewport);
      expect((await detailDialog.boundingBox())!.height).toBeLessThanOrEqual(Math.min(960, viewport.height - 32));
      await detailDialog.getByLabel('Close').click();

      await page.getByTestId('synon-biomed-skills-filter').click();
      const sourceFilter = page.getByRole('combobox', { name: '来源' });
      await sourceFilter.selectOption('personal');
      await expect(sourceFilter).toHaveValue('personal');
      await page.getByRole('combobox', { name: '启用状态' }).selectOption('disabled');
      await expect(workspace.locator('[data-testid^="synon-biomed-skill-row-"]')).toHaveCount(0);
      await page.getByRole('button', { name: '重置筛选' }).click();
      await expect(sourceFilter).toHaveValue('all');
      await expect(page.getByRole('combobox', { name: '启用状态' })).toHaveValue('all');
      await page.getByTestId('synon-biomed-skills-filter').click();

      await page.getByTestId('add-skill-button').click();
      await page.getByText('创建个人 Skill', { exact: true }).click();
      const createModal = page.getByTestId('create-personal-skill-modal');
      await expect(createModal).toBeVisible();
      await expect(createModal.getByRole('textbox', { name: 'Skill 名称' })).toBeVisible();
      const createDialog = page.locator('.arco-modal').filter({ has: createModal });
      await assertInsideViewport(createDialog, viewport);
      await createDialog.getByLabel('Close').click();

      await page.getByTestId('input-search-synon-biomed-skills').fill('alphafold');
      await page.getByTestId('add-skill-button').click();
      await page.getByText('在线市场', { exact: true }).click();
      const marketDialog = page.locator('.arco-modal').filter({ has: page.getByTestId('skill-market-dialog') });
      await expect(marketDialog).toBeVisible();
      await assertInsideViewport(marketDialog, viewport);
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
        expect(gridColumns).toBeGreaterThanOrEqual(1);
        expect(gridColumns).toBeLessThanOrEqual(4);
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

      await marketDialog.getByLabel('Close').click();
      await expect(page.getByTestId('input-search-synon-biomed-skills')).toHaveValue('alphafold');
      await expect(workspace.locator('[data-testid^="synon-biomed-skill-row-"]').first()).toBeVisible();
      await page.getByTestId('input-search-synon-biomed-skills').clear();

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
