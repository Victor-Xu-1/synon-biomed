import type { SettingsRouteId } from '../settingsRouteLoaders';

/**
 * The image-generation set is the visual source of truth for the settings
 * surface.  This manifest keeps the acceptance criteria close to the shared
 * shell instead of scattering route-specific numbers through tests and CSS.
 *
 * Values describe presentation only.  They never imply that a design-sheet
 * label, sample value, or future control should be added to a live module.
 */
export const SETTINGS_VISUAL_SYSTEM_ID = 'scientific-connectors-v3';

export const SETTINGS_DESKTOP_VIEWPORT = { width: 1536, height: 1024 } as const;
export const SETTINGS_MOBILE_VIEWPORT = { width: 390, height: 844 } as const;

/**
 * Desktop sheets supplied by the user for the current visual acceptance
 * baseline. Credentials has no supplied sheet yet and therefore intentionally
 * remains on the shared visual grammar until one is approved.
 */
export const SETTINGS_LOCKED_DESKTOP_REFERENCE_SRC = {
  account: '/branding/settings-generated-v3/references/account.png',
  tools: '/branding/settings-generated-v3/references/tools.png',
  skills: '/branding/settings-generated-v3/references/skills.png',
  'plans-usage': '/branding/settings-generated-v3/references/plans-usage.png',
  experts: '/branding/settings-generated-v3/references/experts.png',
  models: '/branding/settings-generated-v3/references/models.png',
  governance: '/branding/settings-generated-v3/references/governance.png',
  compute: '/branding/settings-generated-v3/references/compute.png',
  network: '/branding/settings-generated-v3/references/network.png',
  general: '/branding/settings-generated-v3/references/general.png',
  storage: '/branding/settings-generated-v3/references/storage.png',
} as const satisfies Partial<Record<SettingsRouteId, string>>;

type GridContract = {
  desktopColumns?: number;
  mobileColumns: number;
  cardMinHeight?: number;
};

export type SettingsVisualContract = {
  route: SettingsRouteId;
  reference: {
    /** Locked image-2 desktop sheet, or null when no sheet was supplied. */
    desktop: string | null;
    /** Mobile sheet is intentionally null until a matching image-2 review is supplied. */
    mobile: string | null;
  };
  rootTestId: string;
  primarySelectors: readonly string[];
  grid?: GridContract;
  sectionCount?: number;
  interactionSelectors: readonly string[];
  dynamicStates: readonly string[];
};

const contract = <T extends SettingsVisualContract>(value: T): T => value;

export const SETTINGS_VISUAL_CONTRACTS = {
  account: contract({
    route: 'account',
    reference: {
      desktop: SETTINGS_LOCKED_DESKTOP_REFERENCE_SRC.account,
      mobile: null,
    },
    rootTestId: 'synon-account-settings',
    primarySelectors: ['.account-profile-hero', '.account-metrics', '.account-activity-chart', '.account-insight-grid'],
    grid: { desktopColumns: 5, mobileColumns: 1 },
    interactionSelectors: ['.account-profile-action', '.account-activity-tabs [role="tab"]'],
    dynamicStates: ['loading', 'partial', 'error', 'empty-chart'],
  }),
  'plans-usage': contract({
    route: 'plans-usage',
    reference: {
      desktop: SETTINGS_LOCKED_DESKTOP_REFERENCE_SRC['plans-usage'],
      mobile: null,
    },
    rootTestId: 'synon-plans-usage-settings',
    primarySelectors: ['.settings-section'],
    sectionCount: 3,
    interactionSelectors: ['[role="progressbar"]'],
    dynamicStates: ['zero-usage', 'empty-bills', 'empty-history'],
  }),
  // The former horizontal-row sheet is retained only as historical asset
  // provenance; the active contract follows the shared catalog card system.
  experts: contract({
    route: 'experts',
    reference: {
      desktop: null,
      mobile: null,
    },
    rootTestId: 'expert-list-page',
    primarySelectors: ['.expert-grid', '.expert-card', '.expert-card__artwork'],
    grid: { desktopColumns: 4, mobileColumns: 1, cardMinHeight: 220 },
    interactionSelectors: ['.expert-card__main', '.expert-card [role="switch"]'],
    dynamicStates: ['loading', 'empty', 'search-empty'],
  }),
  skills: contract({
    route: 'skills',
    reference: {
      desktop: null,
      mobile: null,
    },
    rootTestId: 'synon-biomed-skills-section',
    primarySelectors: [
      '.settings-page-header__row',
      '.settings-skill-library-toolbar',
      '.settings-entity-grid',
      '.settings-entity-card',
      '.settings-skill-library-footer',
    ],
    grid: { desktopColumns: 4, mobileColumns: 1, cardMinHeight: 236 },
    interactionSelectors: [
      '.settings-skill-library-toolbar select',
      '[data-testid="input-search-synon-biomed-skills"]',
      '[data-testid="add-skill-button"]',
      '.settings-entity-card [role="switch"]',
    ],
    dynamicStates: ['loading', 'load-error', 'empty', 'search-empty'],
  }),
  tools: contract({
    route: 'tools',
    reference: {
      desktop: SETTINGS_LOCKED_DESKTOP_REFERENCE_SRC.tools,
      mobile: null,
    },
    rootTestId: 'synon-biomed-mcp-settings',
    primarySelectors: ['.synon-mcp-grid', '.synon-mcp-card', '.mcp-library-toolbar', '.mcp-library-footer'],
    grid: { desktopColumns: 4, mobileColumns: 1, cardMinHeight: 236 },
    interactionSelectors: ['.synon-mcp-card__configure', '.synon-mcp-card__more', '.synon-mcp-card__status-control'],
    dynamicStates: ['catalog-loading', 'catalog-error', 'connected', 'attention', 'marketplace'],
  }),
  models: contract({
    route: 'models',
    reference: {
      desktop: SETTINGS_LOCKED_DESKTOP_REFERENCE_SRC.models,
      mobile: null,
    },
    rootTestId: 'models-header',
    primarySelectors: ['.settings-list', '.settings-list-row'],
    interactionSelectors: ['.settings-summary-action', '.settings-list-row button', '.settings-action-button'],
    dynamicStates: ['loading', 'empty', 'test-success', 'test-error'],
  }),
  compute: contract({
    route: 'compute',
    reference: {
      desktop: SETTINGS_LOCKED_DESKTOP_REFERENCE_SRC.compute,
      mobile: null,
    },
    rootTestId: 'synon-biomed-compute-section',
    primarySelectors: ['.compute-section', '.compute-provider-card', '.compute-endpoint-row'],
    sectionCount: 6,
    interactionSelectors: ['.compute-section button', '.compute-section [role="switch"]'],
    dynamicStates: ['loading', 'empty', 'probe-error', 'gpu-unavailable', 'jobs-error'],
  }),
  governance: contract({
    route: 'governance',
    reference: {
      desktop: SETTINGS_LOCKED_DESKTOP_REFERENCE_SRC.governance,
      mobile: null,
    },
    rootTestId: 'memory-header',
    primarySelectors: ['.memory-manager', '.memory-manager__layer-tabs', '.memory-manager__editor'],
    interactionSelectors: ['.memory-layer-tab', '.memory-manager button', '.memory-manager [role="switch"]'],
    dynamicStates: ['loading', 'empty', 'read-only', 'save-error'],
  }),
  network: contract({
    route: 'network',
    reference: {
      desktop: SETTINGS_LOCKED_DESKTOP_REFERENCE_SRC.network,
      mobile: null,
    },
    rootTestId: 'synon-network-settings',
    primarySelectors: [
      '[data-testid="synon-network-settings"] > section',
      '[data-testid="synon-network-settings"] > details',
    ],
    interactionSelectors: [
      '[data-testid="synon-network-settings"] button',
      '[data-testid="synon-network-settings"] [role="switch"]',
    ],
    dynamicStates: ['loading', 'empty', 'invalid-domain', 'save-error'],
  }),
  credentials: contract({
    route: 'credentials',
    reference: {
      desktop: null,
      mobile: null,
    },
    rootTestId: 'synon-credentials-settings',
    primarySelectors: ['.settings-section', '[data-testid^="credential-provider-"]'],
    interactionSelectors: ['[data-testid^="credential-provider-"] button', '.settings-action-button'],
    dynamicStates: ['loading', 'empty', 'configured', 'redacted', 'save-error'],
  }),
  storage: contract({
    route: 'storage',
    reference: {
      desktop: SETTINGS_LOCKED_DESKTOP_REFERENCE_SRC.storage,
      mobile: null,
    },
    rootTestId: 'synon-storage-settings',
    primarySelectors: ['.settings-section', '.settings-summary-strip'],
    sectionCount: 4,
    interactionSelectors: ['.settings-section button', '[role="progressbar"]'],
    dynamicStates: ['directory-loading', 'usage-loading', 'empty-cloud', 'move-pending', 'save-error'],
  }),
  general: contract({
    route: 'general',
    reference: {
      desktop: SETTINGS_LOCKED_DESKTOP_REFERENCE_SRC.general,
      mobile: null,
    },
    rootTestId: 'synon-general-settings',
    primarySelectors: ['.settings-section', '.message-channels-grid'],
    sectionCount: 5,
    interactionSelectors: ['.settings-section button', '.message-channel-card button', 'select'],
    dynamicStates: ['contact-loading', 'contact-empty', 'email-invalid', 'save-error'],
  }),
} as const satisfies Record<SettingsRouteId, SettingsVisualContract>;

export const SETTINGS_VISUAL_ROUTE_IDS = Object.keys(SETTINGS_VISUAL_CONTRACTS) as SettingsRouteId[];

export function getSettingsVisualContract(route: SettingsRouteId): SettingsVisualContract {
  return SETTINGS_VISUAL_CONTRACTS[route];
}
