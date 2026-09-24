import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const rendererRoot = new URL('../../packages/desktop/src/renderer/', import.meta.url);
const warmCss = readFileSync(new URL('pages/settings/AppearanceSettings/presets/warm.css', rendererRoot), 'utf8');
const coolCss = readFileSync(new URL('pages/settings/AppearanceSettings/presets/cool.css', rendererRoot), 'utf8');
const whiteCss = readFileSync(new URL('pages/settings/AppearanceSettings/presets/white.css', rendererRoot), 'utf8');
const rendererHtml = readFileSync(new URL('index.html', rendererRoot), 'utf8');
const rendererMain = readFileSync(new URL('main.tsx', rendererRoot), 'utf8');
const shellCss = readFileSync(new URL('styles/workspace-theme.css', rendererRoot), 'utf8');
const settingsCoreCss = readFileSync(new URL('pages/settings/components/settings-core.css', rendererRoot), 'utf8');
const settingsCardSurfacesCss = readFileSync(
  new URL('pages/settings/components/settings-card-surfaces.css', rendererRoot),
  'utf8'
);
const messageChannelsCss = readFileSync(new URL('pages/settings/MessageChannelsSettings.css', rendererRoot), 'utf8');
const unoConfig = readFileSync(new URL('../../../../uno.config.ts', rendererRoot), 'utf8');
const planReviewSource = readFileSync(
  new URL('components/synonBiomed/runtime/SynonBiomedPlanReviewContent.tsx', rendererRoot),
  'utf8'
);
const planReviewCss = readFileSync(
  new URL('components/synonBiomed/runtime/SynonBiomedPlanReviewContent.css', rendererRoot),
  'utf8'
);
const askUserHistorySource = readFileSync(
  new URL('components/synonBiomed/runtime/AskUserHistoryCard.tsx', rendererRoot),
  'utf8'
);
const askUserHistoryCss = readFileSync(
  new URL('components/synonBiomed/runtime/AskUserHistoryCard.css', rendererRoot),
  'utf8'
);
const themeInitScript = readFileSync(new URL('../../public/theme-init.js', import.meta.url), 'utf8');
const textAddActionSources = [
  'pages/settings/SynonBiomedModelsSettings.tsx',
  'pages/settings/NetworkSettings.tsx',
  'pages/settings/CredentialsSettings.tsx',
  'pages/settings/ComputeSettings.tsx',
  'pages/settings/SynonBiomedSkillsSettings.tsx',
  'pages/settings/ToolsSettings/SynonBiomedMcpSettings.tsx',
  'pages/settings/SynonBiomedExpertsSettings/ExpertWorkbench.tsx',
  'pages/settings/SynonBiomedExpertsSettings/home/AssistantHomeTabs.tsx',
  'pages/settings/components/ApiKeyEditorModal.tsx',
].map((path) => readFileSync(new URL(path, rendererRoot), 'utf8'));
const memoryManagerSource = readFileSync(
  new URL('pages/settings/components/SynonBiomedMemoryManager.tsx', rendererRoot),
  'utf8'
);

const requiredPaletteVariables = [
  'workspace-canvas',
  'workspace-sidebar',
  'workspace-subtle',
  'workspace-hover',
  'workspace-selected',
  'workspace-border',
  'workspace-border-strong',
  'workspace-text',
  'workspace-text-secondary',
  'workspace-text-tertiary',
  'workspace-accent',
  'workspace-accent-contrast',
] as const;

const parseVariables = (block: string): Record<string, string> =>
  Object.fromEntries(
    [...block.matchAll(/--([a-zA-Z0-9-_]+)\s*:\s*([^;]+);/g)].map((match) => [match[1]!, match[2]!.trim()])
  );

const readBlock = (css: string, selector: string): Record<string, string> => {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const body = css.match(new RegExp(`${escaped}\\s*\\{([\\s\\S]*?)\\}`))?.[1] ?? '';
  return parseVariables(body);
};

const hexChannels = (hex: string): [number, number, number] => {
  const value = hex.slice(1);
  return [0, 2, 4].map((offset) => Number.parseInt(value.slice(offset, offset + 2), 16)) as [number, number, number];
};

const luminance = (hex: string): number => {
  const channels = hexChannels(hex).map((value) => {
    const normalized = value / 255;
    return normalized <= 0.03928 ? normalized / 12.92 : ((normalized + 0.055) / 1.055) ** 2.4;
  });
  return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722;
};

const contrast = (foreground: string, background: string): number => {
  const values = [luminance(foreground), luminance(background)].toSorted((left, right) => right - left);
  return (values[0]! + 0.05) / (values[1]! + 0.05);
};

describe.each([
  ['Warm', warmCss],
  ['Cool', coolCss],
  ['Pure white', whiteCss],
] as const)('%s visual contract', (_name, css) => {
  const palettes = [readBlock(css, "[data-color-scheme='default']"), readBlock(css, "[data-theme='dark']")];

  it('defines complete light and dark warm/cool palettes', () => {
    for (const palette of palettes) {
      for (const variable of requiredPaletteVariables) expect(palette[variable]).toMatch(/^#[0-9a-f]{6}$/i);
      expect(palette['workspace-overlay-border']).toBe('transparent');
      expect(palette['composer-border']).toBe('transparent');
      expect(palette['composer-border-active']).toBe('transparent');
      expect(palette['conversation-user-border']).toBe('transparent');
      expect(palette['conversation-stream-border']).toBe('transparent');
    }

    for (const match of css.matchAll(/#[0-9a-f]{6}\b/gi)) {
      const channels = hexChannels(match[0]);
      expect(Math.max(...channels) - Math.min(...channels)).toBeLessThanOrEqual(60);
    }
  });

  it('keeps tertiary text readable in both modes', () => {
    for (const palette of palettes) {
      expect(contrast(palette['workspace-text-tertiary']!, palette['workspace-canvas']!)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(palette['workspace-text-tertiary']!, palette['workspace-sidebar']!)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(palette['workspace-text-tertiary']!, palette['workspace-subtle']!)).toBeGreaterThanOrEqual(4.5);
    }
  });

  it('rounds streamed images and uses a subtle neutral blockquote edge', () => {
    expect(css).toMatch(/\.markdown-shadow-body img\s*\{[\s\S]*?border-radius:\s*(?:12px|var\(--ui-radius-xl\))/);
    expect(css).toMatch(/\.markdown-shadow-body blockquote\s*\{[\s\S]*?border-left:\s*1px solid/);
    expect(css).toMatch(/\.markdown-shadow-body pre\s*\{[\s\S]*?overflow:\s*auto[\s\S]*?border-color:\s*transparent/);
    expect(css).toMatch(/\.markdown-shadow-body table\s*\{[\s\S]*?border-radius:\s*(?:10px|var\(--ui-radius-lg\))/);
    expect(css).toMatch(/\.markdown-shadow-body code\s*\{[\s\S]*?border-radius:\s*6px/);
    expect(css).toMatch(/\.markdown-shadow-body hr\s*\{[\s\S]*?border-top:\s*1px solid/);
  });
});

describe('shared visual shell contract', () => {
  it('resets native border widths before directional border utilities are applied', () => {
    expect(unoConfig).toMatch(/\*,\s*::before,\s*::after\s*\{[\s\S]*?box-sizing:\s*border-box;/i);
    expect(unoConfig).toMatch(/\*,\s*::before,\s*::after\s*\{[\s\S]*?border-width:\s*0;/i);
    expect(unoConfig).toMatch(/\*,\s*::before,\s*::after\s*\{[\s\S]*?border-style:\s*solid;/i);
  });

  it('boots into the warm palette without development-server artifacts', () => {
    expect(rendererHtml).toMatch(/<html[^>]*data-theme-family=["']warm["']/i);
    expect(rendererHtml).toMatch(/name=["']theme-color["'][^>]*content=["']#f2f1ee["']/i);
    expect(rendererHtml).toMatch(/html\[data-theme=["']dark["']\] \.app-boot-loading/);
    expect(rendererHtml).toMatch(/\.app-boot-loading__mark[\s\S]*?border:\s*2px solid/);
    expect(rendererHtml).not.toMatch(/(?:\/@vite\/client|react-refresh|main\.tsx\?t=)/i);
  });

  it('keeps the component-library primary seed neutral', () => {
    const primaryHex = rendererMain.match(/primaryColor:\s*['"](#[0-9a-f]{6})['"]/i)?.[1];
    expect(primaryHex).toBeDefined();
    const channels = hexChannels(primaryHex!);
    expect(Math.max(...channels) - Math.min(...channels)).toBeLessThanOrEqual(20);
  });

  it('restores both appearance and visual family before React renders', () => {
    expect(themeInitScript).toContain('__synon-ai_theme');
    expect(themeInitScript).toContain('__synon-ai_theme_family');
    expect(themeInitScript).toMatch(/family === 'claude' \? 'warm' : family === 'codex' \? 'cool'/);
    expect(themeInitScript).toMatch(/setAttribute\('data-theme-family', normalizedFamily\)/);
  });

  it('does not reintroduce thick visible borders or focus outlines', () => {
    expect(shellCss).not.toMatch(/border(?:-left|-right|-top|-bottom)?\s*:\s*[3-9]px/i);
    expect(shellCss).not.toMatch(/outline\s*:\s*[3-9]px/i);
  });

  it('keeps the sidebar account footer borderless', () => {
    const footerBlock = shellCss.match(/html body \.sider-footer\s*\{([\s\S]*?)\}/)?.[1] ?? '';
    expect(footerBlock).not.toMatch(/\bborder(?:-top|-right|-bottom|-left)?\s*:/i);
  });

  it('keeps task plans on the lightweight surface contract without browser-default borders', () => {
    expect(planReviewSource).not.toMatch(/\bborder-solid\b/);
    expect(planReviewSource).not.toMatch(/\bborder-l-2\b/);
    expect(planReviewCss).not.toMatch(/border(?:-top|-right|-bottom|-left)?\s*:\s*[2-9]px/i);
    expect(planReviewCss).toMatch(
      /\.synon-biomed-plan-review__phases\s*\{[\s\S]*?border:\s*0;[\s\S]*?border-top:\s*1px solid/i
    );
    expect(planReviewCss).toMatch(
      /\.synon-biomed-plan-review__phase\s*\{[\s\S]*?border:\s*0;[\s\S]*?border-bottom:\s*1px solid/i
    );
    expect(planReviewCss).toMatch(/\.synon-biomed-plan-review__delegation\s*\{[\s\S]*?border:\s*0;/i);
  });

  it('keeps historical answers on the shared quiet conversation surface', () => {
    expect(askUserHistorySource).toContain("import './AskUserHistoryCard.css'");
    expect(askUserHistorySource).not.toMatch(/\bborder-solid\b/);
    expect(askUserHistorySource).not.toMatch(/\bbg-fill-1\b/);
    expect(askUserHistoryCss).toMatch(
      /\.synon-biomed-ask-user-history\s*\{[\s\S]*?border:\s*0;[\s\S]*?border-radius:\s*(?:12px|var\(--ui-radius-xl\));[\s\S]*?box-shadow:\s*none;/i
    );
    expect(askUserHistoryCss).toMatch(/--ask-user-history-surface:\s*rgb\(18 18 18 \/ 3%\)/i);
    expect(askUserHistoryCss).toMatch(
      /\.synon-biomed-ask-user-history__edit\.arco-btn\s*\{[\s\S]*?border:\s*0;[\s\S]*?background:\s*transparent;[\s\S]*?opacity:\s*0;/i
    );
    expect(askUserHistoryCss).toMatch(
      /\.synon-biomed-ask-user-history:hover \.synon-biomed-ask-user-history__edit\.arco-btn,[\s\S]*?focus-visible\s*\{[\s\S]*?opacity:\s*1;/i
    );
  });

  it('keeps text-field focus to one neutral border without an outer ring', () => {
    expect(shellCss).toMatch(
      /\.arco-input:focus,[\s\S]*?\.arco-picker-focused\s*\{[\s\S]*?outline:\s*none\s*!important;[\s\S]*?box-shadow:\s*none\s*!important;/i
    );
    expect(shellCss).toMatch(
      /Text fields use their own one-pixel border[\s\S]*?input:not\(\[type='checkbox'\]\)[\s\S]*?\):focus,[\s\S]*?outline:\s*none\s*!important;[\s\S]*?box-shadow:\s*none\s*!important;/i
    );
    expect(shellCss).not.toMatch(/\[role='dialog'\]\s*:is\(button,\s*input,\s*textarea,\s*select\):focus-visible/i);
  });

  it('uses one compact typography hierarchy across settings, chat, and overlays', () => {
    expect(shellCss).not.toContain('.settings-page-header__title');
    expect(settingsCoreCss).toMatch(
      /\.settings-page-header__title\s*\{[^}]*font-size:\s*(?:28px|var\(--ui-font-headline\))[^}]*line-height:\s*36px/i
    );
    expect(shellCss).toMatch(
      /\.message-item\s*\{[^}]*font-size:\s*(?:15px|var\(--ui-font-title\))[^}]*line-height:\s*1\.65/i
    );
    expect(shellCss).toMatch(
      /\.arco-modal-content,[\s\S]*?\.arco-drawer-content\s*\{[^}]*font-size:\s*(?:14px|var\(--ui-font-subtitle\))/i
    );
    expect(shellCss).toMatch(
      /\.arco-dropdown-menu-item,[\s\S]*?\.arco-select-option\s*\{[^}]*font-size:\s*(?:13px|var\(--ui-font-body\))/i
    );
    expect(shellCss).not.toContain('.settings-page-content .text-11px');
    expect(shellCss).not.toContain('.settings-page-content .text-10px');
    expect(settingsCoreCss).toMatch(
      /\.settings-section__body\s*\{[^}]*font-size:\s*(?:13px|var\(--ui-font-body\))[^}]*line-height:\s*20px/i
    );
    expect(shellCss).toMatch(
      /:is\([\s\S]*?\.arco-modal,[\s\S]*?\.arco-drawer,[\s\S]*?\.accessible-content-dialog__surface,[\s\S]*?\.accessible-action-dialog__dialog[\s\S]*?\)\s*\.text-10px\s*\{[^}]*font-size:\s*(?:11px|var\(--ui-font-micro\))/i
    );
    expect(shellCss).toMatch(
      /\.artifact-library__card \.text-10px\s*\{[^}]*font-size:\s*(?:11px|var\(--ui-font-micro\))/i
    );
    expect(shellCss).toMatch(/\.message-scientific-files__metadata\s*\{[^}]*background:\s*var\(--workspace-canvas\)/i);
    expect(messageChannelsCss).toMatch(
      /\.message-channel-card__status\s*\{[^}]*color:\s*var\(--workspace-text-tertiary,[^}]*font-size:\s*(?:11px|var\(--ui-font-micro\))[^}]*line-height:\s*16px/i
    );
    expect(shellCss).toMatch(/\.arco-tag \.arco-tag-content\s*\{[^}]*color:\s*var\(--workspace-text-secondary\)/i);
    expect(shellCss).toMatch(
      /\.arco-tag-gray,[\s\S]*?\.arco-tag-gray \.arco-tag-content\s*\{[^}]*color:\s*var\(--workspace-text-secondary\)/i
    );
    expect(shellCss).toMatch(/\.text-green-6\s*\{[^}]*color:\s*var\(--workspace-success-text\)/i);
    expect(shellCss).toMatch(
      /\.arco-btn-status-danger,\s*html body \.settings-text-danger-button\s*\{[^}]*color:\s*var\(--workspace-danger-text\)/i
    );
    expect(shellCss).toMatch(
      /\.arco-dropdown-menu-item-danger,[\s\S]*?\[role='menuitem'\]\[data-danger='true'\]\s*\{[^}]*color:\s*var\(--workspace-danger-text\)/i
    );
    expect(shellCss).not.toContain('.tool-step--error');
    expect(shellCss).not.toContain('conversation-scroll-control__count');
    expect(shellCss).toMatch(
      /\.synon-sidebar-section-title-button \.sider-section-title,[\s\S]*?\.synon-sidebar-timeline \.text-t-secondary,[\s\S]*?\.synon-sidebar-project-meta\s*\{[^}]*color:\s*var\(--workspace-text-secondary\)/i
    );
  });

  it('keeps every single-select value and arrow on one centered row', () => {
    expect(shellCss).toMatch(
      /\.arco-select-single \.arco-select-view\s*\{[^}]*align-items:\s*center[^}]*flex-wrap:\s*nowrap/i
    );
    expect(shellCss).toMatch(
      /\.arco-select-single \.arco-select-view-selector\s*\{[^}]*align-items:\s*center[^}]*min-width:\s*0/i
    );
    expect(shellCss).toMatch(
      /\.arco-select-single \.arco-select-view-value\s*\{[^}]*min-width:\s*0[^}]*white-space:\s*nowrap/i
    );
    expect(shellCss).toMatch(
      /\.arco-select-single \.arco-select-suffix\s*\{[^}]*align-self:\s*stretch[^}]*flex:\s*0 0 auto[^}]*justify-content:\s*center/i
    );
  });

  it('coordinates settings navigation type with the conversation scale', () => {
    expect(shellCss).toMatch(
      /\.settings-sider__item-label\s*\{[^}]*font-size:\s*(?:15px|var\(--ui-font-title\))[^}]*line-height:\s*24px[^}]*font-weight:\s*(?:500|var\(--ui-weight-medium\))/i
    );
    expect(shellCss).toMatch(
      /\.settings-sider__group-header\s*\{[^}]*font-size:\s*(?:12px|var\(--ui-font-meta\))[^}]*line-height:\s*18px[^}]*font-weight:\s*(?:600|var\(--ui-weight-semibold\))/i
    );
    expect(shellCss).toMatch(
      /\.settings-sider__item\[aria-current='page'\] \.settings-sider__item-label\s*\{[^}]*font-weight:\s*(?:600|var\(--ui-weight-semibold\))/i
    );
  });

  it('normalizes every switch to one quiet pill geometry', () => {
    expect(shellCss).toMatch(
      /\.arco-switch\s*\{[^}]*width:\s*40px[^}]*min-width:\s*40px[^}]*height:\s*24px[^}]*border:\s*0[^}]*border-radius:\s*(?:999px|var\(--ui-radius-pill\))/i
    );
    expect(shellCss).toMatch(
      /\.arco-switch \.arco-switch-dot\s*\{[^}]*top:\s*4px[^}]*left:\s*4px[^}]*width:\s*16px[^}]*height:\s*16px[^}]*border-radius:\s*(?:50%|var\(--ui-radius-circle\))/i
    );
    expect(shellCss).toMatch(/\.arco-switch-checked \.arco-switch-dot\s*\{[^}]*left:\s*20px/i);
  });

  it('keeps selected session options flat while preserving the switch state', () => {
    expect(shellCss).toMatch(
      /\.session-options-menu__row\[aria-checked='true'\]\s*\{[^}]*background:\s*transparent\s*!important[^}]*box-shadow:\s*none\s*!important/i
    );
  });

  it('does not repeat additive wording with a leading plus icon', () => {
    for (const source of textAddActionSources) expect(source).not.toMatch(/<(?:Plus|Add)\b/);

    expect([...memoryManagerSource.matchAll(/<Plus\b/g)]).toHaveLength(1);
    expect(memoryManagerSource).toMatch(/data-testid='memory-category-add'[\s\S]{0,240}icon=\{<Plus\b/);
    expect(memoryManagerSource).not.toMatch(/data-testid='memory-add'[\s\S]{0,240}icon=\{<Plus\b/);
  });

  it('keeps semantic status text readable on every family surface', () => {
    const semanticPalettes = [
      readBlock(shellCss, "[data-color-scheme='default']"),
      readBlock(shellCss, "[data-theme='dark']"),
    ];

    for (const familyCss of [warmCss, coolCss]) {
      const familyPalettes = [
        readBlock(familyCss, "[data-color-scheme='default']"),
        readBlock(familyCss, "[data-theme='dark']"),
      ];

      familyPalettes.forEach((palette, index) => {
        const semantics = semanticPalettes[index]!;
        for (const surface of ['workspace-canvas', 'workspace-sidebar', 'workspace-subtle'] as const) {
          expect(contrast(semantics['workspace-success-text']!, palette[surface]!)).toBeGreaterThanOrEqual(4.5);
          expect(contrast(semantics['workspace-danger-text']!, palette[surface]!)).toBeGreaterThanOrEqual(4.5);
        }
      });
    }
  });

  it('keeps destructive text monochrome in both appearance modes', () => {
    const semanticPalettes = [
      readBlock(shellCss, "[data-color-scheme='default']"),
      readBlock(shellCss, "[data-theme='dark']"),
    ];

    for (const palette of semanticPalettes) {
      expect(palette['workspace-danger-text']).toBe(palette['workspace-text']);
    }

    expect(shellCss).toMatch(
      /\[class~='text-danger-6'[\s\S]*?\.message-channel-card__danger-button[\s\S]*?color:\s*var\(--workspace-text\)/i
    );
    expect(shellCss).toMatch(
      /\.arco-btn-status-danger\s*\{[^}]*background:\s*var\(--workspace-canvas\)[^}]*border-color:\s*var\(--workspace-border\)/i
    );
    expect(shellCss).toMatch(
      /\[class~='text-warning-6'[\s\S]*?\[class~='text-orange-7'[\s\S]*?color:\s*var\(--workspace-text\)/i
    );
    expect(shellCss).toMatch(
      /\[class\*='border-danger-'\][\s\S]*?\[class\*='border-orange-'\][\s\S]*?border-color:\s*var\(--workspace-border\)/i
    );
  });

  it('maps legacy tertiary text utilities onto the readable family palette', () => {
    expect(shellCss).toMatch(
      /\.text-t-tertiary,[\s\S]*?\.placeholder\\:text-t-tertiary::placeholder\s*\{[^}]*color:\s*var\(--workspace-text-tertiary\)/i
    );
  });

  it('keeps every explicit frame neutral', () => {
    const frameValues = [
      ...shellCss.matchAll(
        /^\s*(?:border(?:-(?:top|right|bottom|left))?(?:-color)?|outline(?:-color)?)\s*:\s*([^;]+);/gim
      ),
    ].map((match) => match[1]!);

    expect(frameValues).not.toHaveLength(0);
    for (const value of frameValues) {
      expect(value).not.toMatch(/(?:primary|brand|accent|blue|orange|red|success|warning|danger)/i);
      for (const match of value.matchAll(/#[0-9a-f]{6}\b/gi)) {
        const channels = hexChannels(match[0]);
        expect(Math.max(...channels) - Math.min(...channels)).toBeLessThanOrEqual(20);
      }
    }
  });

  it('removes provider-colored frames from step indicators', () => {
    expect(shellCss).toMatch(
      /\.synon-ai-steps \.arco-steps-item-process \.arco-steps-item-icon,[\s\S]*?border-color:\s*transparent !important/i
    );
    expect(shellCss).toMatch(
      /\.synon-ai-steps \.arco-steps-item-finish \.arco-steps-item-icon\s*\{[^}]*background:\s*var\(--workspace-subtle\)/i
    );
  });

  it('keeps modules and overlays rounded with neutral boundaries', () => {
    expect(shellCss).toMatch(
      /:is\([\s\S]*?\.arco-card,[\s\S]*?\.arco-modal,[\s\S]*?\.arco-drawer,[\s\S]*?\)\s*\{[^}]*border-width:\s*1px[^}]*border-color:\s*transparent/i
    );
    expect(shellCss).not.toContain('.settings-list');
    expect(settingsCoreCss).toMatch(
      /\.settings-list\s*\{[^}]*border-radius:\s*var\(--settings-card-radius\)\s*!important/i
    );
    expect(settingsCardSurfacesCss).toMatch(
      /\.settings-summary-strip,[\s\S]*?\.settings-section,[\s\S]*?\)\s*\{[^}]*border-radius:\s*(?:16px|var\(--ui-radius-2xl\))\s*!important/i
    );
    expect(shellCss).not.toContain('tool-detail-content');
    expect(shellCss).toMatch(/:is\(\.arco-card,[\s\S]*?\.arco-table-container,[\s\S]*?\)\s*\{[^}]*overflow:\s*hidden/i);
    expect(messageChannelsCss).toMatch(/\.message-channel-card\s*\{[^}]*overflow:\s*hidden/i);
    expect(shellCss).toMatch(
      /:is\(\.arco-modal,\s*\.accessible-content-dialog__surface,\s*\.accessible-action-dialog__dialog\)\s*\{[^}]*border-radius:\s*(?:16px|var\(--ui-radius-2xl\))/i
    );
    expect(shellCss).toMatch(/\.arco-drawer\s*\{[^}]*border-radius:\s*(?:16px|var\(--ui-radius-2xl\)) 0 0 16px/i);
    expect(shellCss).toMatch(
      /:is\(\.arco-modal,\s*\.arco-drawer,\s*\.accessible-content-dialog__surface,\s*\.accessible-action-dialog__dialog\)\s*\{[^}]*overflow:\s*hidden/i
    );
    expect(shellCss).toMatch(
      /@media \(prefers-reduced-motion:\s*reduce\)[\s\S]*?\.css-theme-card,[\s\S]*?transition:\s*none !important/i
    );
  });

  it('normalizes non-shadow Markdown modules as well as streamed Markdown', () => {
    expect(shellCss).toMatch(
      /\.synon-ai-markdown :where\(img\)\s*\{[^}]*border-radius:\s*(?:12px|var\(--ui-radius-xl\))/i
    );
    expect(shellCss).toMatch(
      /\.synon-ai-markdown :where\(blockquote\)\s*\{[^}]*border-left:\s*1px solid[^}]*border-radius:\s*(?:10px|var\(--ui-radius-lg\))/i
    );
    expect(shellCss).toMatch(
      /\.synon-ai-markdown :where\(pre\),[\s\S]*?\[data-streamdown='code-block'\][\s\S]*?border-color:\s*transparent/i
    );
    expect(shellCss).toMatch(
      /\.synon-ai-markdown :where\(table\)\s*\{[^}]*border:\s*1px solid transparent[^}]*border-radius:\s*(?:10px|var\(--ui-radius-lg\))/i
    );
    expect(shellCss).toMatch(/\.synon-ai-markdown :where\(hr\)\s*\{[^}]*border-top:\s*1px solid/i);
  });
});
