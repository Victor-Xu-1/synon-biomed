import { expect, test, type Locator, type Page } from '@playwright/test';
import { SETTINGS_VISUAL_CONTRACTS } from '../../packages/desktop/src/renderer/pages/settings/components/settingsVisualContract';
import { loginToScientificWorkbench } from './synonBiomedScientificFixture';

const modules = [
  { route: 'account', ready: '[data-testid="synon-account-settings"]' },
  { route: 'plans-usage', ready: '[data-testid="synon-plans-usage-settings"]' },
  { route: 'experts', ready: '[data-testid="expert-list-page"]' },
  { route: 'skills', ready: '[data-testid="synon-biomed-skills-section"]' },
  { route: 'tools', ready: '[data-testid="synon-biomed-mcp-settings"]' },
  { route: 'environments', ready: '[data-testid="scientific-environments"]' },
  { route: 'models', ready: '[data-testid="models-header"]' },
  { route: 'compute', ready: '[data-testid="synon-biomed-compute-section"]' },
  // The memory surface can load after the shared page heading.
  { route: 'governance', ready: '[data-testid="memory-manager"]' },
  { route: 'network', ready: '[data-testid="synon-network-settings"]' },
  { route: 'credentials', ready: '[data-testid="synon-credentials-settings"]' },
  { route: 'storage', ready: '[data-testid="synon-storage-settings"]' },
  { route: 'general', ready: '[data-testid="synon-general-settings"]' },
] as const;

const libraryRoutes = new Set(['experts', 'skills', 'tools', 'environments']);

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
      if (module.route === 'account') {
        // Account uses the profile hero as its heading instead of the shared page header.
        await expect(wrapper.locator('#account-profile-name')).toBeVisible();
        const accentLayer = await wrapper
          .locator('.account-profile-hero')
          .evaluate((hero) => getComputedStyle(hero, '::after').zIndex);
        expect(accentLayer).toBe('-1');
      } else if (libraryRoutes.has(module.route)) {
        await expect(wrapper.locator('.settings-library-tab-header__title')).toBeVisible();
        await expect(wrapper.getByTestId(`settings-tab-${module.route}`)).toHaveAttribute('aria-selected', 'true');
      } else {
        await expect(wrapper.locator('.settings-page-header__title')).toBeVisible();
      }
      const desktopReference = SETTINGS_VISUAL_CONTRACTS[module.route].reference.desktop;
      if (desktopReference) {
        await expect(wrapper).toHaveAttribute('data-settings-reference-desktop', desktopReference);
      } else {
        await expect(wrapper).not.toHaveAttribute('data-settings-reference-desktop');
      }
      await expect(page.getByTestId('synon-biomed-brand-lockup')).toHaveAttribute(
        'src',
        './branding/synon-biomed-lockup.png?v=0cac2ebf'
      );

      await assertInsideViewport(wrapper, 1536);
      await assertNoHorizontalPageOverflow(page);
      if (module.route !== 'account') {
        await assertHeaderReadable(
          wrapper,
          libraryRoutes.has(module.route) ? '.settings-library-tab-header__title' : '.settings-page-header__title'
        );
      }
      await assertSharedGeometry(
        wrapper,
        module.route === 'account' ? '12px' : '16px',
        module.route !== 'account' && module.route !== 'network'
      );
      await assertGeneratedAssets(wrapper, module.route !== 'storage' && module.route !== 'environments');
      if (module.route === 'skills' || module.route === 'tools') {
        await assertMergedLibraryGrid(wrapper, module.route);
      }

      const screenshot = testInfo.outputPath(`settings-${module.route}.png`);
      await page.screenshot({ path: screenshot, fullPage: true });
      await testInfo.attach(`settings-${module.route}`, { path: screenshot, contentType: 'image/png' });
    }

    expect(pageErrors).toEqual([]);
  });

  test('keeps key settings surfaces usable in dark narrow view', async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await loginToScientificWorkbench(page);
    await page.goto('/#/settings/general', { waitUntil: 'domcontentloaded' });
    await page.getByTestId('theme-family-select').click();
    await page.getByRole('option', { name: '夜间暗色系' }).click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');

    for (const route of ['account', 'skills', 'tools'] as const) {
      await page.goto(`/#/settings/${route}`, { waitUntil: 'domcontentloaded' });
      await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
      const wrapper = page.locator(`.settings-page-wrapper[data-settings-route="${route}"]`);
      await expect(wrapper).toBeVisible();
      await expect(wrapper.locator('.settings-mobile-top-nav')).toBeVisible();
      await assertNoHorizontalPageOverflow(page);
      const surface =
        route === 'account'
          ? wrapper.locator('.account-profile-hero')
          : route === 'skills'
            ? wrapper.locator('.settings-entity-card').first()
            : wrapper.locator('.synon-mcp-card').first();
      await expect(surface).toBeVisible();
      if (route === 'account') await assertAvatarContrast(wrapper);
      await page.screenshot({ path: testInfo.outputPath(`settings-${route}-dark-narrow.png`), fullPage: true });
    }
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
    await expect(page.locator('[data-testid="expert-list-page"] .expert-card').first()).toBeVisible();
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

async function assertSharedGeometry(wrapper: Locator, expectedCardRadius: string, elevated: boolean) {
  const geometry = await wrapper.evaluate((root, cardRadius) => {
    const content = root.querySelector<HTMLElement>('.settings-page-content');
    const header = [
      ...root.querySelectorAll<HTMLElement>('h1, h2, h3, [role="tab"], .settings-page-header__title'),
    ].find((element) => {
      const rect = element.getBoundingClientRect();
      return rect.width > 0 && rect.height > 0;
    });
    if (!content || !header) return null;
    const contentRect = content.getBoundingClientRect();
    const headerStyle = getComputedStyle(header);
    const cardSelectors =
      '.settings-section, .settings-list, .settings-summary-strip, .settings-toolbar, .settings-library-card, .settings-entity-card, .synon-mcp-card, .account-profile-hero, .expert-card, .expert-group, .expert-row, .settings-model-profile, .compute-section, .network-section--preset-groups, .credentials-connection-card, .storage-usage-card, .memory-manager__status, .memory-manager__workspace';
    const findVisualCard = () => {
      const explicit = [...root.querySelectorAll<HTMLElement>(cardSelectors)].find(
        (candidate) => getComputedStyle(candidate).borderRadius === cardRadius
      );
      if (explicit) return explicit;
      return [...root.querySelectorAll<HTMLElement>('*')].find((element) => {
        const rect = element.getBoundingClientRect();
        const style = getComputedStyle(element);
        return (
          rect.width >= 280 && rect.height >= 60 && style.borderRadius === cardRadius && style.borderTopWidth !== '0px'
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
  }, expectedCardRadius);

  expect(geometry).not.toBeNull();
  expect(geometry?.contentWidth ?? 0).toBeGreaterThan(1100);
  expect(geometry?.contentLeft ?? -1).toBeGreaterThanOrEqual(280);
  expect(geometry?.contentLeft ?? Number.POSITIVE_INFINITY).toBeLessThanOrEqual(330);
  expect(geometry?.titleSize ?? 0).toBeGreaterThanOrEqual(14);
  expect(geometry?.cardRadius).toBe(expectedCardRadius);
  if (elevated) {
    expect(geometry?.cardShadow).not.toBe('none');
  } else {
    expect(geometry?.cardShadow).toBe('none');
  }
}

async function assertHeaderReadable(wrapper: Locator, selector: string) {
  const title = wrapper.locator(selector);
  await expect(title).toBeVisible();
  const box = await title.boundingBox();
  expect(box?.width ?? 0).toBeGreaterThan(20);
  expect(box?.height ?? 0).toBeGreaterThan(20);

  const description = wrapper.locator('.settings-page-header__description');
  if (await description.count()) await expect(description).toBeVisible();
}

async function assertAvatarContrast(wrapper: Locator) {
  const ratio = await wrapper.locator('.account-profile-avatar').evaluate((avatar) => {
    const style = getComputedStyle(avatar);
    const luminance = (color: string) => {
      const channels = color
        .match(/[\d.]+/g)
        ?.slice(0, 3)
        .map(Number);
      if (!channels || channels.length !== 3) throw new Error(`Cannot read avatar color: ${color}`);
      const linear = channels.map((value) => {
        const normalized = value / 255;
        return normalized <= 0.04045 ? normalized / 12.92 : ((normalized + 0.055) / 1.055) ** 2.4;
      });
      return linear[0] * 0.2126 + linear[1] * 0.7152 + linear[2] * 0.0722;
    };
    const foreground = luminance(style.color);
    const background = luminance(style.backgroundColor);
    return (Math.max(foreground, background) + 0.05) / (Math.min(foreground, background) + 0.05);
  });
  expect(ratio).toBeGreaterThanOrEqual(4.5);
}

async function assertGeneratedAssets(wrapper: Locator, requireArtwork: boolean) {
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

  if (requireArtwork) expect(assets.count).toBeGreaterThan(0);
  expect(assets.broken).toBe(0);
  expect(assets.outsideRoot).toBe(0);
  expect(assets.decorativeSvgCount).toBe(0);
  expect(assets.connectorImagesWithoutGeneratedMarker).toBe(0);
}

async function assertMergedLibraryGrid(wrapper: Locator, route: 'skills' | 'tools') {
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
