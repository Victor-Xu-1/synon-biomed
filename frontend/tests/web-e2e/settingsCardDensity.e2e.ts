import { expect, test } from './officialChromeTest';
import { webPassword, webUsername } from './synonGoWebCredentials';

async function signIn(page: import('@playwright/test').Page) {
  await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
  await page.getByRole('combobox').selectOption('zh-CN');
  await page.getByRole('textbox', { name: '用户名' }).fill(webUsername);
  await page.getByRole('textbox', { name: '密码' }).fill(webPassword);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page).toHaveURL(/#\/(guid|settings\/.+)/);
}

for (const route of ['skills', 'tools', 'environments'] as const) {
  test(`keeps ${route} card sheet inside the desktop viewport`, async ({ page }, info) => {
    await signIn(page);
    await page.goto(`/#/settings/${route}`, { waitUntil: 'domcontentloaded' });
    const root = page.locator('.settings-page-wrapper');
    await expect(root).toBeVisible();
    const grid = page.locator(
      route === 'skills'
        ? '[data-testid="synon-biomed-skill-grid"]'
        : route === 'tools'
          ? '[data-testid="synon-biomed-mcp-grid"]'
          : '[data-testid="scientific-environments"] .environment-grid'
    );
    await expect(grid).toBeVisible({ timeout: 20_000 });
    const metrics = await page.evaluate((routeName) => {
      const wrapper = document.querySelector('.settings-page-wrapper');
      const content = document.querySelector('.settings-page-content');
      const pageRoot = document.querySelector(
        routeName === 'skills'
          ? '.settings-skills-page'
          : routeName === 'tools'
            ? '.mcp-library'
            : '.environment-library'
      );
      const grid = document.querySelector(
        routeName === 'skills'
          ? '[data-testid="synon-biomed-skill-grid"]'
          : routeName === 'tools'
            ? '[data-testid="synon-biomed-mcp-grid"]'
            : '[data-testid="scientific-environments"] .environment-grid'
      );
      const scrollOwner = grid?.parentElement;
      const header = document.querySelector('.settings-page-header');
      const toolbar = document.querySelector(
        routeName === 'skills'
          ? '.settings-skill-library-toolbar'
          : routeName === 'tools'
            ? '.mcp-library-toolbar'
            : '.environment-toolbar'
      );
      const controls = [...(toolbar?.querySelectorAll<HTMLElement>('input, select, button, .arco-input-wrapper') ?? [])]
        .map((control) => {
          const rect = control.getBoundingClientRect();
          const style = getComputedStyle(control);
          return {
            tag: control.tagName,
            width: rect.width,
            height: rect.height,
            bottom: rect.bottom,
            display: style.display,
          };
        })
        .filter((control) => control.width > 0 && control.height > 0);
      const cards = [...(grid?.children ?? [])] as HTMLElement[];
      const rects = cards.map((card) => {
        const rect = card.getBoundingClientRect();
        return {
          x: rect.x,
          y: rect.y,
          width: rect.width,
          height: rect.height,
          right: rect.right,
          bottom: rect.bottom,
        };
      });
      return {
        viewport: { width: window.innerWidth, height: window.innerHeight },
        document: { width: document.documentElement.scrollWidth, height: document.documentElement.scrollHeight },
        header: header ? { width: header.clientWidth, height: header.clientHeight } : null,
        toolbar: toolbar ? { width: toolbar.clientWidth, height: toolbar.clientHeight } : null,
        controls,
        wrapper: wrapper
          ? { width: wrapper.clientWidth, height: wrapper.clientHeight, scrollHeight: wrapper.scrollHeight }
          : null,
        content: content
          ? { width: content.clientWidth, height: content.clientHeight, scrollHeight: content.scrollHeight }
          : null,
        pageRoot: pageRoot
          ? { width: pageRoot.clientWidth, height: pageRoot.clientHeight, scrollHeight: pageRoot.scrollHeight }
          : null,
        grid: grid
          ? {
              width: grid.clientWidth,
              height: grid.clientHeight,
              scrollHeight: grid.scrollHeight,
              style: getComputedStyle(grid).gridTemplateRows,
              overflowY: getComputedStyle(grid).overflowY,
            }
          : null,
        scrollOwner: scrollOwner
          ? {
              className: scrollOwner.className,
              width: scrollOwner.clientWidth,
              height: scrollOwner.clientHeight,
              scrollHeight: scrollOwner.scrollHeight,
              display: getComputedStyle(scrollOwner).display,
              flex: getComputedStyle(scrollOwner).flex,
              minHeight: getComputedStyle(scrollOwner).minHeight,
              overflowY: getComputedStyle(scrollOwner).overflowY,
            }
          : null,
        cards: rects,
        cardCount: cards.length,
      };
    }, route);
    console.log(`CARD_METRICS ${route} ${JSON.stringify(metrics)}`);
    expect(metrics.cardCount).toBeGreaterThan(0);
    expect(metrics.document.width).toBeLessThanOrEqual(metrics.viewport.width + 1);
    expect(metrics.document.height).toBeLessThanOrEqual(metrics.viewport.height + 1);
    expect(metrics.header, `${route} compact header metrics`).not.toBeNull();
    expect(metrics.toolbar, `${route} compact toolbar metrics`).not.toBeNull();
    expect(metrics.header?.height, `${route} header height`).toBeLessThanOrEqual(80);
    expect(metrics.toolbar?.height, `${route} toolbar height`).toBeLessThanOrEqual(42);
    expect(metrics.controls.length, `${route} visible toolbar controls`).toBeGreaterThan(0);
    for (const [index, control] of metrics.controls.entries()) {
      expect(control.height, `${route} control ${index} compact height`).toBeLessThanOrEqual(34);
      expect(control.bottom, `${route} control ${index} bottom edge`).toBeLessThanOrEqual(metrics.viewport.height + 1);
    }
    for (const [name, box] of [
      ['wrapper', metrics.wrapper],
      ['content', metrics.content],
      ['pageRoot', metrics.pageRoot],
      ['grid', metrics.grid],
      ['scrollOwner', metrics.scrollOwner],
    ] as const) {
      expect(box, `${route} ${name} metrics`).not.toBeNull();
      expect(box?.scrollHeight, `${route} ${name} vertical overflow`).toBeLessThanOrEqual((box?.height ?? 0) + 1);
    }
    for (const [index, card] of metrics.cards.entries()) {
      expect(card.x, `${route} card ${index} left edge`).toBeGreaterThanOrEqual(-1);
      expect(card.y, `${route} card ${index} top edge`).toBeGreaterThanOrEqual(-1);
      expect(card.right, `${route} card ${index} right edge`).toBeLessThanOrEqual(metrics.viewport.width + 1);
      expect(card.bottom, `${route} card ${index} bottom edge`).toBeLessThanOrEqual(metrics.viewport.height + 1);
    }
    await page.screenshot({ path: info.outputPath(`${route}-cards.png`), fullPage: true });
  });
}
