import { expect, test, type Locator, type Page } from '@playwright/test';
import { loginToScientificWorkbench } from './synonBiomedScientificFixture';

const modules = [
  { route: 'account', ready: '[data-testid="synon-account-settings"]' },
  { route: 'plans-usage', ready: '[data-testid="synon-plans-usage-settings"]' },
  { route: 'experts', ready: '[data-testid="expert-list-page"]' },
  { route: 'skills', ready: '[data-testid="synon-biomed-skills-section"]' },
  { route: 'tools', ready: '[data-testid="synon-biomed-mcp-settings"]' },
  { route: 'models', ready: '[data-testid="models-header"]' },
  { route: 'compute', ready: '[data-testid="synon-biomed-compute-section"]' },
  // The v3 shell keeps the page heading semantic-only; wait on the visible
  // memory surface instead of the intentionally collapsed header.
  { route: 'governance', ready: '[data-testid="memory-manager"]' },
  { route: 'network', ready: '[data-testid="synon-network-settings"]' },
  { route: 'credentials', ready: '[data-testid="synon-credentials-settings"]' },
  { route: 'storage', ready: '[data-testid="synon-storage-settings"]' },
  { route: 'general', ready: '[data-testid="synon-general-settings"]' },
] as const;

test.describe('unified settings visual system', () => {
  test.use({ viewport: { width: 1536, height: 1024 } });

  test('adapts every existing module without replacing the product brand', async ({ page }, testInfo) => {
    test.setTimeout(180_000);
    await loginToScientificWorkbench(page);

    const pageErrors: string[] = [];
    page.on('pageerror', (error) => pageErrors.push(error.message));

    for (const module of modules) {
      await page.goto(`/#/settings/${module.route}`, { waitUntil: 'domcontentloaded' });
      await expect(page.locator(module.ready)).toBeVisible();
      await waitForModuleContent(page, module.route);

      const wrapper = page.locator(
        `.settings-page-wrapper[data-settings-route="${module.route}"][data-settings-visual-system="scientific-connectors-v3"]`
      );
      await expect(wrapper).toBeVisible();
      await expect(wrapper.locator('.settings-page-header__title')).toBeAttached();
      await expect(wrapper).toHaveAttribute(
        'data-settings-reference-desktop',
        `settings-design-v3-${module.route}.png`
      );
      await expect(page.getByTestId('synon-biomed-brand-lockup')).toHaveAttribute(
        'src',
        './branding/synon-biomed-lockup.png?v=0cac2ebf'
      );

      await assertInsideViewport(wrapper, 1536);
      await assertNoHorizontalPageOverflow(page);
      await assertHeaderIntroCollapsed(wrapper);
      await assertSharedGeometry(wrapper);
      await assertGeneratedAssets(wrapper);
      if (module.route === 'skills' || module.route === 'tools') {
        await assertCompactFourColumnGrid(wrapper, module.route);
      }

      const screenshot = testInfo.outputPath(`settings-${module.route}.png`);
      await page.screenshot({ path: screenshot, fullPage: true });
      await testInfo.attach(`settings-${module.route}`, { path: screenshot, contentType: 'image/png' });
    }

    expect(pageErrors).toEqual([]);
  });
});

async function waitForModuleContent(page: Page, route: (typeof modules)[number]['route']) {
  if (route === 'account') {
    await expect
      .poll(
        () =>
          page.evaluate(() => {
            const root = document.querySelector('[data-testid="synon-account-settings"]');
            if (!root) return false;
            return Boolean(root.querySelector('.account-activity') || root.querySelector('.account-error-panel'));
          }),
        { timeout: 30_000 }
      )
      .toBe(true);
  }
  if (route === 'skills') await expect(page.locator('.settings-entity-card').first()).toBeVisible();
  if (route === 'experts') {
    await expect
      .poll(
        () =>
          page.evaluate(() => {
            const root = document.querySelector('[data-testid="expert-list-page"]');
            return Boolean(root && (root.querySelector('.expert-row') || root.querySelector('[role="alert"]')));
          }),
        { timeout: 30_000 }
      )
      .toBe(true);
  }
  if (route === 'tools') {
    await expect
      .poll(
        () =>
          page.evaluate(() => {
            const root = document.querySelector('[data-testid="synon-biomed-mcp-settings"]');
            return Boolean(root && (root.querySelector('.synon-mcp-card') || root.querySelector('[role="alert"]')));
          }),
        { timeout: 30_000 }
      )
      .toBe(true);
  }
  if (route === 'compute') await expect(page.locator('.compute-section').first()).toBeVisible();
  if (route === 'storage') {
    await page.waitForTimeout(1_500);
  }
}

async function assertSharedGeometry(wrapper: Locator) {
  const geometry = await wrapper.evaluate((root) => {
    const content = root.querySelector<HTMLElement>('.settings-page-content');
    const header = [...root.querySelectorAll<HTMLElement>('h2, h3, [role="tab"], .settings-page-header__title')].find(
      (element) => {
        const rect = element.getBoundingClientRect();
        return rect.width > 0 && rect.height > 0;
      }
    );
    if (!content || !header) return null;
    const contentRect = content.getBoundingClientRect();
    const headerStyle = getComputedStyle(header);
    const cardSelectors =
      '.settings-section, .settings-list, .settings-summary-strip, .settings-toolbar, .settings-entity-card, .synon-mcp-card, .account-profile-hero, .expert-group, .expert-row, .settings-model-profile, .compute-section, .network-section, .credentials-connection-card, .storage-usage-card, .memory-manager__status, .memory-manager__workspace';
    const findVisualCard = () => {
      const explicit = root.querySelector<HTMLElement>(cardSelectors);
      if (explicit) {
        const explicitStyle = getComputedStyle(explicit);
        if (explicitStyle.borderRadius === '16px') return explicit;
      }
      return [...root.querySelectorAll<HTMLElement>('*')].find((element) => {
        const rect = element.getBoundingClientRect();
        const style = getComputedStyle(element);
        return (
          rect.width >= 280 &&
          rect.height >= 60 &&
          style.borderRadius === '16px' &&
          style.boxShadow === 'none' &&
          style.borderTopWidth !== '0px'
        );
      });
    };
    const card = findVisualCard();
    return {
      contentWidth: contentRect.width,
      contentLeft: contentRect.left,
      titleSize: Number.parseFloat(headerStyle.fontSize),
      cardRadius: card ? getComputedStyle(card).borderRadius : null,
      cardShadow: card ? getComputedStyle(card).boxShadow : null,
    };
  });

  expect(geometry).not.toBeNull();
  expect(geometry?.contentWidth ?? 0).toBeGreaterThan(1100);
  expect(geometry?.contentLeft ?? -1).toBeGreaterThanOrEqual(280);
  expect(geometry?.contentLeft ?? Number.POSITIVE_INFINITY).toBeLessThanOrEqual(330);
  expect(geometry?.titleSize ?? 0).toBeGreaterThanOrEqual(14);
  expect(geometry?.cardRadius).toBe('16px');
  expect(geometry?.cardShadow).toBe('none');
}

async function assertHeaderIntroCollapsed(wrapper: Locator) {
  const headerState = await wrapper.locator('.settings-page-header').evaluate((header) => {
    const title = header.querySelector<HTMLElement>('.settings-page-header__title');
    const description = header.querySelector<HTMLElement>('.settings-page-header__description');
    const titleRect = title?.getBoundingClientRect();
    return {
      titlePresent: Boolean(title),
      titleVisualSize: titleRect ? `${Math.round(titleRect.width)}x${Math.round(titleRect.height)}` : null,
      descriptionDisplay: description ? getComputedStyle(description).display : null,
    };
  });

  expect(headerState.titlePresent).toBe(true);
  expect(headerState.titleVisualSize).toBe('1x1');
  expect(headerState.descriptionDisplay).toBe('none');
}

async function assertGeneratedAssets(wrapper: Locator) {
  // Connector and skill artwork is loaded after the route shell becomes
  // visible. Give the browser a bounded settling window before treating an
  // in-flight image as a broken asset.
  await expect
    .poll(
      () =>
        wrapper.evaluate(
          (root) =>
            [...root.querySelectorAll<HTMLImageElement>('img[data-settings-generated-asset]')].filter(
              (image) => !image.complete || image.naturalWidth === 0
            ).length
        ),
      { timeout: 15_000 }
    )
    .toBe(0);
  const assets = await wrapper.evaluate((root) => {
    const images = [...root.querySelectorAll<HTMLImageElement>('img[data-settings-generated-asset]')];
    return {
      count: images.length,
      broken: images.filter((image) => !image.complete || image.naturalWidth === 0).length,
      outsideRoot: images.filter((image) => !image.getAttribute('src')?.includes('/branding/settings-generated-v3/'))
        .length,
      decorativeSvgCount: root.querySelectorAll('.molecular-identity-motif, .mcp-connector-visual__svg').length,
      connectorImagesWithoutGeneratedMarker: [...root.querySelectorAll('.mcp-connector-visual__image')].filter(
        (image) => !image.closest('[data-settings-generated-asset]')
      ).length,
    };
  });

  expect(assets.count).toBeGreaterThan(0);
  expect(assets.broken).toBe(0);
  expect(assets.outsideRoot).toBe(0);
  expect(assets.decorativeSvgCount).toBe(0);
  expect(assets.connectorImagesWithoutGeneratedMarker).toBe(0);
}

async function assertCompactFourColumnGrid(wrapper: Locator, route: 'skills' | 'tools') {
  const selector = route === 'skills' ? '[data-testid="synon-biomed-skill-grid"]' : '.synon-mcp-grid';
  const grid = wrapper.locator(selector).first();
  await expect(grid).toBeVisible();

  const layout = await grid.evaluate((element) => {
    const children = [...element.children].map((child) => child.getBoundingClientRect());
    const firstRowY = children.at(0)?.y;
    return {
      computedColumns: getComputedStyle(element).gridTemplateColumns.split(' ').filter(Boolean).length,
      firstRowCards: children.filter((rect) => Math.abs(rect.y - (firstRowY ?? rect.y)) < 1).length,
    };
  });

  expect(layout.computedColumns).toBe(4);
  expect(layout.firstRowCards).toBe(4);
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
