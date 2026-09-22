import { createHash } from 'node:crypto';
import { existsSync, readFileSync as readFileRaw } from 'node:fs';
import { resolveDesignTokens } from '../_helpers/designTokens';
import { describe, expect, it } from 'vitest';
import {
  SETTINGS_DESKTOP_VIEWPORT,
  SETTINGS_LOCKED_DESKTOP_REFERENCE_SRC,
  SETTINGS_MOBILE_VIEWPORT,
  SETTINGS_VISUAL_CONTRACTS,
  SETTINGS_VISUAL_ROUTE_IDS,
  SETTINGS_VISUAL_SYSTEM_ID,
} from '@/renderer/pages/settings/components/settingsVisualContract';
import {
  SETTINGS_GENERATED_ASSET_REVISION,
  SETTINGS_GENERATED_ARTWORK_SRC,
  SETTINGS_GENERATED_EMPTY_SRC,
  SETTINGS_GENERATED_ICON_SRC,
  SETTINGS_GENERATED_NAV_SRC,
} from '@/renderer/pages/settings/components/SettingsGeneratedAsset';

/** Stylesheets are read with primitives inlined; contracts keep pinning numbers. */
const readFileSync = (target: string | URL, encoding?: BufferEncoding): string =>
  `${target}`.endsWith('.css')
    ? resolveDesignTokens(readFileRaw(target, 'utf8'))
    : (readFileRaw(target, encoding) as unknown as string);

const layoutCss = readFileSync(
  new URL('../../../packages/desktop/src/renderer/pages/settings/components/settings-layout.css', import.meta.url),
  'utf8'
);
const settingsManifestCss = readFileSync(
  new URL('../../../packages/desktop/src/renderer/pages/settings/components/settings.css', import.meta.url),
  'utf8'
);
const settingsRouteSource = readFileSync(
  new URL('../../../packages/desktop/src/renderer/pages/settings/SettingsRoute.tsx', import.meta.url),
  'utf8'
);
const wrapperSource = readFileSync(
  new URL('../../../packages/desktop/src/renderer/pages/settings/components/SettingsPageWrapper.tsx', import.meta.url),
  'utf8'
);
const accountSettingsSource = readFileSync(
  new URL('../../../packages/desktop/src/renderer/pages/settings/AccountSettings.tsx', import.meta.url),
  'utf8'
);
const messageChannelsCss = readFileSync(
  new URL('../../../packages/desktop/src/renderer/pages/settings/MessageChannelsSettings.css', import.meta.url),
  'utf8'
);
const skillsCss = readFileSync(
  new URL('../../../packages/desktop/src/renderer/pages/settings/components/settings-skills.css', import.meta.url),
  'utf8'
);
const toolsCss = readFileSync(
  new URL('../../../packages/desktop/src/renderer/pages/settings/components/settings-tools.css', import.meta.url),
  'utf8'
);
const connectorsCss = readFileSync(
  new URL('../../../packages/desktop/src/renderer/pages/settings/components/settings-connectors.css', import.meta.url),
  'utf8'
);
const expertsCss = readFileSync(
  new URL('../../../packages/desktop/src/renderer/pages/settings/components/settings-experts.css', import.meta.url),
  'utf8'
);
const cardSurfacesCss = readFileSync(
  new URL(
    '../../../packages/desktop/src/renderer/pages/settings/components/settings-card-surfaces.css',
    import.meta.url
  ),
  'utf8'
);
const modelsCss = readFileSync(
  new URL('../../../packages/desktop/src/renderer/pages/settings/components/settings-models.css', import.meta.url),
  'utf8'
);
const routeLayoutsCss = readFileSync(
  new URL(
    '../../../packages/desktop/src/renderer/pages/settings/components/settings-route-layouts.css',
    import.meta.url
  ),
  'utf8'
);
const cardDensityCss = readFileSync(
  new URL(
    '../../../packages/desktop/src/renderer/pages/settings/components/settings-card-density.css',
    import.meta.url
  ),
  'utf8'
);
const componentsCss = [
  'settings-core.css',
  'settings-skills.css',
  'settings-compute-components.css',
  'settings-route-layouts.css',
  'settings-breakpoints.css',
  'settings-tabbed-pages.css',
]
  .map((file) =>
    readFileSync(
      new URL(`../../../packages/desktop/src/renderer/pages/settings/components/${file}`, import.meta.url),
      'utf8'
    )
  )
  .join('\n');
const compactCss = readFileSync(
  new URL(
    '../../../packages/desktop/src/renderer/pages/settings/components/settings-compact-desktop.css',
    import.meta.url
  ),
  'utf8'
);
const accountCss = [
  'AccountActivityChart.css',
  'AccountInsightsPanels.css',
  'AccountMetricsStrip.css',
  'AccountProfileHero.css',
  'AccountSecurityPanel.css',
  'AccountSettings.css',
]
  .map((file) =>
    readFileSync(
      new URL(`../../../packages/desktop/src/renderer/pages/settings/account/${file}`, import.meta.url),
      'utf8'
    )
  )
  .join('\n');
const governanceCss = readFileSync(
  new URL('../../../packages/desktop/src/renderer/pages/settings/components/settings-governance.css', import.meta.url),
  'utf8'
);
const networkCss = readFileSync(
  new URL('../../../packages/desktop/src/renderer/pages/settings/components/settings-network.css', import.meta.url),
  'utf8'
);
const lockedCss = [
  'settings-reference-rails.css',
  'settings-models.css',
  'settings-experts.css',
  'settings-compute.css',
  'settings-governance.css',
  'settings-network.css',
  'settings-general.css',
  'settings-storage.css',
  'settings-plans.css',
  'settings-tools.css',
  'settings-responsive.css',
]
  .map((file) =>
    readFileSync(
      new URL(`../../../packages/desktop/src/renderer/pages/settings/components/${file}`, import.meta.url),
      'utf8'
    )
  )
  .join('\n');

/** The four card components the merged library page renders, one per tab. */
const mergedLibraryCardSources = {
  experts: readFileSync(
    new URL(
      '../../../packages/desktop/src/renderer/pages/settings/SynonBiomedExpertsSettings/ExpertWorkbench.tsx',
      import.meta.url
    ),
    'utf8'
  ),
  skills: readFileSync(
    new URL('../../../packages/desktop/src/renderer/pages/settings/skills/SkillLibraryCard.tsx', import.meta.url),
    'utf8'
  ),
  connectors: readFileSync(
    new URL(
      '../../../packages/desktop/src/renderer/pages/settings/ToolsSettings/McpConnectorCard.tsx',
      import.meta.url
    ),
    'utf8'
  ),
  environments: readFileSync(
    new URL('../../../packages/desktop/src/renderer/pages/settings/environments/EnvironmentCard.tsx', import.meta.url),
    'utf8'
  ),
} as const;

const countOccurrences = (source: string, marker: string): number => source.split(marker).length - 1;

describe('settings image-based visual contract', () => {
  it('loads each visual module once and keeps connector foundations before tools geometry', () => {
    const imports = [...settingsManifestCss.matchAll(/@import\s+'([^']+)'\s*;/g)].map((match) => match[1]);

    expect(new Set(imports).size).toBe(imports.length);
    expect(imports).toContain('./settings-connectors.css');
    expect(imports).toContain('./settings-card-surfaces.css');
    expect(imports.indexOf('./settings-connectors.css')).toBeLessThan(imports.indexOf('./settings-card-surfaces.css'));
    expect(imports.indexOf('./settings-connectors.css')).toBeLessThan(imports.indexOf('./settings-tools.css'));
  });

  it('loads settings CSS at the route boundary and keeps mounted route geometry stable during navigation', () => {
    expect(settingsRouteSource).toContain("import './components/settings.css';");
    expect(wrapperSource).not.toContain("import './settings.css';");
    expect(wrapperSource).toMatch(/const \[contentRoute\][\s\S]*?readSettingsRoute/);
    expect(wrapperSource).toMatch(/settings-page-wrapper--transitioning/);
    expect(wrapperSource).toMatch(/data-settings-route=\{contentRoute\}/);
  });

  it('provides a real scroll authority for compact modules that exceed the desktop viewport', () => {
    expect(layoutCss).toMatch(
      /data-settings-route='credentials'[\s\S]*?data-settings-route='storage'[\s\S]*?data-settings-route='general'[\s\S]*?data-settings-route='account'[\s\S]*?data-settings-route='compute'[\s\S]*?data-settings-route='network'[\s\S]*?data-settings-route='plans-usage'[\s\S]*?overflow-y:\s*auto\s*!important/
    );
    expect(layoutCss).toMatch(
      /data-settings-route='account'[\s\S]*?settings-page-content[\s\S]*?height:\s*auto\s*!important[\s\S]*?overflow:\s*visible\s*!important/
    );
  });

  it('routes the complete settings surface system through the active visual theme', () => {
    expect(layoutCss).toMatch(/--settings-accent:\s*var\(--workspace-accent/);
    expect(layoutCss).toMatch(/--settings-accent-contrast:\s*var\(--workspace-accent-contrast/);
    expect(layoutCss).toMatch(/--settings-canvas:\s*var\(--workspace-canvas/);
    expect(layoutCss).toMatch(/--settings-surface:\s*var\(--workspace-overlay-surface/);
    expect(layoutCss).toMatch(/--settings-surface-subtle:\s*var\(--workspace-overlay-surface-muted/);
    expect(layoutCss).toMatch(/--settings-border:\s*var\(--workspace-border/);
    expect(layoutCss).toMatch(/--settings-text:\s*var\(--workspace-text/);
    expect(layoutCss).not.toMatch(/--workspace-accent:\s*var\(--settings-accent/);

    expect(cardSurfacesCss).toMatch(/data-settings-route='skills'[\s\S]*?settings-skill-card/);
    expect(cardSurfacesCss).toMatch(/data-settings-route='tools'[\s\S]*?synon-mcp-card/);
    expect(cardSurfacesCss).toMatch(/data-settings-route='experts'[\s\S]*?expert-card/);
    // The environment tile joins the same surface authority, so all four library
    // tabs share one hairline border, one radius and one soft two-layer shadow.
    expect(cardSurfacesCss).toMatch(/data-settings-route='environments'[\s\S]*?environment-card\s*\{[^}]*box-shadow/);
    expect(cardSurfacesCss).toMatch(/settings-summary-strip[\s\S]*?settings-model-profile[\s\S]*?settings-section/);
    expect(cardSurfacesCss).toMatch(/background:\s*var\(--settings-surface\)\s*!important/);
    expect(componentsCss).not.toMatch(/background:\s*#fff(?:fff)?(?:\s*!important)?\s*;/i);
    expect(componentsCss).toMatch(/arco-btn-primary[\s\S]*?color:\s*var\(--settings-accent-contrast\)\s*!important/);
    expect(componentsCss).toMatch(
      /\[role='switch'\]\s+\.arco-switch-dot[\s\S]*?background:\s*var\(--settings-surface\)\s*!important/
    );
    expect(governanceCss).not.toMatch(/background:\s*#fff(?:fff)?(?:\s*!important)?\s*;/i);
    expect(expertsCss).toMatch(
      /expert-card__default-badge[\s\S]*?background:\s*color-mix\(in srgb, var\(--settings-card-interaction-accent\) 7%, var\(--settings-surface\)\)/
    );
    expect(messageChannelsCss).toMatch(/message-channel-card__qr-frame[\s\S]*?background:\s*#fff/);
    expect(cardSurfacesCss).not.toMatch(/background:\s*linear-gradient/);
    expect(cardSurfacesCss).toMatch(/box-shadow:[\s\S]*?inset 0 1px 0[\s\S]*?0 8px 22px/);
    expect(cardSurfacesCss).toMatch(/@media \(hover:\s*hover\) and \(pointer:\s*fine\)/);
    expect(cardSurfacesCss).toMatch(/transform:\s*translate3d\(0, -1px, 0\)/);
    expect(cardSurfacesCss).not.toMatch(/translate3d\(0, -4px, 0\)/);
    expect(cardSurfacesCss).toMatch(/data-settings-route='environments'[\s\S]*?environment-card:hover/);
    expect(cardSurfacesCss).toMatch(/settings-skill-card:hover[\s\S]*?settings-skill-card__icon/);
    expect(cardSurfacesCss).toMatch(/synon-mcp-card:hover[\s\S]*?synon-mcp-card__icon/);
    expect(cardSurfacesCss).not.toContain('mcp-connector-visual--artwork');
    expect(cardSurfacesCss).toMatch(/expert-card:hover[\s\S]*?expert-card__artwork/);
    expect(cardSurfacesCss).toMatch(/settings-skill-card:focus-visible/);
    expect(cardSurfacesCss).toMatch(/synon-mcp-card:focus-within/);
    expect(cardSurfacesCss).toMatch(/@media \(prefers-reduced-motion:\s*reduce\)/);
    expect(skillsCss).not.toMatch(/settings-entity-card:hover/);
    expect(connectorsCss).not.toMatch(/synon-mcp-card:hover/);
  });

  it('uses a four-column expert card grid instead of the retired horizontal rows', () => {
    expect(SETTINGS_VISUAL_CONTRACTS.experts.grid).toEqual({
      desktopColumns: 4,
      mobileColumns: 1,
      cardMinHeight: 220,
    });
    expect(expertsCss).toMatch(/\.expert-grid[\s\S]*?repeat\(4,\s*minmax\(0,\s*1fr\)\)/);
    expect(expertsCss).toMatch(/\.expert-grid[\s\S]*?grid-auto-rows:\s*auto/);
    // The expert wall takes the shared 16px card gap, so its four columns are
    // exactly as wide as the other three tabs' columns.
    expect(expertsCss).toMatch(/\.expert-grid[\s\S]*?gap:\s*16px/);
    // The expert tile no longer pins its own height; the shared merged-library
    // card anatomy owns it so all four tabs stay exactly equal.
    expect(expertsCss).not.toMatch(/(?<![-\w])height:\s*216px/);
    expect(expertsCss).not.toMatch(/grid-template-columns:\s*643px/);
    expect(expertsCss).not.toMatch(/settings-list-row/);
  });

  it('gives all four merged library tabs one card anatomy and one card height', () => {
    // Same three parts on every card: heading (icon + title), a two-line
    // description, then a footer holding one metadata line and one 24px pill
    // control. Every metric is a token, so the tabs cannot drift apart again.
    // The merged library floor is 216px — 24px roomier than the 216px entity
    // token the standalone settings pages keep — so the two-line copy and the
    // status row always fit whole.
    expect(cardDensityCss).toMatch(/\.settings-library-card\s*\{[^}]*min-height:\s*216px\s*!important/);
    expect(cardDensityCss).toMatch(/\.settings-library-card\s*\{[^}]*padding:\s*20px\s*!important/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__heading\s*\{[^}]*min-height:\s*36px/);
    // The heading owns the title ramp, so the icon slot, the title slot and the
    // title itself all resolve to 15px/600/22px on every tab.
    expect(cardDensityCss).toMatch(/\.settings-library-card__heading\s*\{[^}]*font-size:\s*15px/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__heading\s*\{[^}]*font-weight:\s*600/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__heading\s*\{[^}]*line-height:\s*22px/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__icon\s*\{[^}]*width:\s*36px;[^}]*height:\s*36px;/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__title\s*\{[^}]*font-size:\s*15px\s*!important/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__title\s*\{[^}]*font-weight:\s*600\s*!important/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__description\s*\{[^}]*font-size:\s*13px\s*!important/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__description\s*\{[^}]*font-weight:\s*400\s*!important/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__description\s*\{[^}]*-webkit-line-clamp:\s*2/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__description\s*\{[^}]*min-height:\s*calc\(20px \* 2\)/);
    // The copy zone is pinned at exactly two lines, so no route can grow it
    // into a third line row and push the status row out of the card.
    expect(cardDensityCss).toMatch(/\.settings-library-card__description\s*\{[^}]*max-height:\s*calc\(20px \* 2\)/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__meta\s*\{[^}]*font-size:\s*12px\s*!important/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__meta\s*\{[^}]*white-space:\s*nowrap/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__meta\s*\{[^}]*min-width:\s*0/);
    expect(cardDensityCss).toMatch(
      /\.settings-library-card__meta > \*\s*\{[^}]*min-width:\s*0;[^}]*overflow:\s*hidden;[^}]*white-space:\s*nowrap;[^}]*text-overflow:\s*ellipsis/
    );
    expect(cardDensityCss).toMatch(/\.settings-library-card__footer\s*\{[^}]*min-height:\s*33px/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__footer\s*\{[^}]*flex-wrap:\s*nowrap/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__control\s*\{[^}]*height:\s*24px/);
    // The one control is pinned so the pill/capsule can never be squeezed by
    // the metadata line beside it.
    expect(cardDensityCss).toMatch(/\.settings-library-card__control\s*\{[^}]*flex:\s*0 0 auto/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__control\s*\{[^}]*border-radius:\s*999px/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__control\s*\{[^}]*font-size:\s*12px\s*!important/);
    // The footer owns the 12px metadata ramp, so the bottom row reads the same
    // on all four tabs even though each route names its own footer class.
    expect(cardDensityCss).toMatch(/\.settings-library-card__footer\s*\{[^}]*font-size:\s*12px\s*!important/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__footer\s*\{[^}]*line-height:\s*18px\s*!important/);
    // One rail and one wall inset for all four tabs: the retired per-route rail
    // and the experts-only 0.72 scale used to give that tab a different card.
    expect(cardDensityCss).toMatch(/\.settings-library-route[\s\S]*?transform:\s*none\s*!important/);
    expect(cardDensityCss).toMatch(/\.settings-library-route \.expert-grid\s*\{[^}]*padding:/);
    // Every card component renders the shared anatomy instead of its own.
    for (const [tab, source] of Object.entries(mergedLibraryCardSources)) {
      expect(source, tab).toContain('settings-library-card');
      expect(source, tab).toContain('settings-library-card__heading');
      expect(source, tab).toContain('settings-library-card__description');
      expect(source, tab).toContain('settings-library-card__footer');
      expect(source, tab).toContain('settings-library-card__control');
    }
    // Auto rows keep a taller card from stretching its row-mates.
    expect(countOccurrences(cardDensityCss, 'grid-auto-rows: auto')).toBeGreaterThanOrEqual(3);
  });

  it('uses three compact, consistently styled memory rows', () => {
    expect(governanceCss).toMatch(/\.memory-manager__layer-stack[\s\S]*?grid-template-columns:\s*minmax\(0,\s*1fr\)/);
    expect(governanceCss).toMatch(
      /\.memory-layer-card,[\s\S]*?\.memory-manager__workspace[\s\S]*?border-radius:\s*12px/
    );
    expect(governanceCss).toMatch(/\.settings-governance-page\s*\{[\s\S]*?width:\s*min\(100%,\s*1080px\)/);
    expect(governanceCss).toMatch(/\.settings-governance-page\s*\{[\s\S]*?margin-inline:\s*auto/);
    expect(governanceCss).toMatch(/\.memory-layer-card[\s\S]*?min-height:\s*140px/);
    expect(governanceCss).toMatch(/\.memory-layer-tab__label[\s\S]*?font-size:\s*18px/);
    expect(governanceCss).toMatch(/\.memory-layer-tab__description[\s\S]*?font-size:\s*14px/);
    expect(governanceCss).toMatch(/\.memory-manager__status-control[\s\S]*?border:\s*0/);
    expect(governanceCss).toMatch(/\.memory-manager__workspace[\s\S]*?height:\s*240px/);
    expect(governanceCss).not.toMatch(/\.memory-manager__status-control[\s\S]*?width:\s*511px/);
  });

  it('keeps network content inset and gives large card bodies their own vertical scroll', () => {
    expect(networkCss).toMatch(/synon-network-settings[\s\S]*?>\s*section[\s\S]*?padding:\s*20px 20px/);
    expect(networkCss).toMatch(/builtin-network-groups[\s\S]*?overflow-y:\s*auto/);
    expect(networkCss).toMatch(/allowed-domain-list[\s\S]*?overflow-y:\s*auto/);
    expect(networkCss).toMatch(/network-group-row[\s\S]*?min-height:\s*52px/);
    expect(compactCss).toMatch(/network-section--preset-groups[\s\S]*?overflow:\s*hidden\s*!important/);
  });

  it('covers every shipped settings module exactly once', () => {
    expect(SETTINGS_VISUAL_ROUTE_IDS).toHaveLength(13);
    expect(new Set(SETTINGS_VISUAL_ROUTE_IDS).size).toBe(13);
    expect(Object.keys(SETTINGS_VISUAL_CONTRACTS).toSorted()).toEqual([...SETTINGS_VISUAL_ROUTE_IDS].toSorted());
  });

  it('gives every module an explicit reference state and actionable acceptance selectors', () => {
    const desktopReferences = new Set<string>();
    const mobileReferences = new Set<string>();

    for (const route of SETTINGS_VISUAL_ROUTE_IDS) {
      const entry = SETTINGS_VISUAL_CONTRACTS[route];
      expect(entry.route).toBe(route);
      expect(entry.rootTestId).toBeTruthy();
      expect(entry.primarySelectors.length).toBeGreaterThan(0);
      expect(entry.interactionSelectors.length).toBeGreaterThan(0);
      expect(entry.dynamicStates.length).toBeGreaterThan(0);
      if (route === 'credentials' || route === 'experts' || route === 'skills' || route === 'environments') {
        expect(entry.reference.desktop).toBeNull();
      } else {
        expect(entry.reference.desktop).toBe(SETTINGS_LOCKED_DESKTOP_REFERENCE_SRC[route]);
        if (entry.reference.desktop) desktopReferences.add(entry.reference.desktop);
      }
      expect(entry.reference.mobile).toBeNull();
      if (entry.reference.mobile) mobileReferences.add(entry.reference.mobile);
    }

    expect(desktopReferences.size).toBe(9);
    expect(mobileReferences.size).toBe(0);
  });

  it('keeps the shared rail fluid at the reference viewport and matches the three-column locked sheets', () => {
    expect(SETTINGS_VISUAL_SYSTEM_ID).toBe('scientific-connectors-v3');
    expect(SETTINGS_DESKTOP_VIEWPORT).toEqual({ width: 1536, height: 1024 });
    expect(SETTINGS_MOBILE_VIEWPORT).toEqual({ width: 390, height: 844 });
    expect(layoutCss).toMatch(/--settings-content-max:\s*1680px/);
    expect(layoutCss).toMatch(/max-width:\s*min\(100%,\s*var\(--settings-content-max\)\)\s*!important/);
    expect(layoutCss).toMatch(/\.settings-page-content\s*\{[\s\S]*?margin-inline:\s*0;/);
    expect(layoutCss).not.toContain('--settings-content-width');
  });

  it('uses readable fluid skill cards while aligning the connector card contract', () => {
    expect(SETTINGS_VISUAL_CONTRACTS.skills.grid?.desktopColumns).toBe(4);
    expect(SETTINGS_VISUAL_CONTRACTS.skills.grid?.cardMinHeight).toBe(236);
    expect(SETTINGS_VISUAL_CONTRACTS.tools.grid?.desktopColumns).toBe(4);
    expect(SETTINGS_VISUAL_CONTRACTS.tools.grid?.cardMinHeight).toBe(236);
    expect(componentsCss).toMatch(/--settings-entity-card-height:\s*216px/);
    expect(componentsCss).toMatch(/--settings-card-title-size:\s*15px/);
    expect(componentsCss).toMatch(/--settings-entity-card-body-size:\s*13px/);
    // The skill tile takes its height and type ramp from the shared merged-library
    // anatomy rather than pinning a route-specific min-height of its own.
    expect(skillsCss).not.toMatch(/\.settings-skill-card\s*\{[^}]*min-height:\s*236px/);
    expect(cardDensityCss).toMatch(/\.settings-library-card\s*\{[^}]*min-height:\s*216px\s*!important/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__description\s*\{[^}]*font-size:\s*13px\s*!important/);
    expect(cardDensityCss).toMatch(/\.settings-library-card__description\s*\{[^}]*overflow-wrap:\s*anywhere/);
    expect(skillsCss).toMatch(/\.settings-skill-library-scroll[\s\S]*?overflow-y:\s*auto/);
    expect(skillsCss).toMatch(/\.settings-skill-library-footer[\s\S]*?flex-shrink:\s*0/);
    expect(skillsCss).not.toContain('settings-skill-card__artwork');
  });

  it('keeps Skills at native size and retains unrelated compact surfaces', () => {
    expect(compactCss).toMatch(/@media \(min-width:\s*1200px\) and \(max-height:\s*900px\)/);
    expect(compactCss).not.toContain("data-settings-route='skills'");
    // The skills wall takes the shared four-column count instead of restating a
    // fluid one of its own, so the four tabs cannot disagree about it.
    expect(skillsCss).not.toMatch(/\.settings-entity-grid\s*\{[^}]*grid-template-columns/);
    expect(cardDensityCss).toMatch(
      /\.settings-entity-grid,[\s\S]*?grid-template-columns:\s*repeat\(4,\s*minmax\(0,\s*1fr\)\)\s*!important/
    );
    expect(skillsCss).toContain('.settings-skill-library-toolbar');
    expect(skillsCss).toContain('grid-template-columns: minmax(0, 1fr) minmax(180px, 280px) auto');
    expect(skillsCss).toContain('@container (max-width: 600px)');
    expect(skillsCss).not.toContain('synon-skill-category-filter__options');
    expect(compactCss).not.toContain('settings-page-header__tabs-actions');
    expect(compactCss).not.toContain("[data-testid='synon-biomed-skills-filter']");
    expect(compactCss).toMatch(
      /@media \(min-width:\s*1200px\) and \(min-height:\s*768px\) and \(max-height:\s*800px\)[\s\S]*?data-settings-route='account'[\s\S]*?scale\(0\.72\)[\s\S]*?data-settings-route='experts'[\s\S]*?scale\(0\.72\)/
    );
    expect(compactCss).toMatch(
      /max-height:\s*800px[\s\S]*?data-settings-route='compute'[\s\S]*?scale\(0\.68\)[\s\S]*?data-settings-route='network'[\s\S]*?scale\(0\.72\)/
    );
    expect(compactCss).toMatch(
      /max-height:\s*800px[\s\S]*?data-settings-route='general'[\s\S]*?scale\(0\.72\)[\s\S]*?data-settings-route='plans-usage'[\s\S]*?scale\(0\.72\)/
    );
    expect(compactCss).toMatch(
      /data-settings-route='general'[\s\S]*?general-preferences-grid[\s\S]*?width:\s*100%\s*!important/
    );
    expect(compactCss).toMatch(
      /data-settings-route='governance'[\s\S]*?memory-manager[\s\S]*?width:\s*100%\s*!important/
    );
    expect(compactCss).not.toContain("data-settings-route='tools'");
    expect(toolsCss).toMatch(/\.mcp-library-scroll[\s\S]*?overflow-y:\s*auto/);
    expect(toolsCss).toContain('.mcp-library-footer');
    expect(toolsCss).not.toContain('visibility: hidden');
    expect(connectorsCss).toContain('repeat(auto-fill, minmax(min(100%, 260px), 1fr))');
    expect(toolsCss).toContain('grid-template-columns: repeat(auto-fill, minmax(260px, 1fr))');
    expect(toolsCss).toContain('grid-template-rows: repeat(3, minmax(min-content, 1fr))');
    expect(connectorsCss).toContain('.synon-mcp-card__footer');
    expect(compactCss).toMatch(/details:not\(\[open\]\)\s*>\s*div[\s\S]*?display:\s*none\s*!important/);
  });

  it('uses one compact route scale and removes unreadable account microcopy', () => {
    for (const route of ['models', 'governance', 'network', 'experts', 'account', 'plans-usage', 'credentials']) {
      expect(compactCss).toMatch(new RegExp(`data-settings-route='${route}'[\\s\\S]*?transform:\\s*scale\\(0\\.72\\)`));
    }
    expect(compactCss).not.toContain("data-settings-route='storage'");
    expect(accountCss).not.toMatch(/font-size:\s*(?:9|10)px/);
  });

  it('reflows Skills controls by available content width without squeezing or hiding actions', () => {
    expect(skillsCss).toMatch(/settings-skills-page[\s\S]*?container-type:\s*inline-size/);
    expect(skillsCss).toMatch(/@container \(max-width:\s*600px\)/);
    expect(skillsCss).toMatch(/settings-skill-library-search[\s\S]*?grid-column:\s*1 \/ -1/);
    expect(skillsCss).toMatch(/settings-skill-filter-panel[\s\S]*?grid-template-columns:\s*minmax\(0,\s*1fr\)/);
    expect(skillsCss).not.toContain('settings-page-header__tabs');
    expect(skillsCss).not.toMatch(/min-width:\s*(?:420|284|180)px/);
  });

  it('gives Account the sea-blue accent and a shared-width header in compact Chrome mode', () => {
    expect(compactCss).toMatch(/data-settings-route='account'[\s\S]*?--account-accent:\s*#3aaec8/);
    expect(compactCss).toMatch(/data-settings-route='account'[\s\S]*?--account-accent-strong:\s*#1684a5/);
    expect(compactCss).toMatch(
      /data-settings-route='account'[\s\S]*?--account-panel-subtle:\s*var\(--settings-surface-subtle, var\(--settings-surface\)\)\s*!important/
    );
    expect(compactCss).toMatch(
      /data-settings-route='account'[\s\S]*?account-settings\s*>\s*\.settings-page-header[\s\S]*?width:\s*100%\s*!important/
    );
    expect(compactCss).toMatch(/account-insight-panel[\s\S]*?height:\s*276px\s*!important/);
    expect(compactCss).toMatch(
      /data-settings-route='account'[\s\S]*?account-activity-chart__marker[\s\S]*?translate\(-50%,\s*-50%\)\s*scale\(1\.388889\)/
    );
  });

  it('removes the unified account-management module and keeps plans billing in normal document flow', () => {
    expect(accountSettingsSource).not.toContain('AccountManagementWorkbench');
    expect(accountCss).not.toContain('account-management-workbench');
    expect(lockedCss).toMatch(/plans-usage-plan-identity__icon[\s\S]*?width:\s*56px\s*!important/);
    expect(lockedCss).toMatch(
      /plans-usage-section--billing[\s\S]*?settings-section__header[\s\S]*?position:\s*static\s*!important/
    );
    expect(lockedCss).toMatch(
      /plans-usage-section--billing[\s\S]*?settings-section__body[\s\S]*?position:\s*static\s*!important[\s\S]*?pointer-events:\s*auto/
    );
    expect(componentsCss).toMatch(/plans-usage-section--rates\s*\{[\s\S]*?height:\s*auto/);
    expect(compactCss).toMatch(
      /data-settings-route='account'[\s\S]*?account-settings[\s\S]*?font-size:\s*13px\s*!important/
    );
    expect(compactCss).toMatch(
      /data-settings-route='plans-usage'[\s\S]*?settings-plans-page \.text-11px[\s\S]*?font-size:\s*13px\s*!important/
    );
  });

  it('keeps the original Models layout with one shared border authority and restrained typography', () => {
    expect(modelsCss).toMatch(/\.settings-summary-action\s*\{[^}]*cursor:\s*pointer/);
    expect(modelsCss).toMatch(/\.settings-summary-action__label\s*\{[^}]*font-size:\s*15px\s*!important/);
    expect(modelsCss).toMatch(/\.settings-summary-item strong\s*\{[^}]*font-size:\s*18px\s*!important/);
    expect(modelsCss).toMatch(/\.settings-model-profile__identity-name\s*\{[^}]*font-size:\s*15px\s*!important/);
    expect(modelsCss).toMatch(/\.settings-model-profile__key-value\s*\{[^}]*font-size:\s*13px\s*!important/);
    expect(modelsCss).toMatch(
      /\.settings-model-profile__actions \.arco-btn\s*\{[^}]*height:\s*32px\s*!important[^}]*font-size:\s*13px\s*!important/
    );
    expect(cardSurfacesCss).toMatch(
      /:is\([^)]*settings-model-profile[^)]*\)\s*\{[^}]*border-radius:\s*16px\s*!important/
    );
    expect(modelsCss).not.toMatch(/\.settings-model-profile\s*\{[^}]*border-radius:/);
    expect(routeLayoutsCss).not.toMatch(/data-settings-route='models'\] \.settings-list-row\s*\{[^}]*border-radius:/);
  });

  it('shares the entity typography tokens with MCP cards so their geometry stays in sync', () => {
    expect(connectorsCss).toMatch(/\.synon-mcp-card[\s\S]*?height:\s*auto/);
    expect(connectorsCss).toMatch(/.synon-mcp-card__title-row > span[\s\S]*?font-size:\s*15px/);
    // The connector tile takes the shared 20px padding and the shared 20px body
    // line instead of its own 18px padding, 21px line and 236px floor.
    expect(connectorsCss).toMatch(/\.synon-mcp-card\s*\{[^}]*padding:\s*20px\s*!important/);
    expect(connectorsCss).not.toMatch(/\.synon-mcp-card\s*\{[^}]*min-height:\s*236px/);
    expect(connectorsCss).toMatch(
      /\.synon-mcp-card__description[\s\S]*?line-height:\s*(?:var\(--settings-entity-card-body-line\)|20px)/
    );
    expect(connectorsCss).toMatch(
      /data-state='connected'[\s\S]*?background:\s*color-mix\(in srgb, var\(--success\) 14%,[\s\S]*?!important[\s\S]*?color:\s*var\(--success\)\s*!important/
    );
    expect(connectorsCss).toMatch(
      /data-state='attention'[\s\S]*?background:\s*color-mix\(in srgb, var\(--warning\) 14%,[\s\S]*?!important[\s\S]*?color:\s*var\(--warning\)\s*!important/
    );
  });

  it('keeps the locked page title and description in the visual flow', () => {
    expect(componentsCss).toMatch(/settings-page-header__title[\s\S]*?font-size:\s*26px\s*!important/);
    expect(componentsCss).toMatch(/settings-page-header__description[\s\S]*?font-size:\s*14px\s*!important/);
    expect(layoutCss).toMatch(
      /settings-sider__item-label[\s\S]*?font-size:\s*14px\s*!important[\s\S]*?line-height:\s*24px\s*!important/
    );
    expect(layoutCss).toMatch(/settings-sider__item\s*\{[\s\S]*?min-height:\s*40px/);
    expect(layoutCss).toMatch(/settings-sider__item-icon img[\s\S]*?width:\s*18px[\s\S]*?height:\s*18px/);
  });

  it('locks every supplied image-2 sheet by route and SHA-256', () => {
    const manifestPath = new URL(
      '../../../public/branding/settings-generated-v3/references/manifest.json',
      import.meta.url
    );
    const manifest = JSON.parse(readFileSync(manifestPath, 'utf8')) as {
      routes: Record<string, { file: string; sha256: string }>;
    };
    expect(Object.keys(SETTINGS_LOCKED_DESKTOP_REFERENCE_SRC).toSorted()).toEqual(
      Object.keys(manifest.routes).toSorted()
    );
    for (const [route, source] of Object.entries(SETTINGS_LOCKED_DESKTOP_REFERENCE_SRC)) {
      const file = new URL(`../../../public${source}`, import.meta.url);
      expect(existsSync(file)).toBe(true);
      const hash = createHash('sha256').update(readFileSync(file)).digest('hex');
      expect(hash).toBe(manifest.routes[route].sha256);
      expect(source).toBe(`/branding/settings-generated-v3/references/${manifest.routes[route].file}`);
    }
  });

  it('versions every generated asset so a reviewed crop cannot be masked by a cached 404', () => {
    expect(SETTINGS_GENERATED_ASSET_REVISION).toMatch(/^\d{8}[a-z]$/);
    const sources = [
      ...Object.values(SETTINGS_GENERATED_ICON_SRC),
      ...Object.values(SETTINGS_GENERATED_ARTWORK_SRC),
      ...Object.values(SETTINGS_GENERATED_EMPTY_SRC),
      ...Object.values(SETTINGS_GENERATED_NAV_SRC),
    ];
    expect(sources.length).toBeGreaterThan(0);
    for (const source of sources) {
      expect(source).toContain('/branding/settings-generated-v3/');
      expect(source).toContain(`?v=${SETTINGS_GENERATED_ASSET_REVISION}`);
    }
  });
});
