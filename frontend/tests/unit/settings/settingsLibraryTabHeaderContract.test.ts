/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * The merged library page keeps one compact tab header for experts, skills,
 * connectors and environments. Every module must keep its own category filter,
 * because a filterless catalog is unusable — but only the filters come back.
 * The search box, result summary, health row and secondary maintenance
 * controls stay on the standalone routes, so this contract pins the header
 * shape instead of trusting the next edit to remember it.
 */

import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const settingsDir = fileURLToPath(new URL('../../../packages/desktop/src/renderer/pages/settings/', import.meta.url));
const read = (relative: string): string => readFileSync(path.join(settingsDir, relative), 'utf8');

const tabSources = {
  experts: read('SynonBiomedExpertsSettings/ExpertWorkbench.tsx'),
  skills: read('SynonBiomedSkillsSettings.tsx'),
  connectors: read('ToolsSettings/McpLibraryToolbar.tsx'),
  environments: read('ScientificEnvironmentSettings.tsx'),
} as const;

const headerSource = read('components/SettingsLibraryTabHeader.tsx');
const sharedSelectSource = read('components/SettingsLibraryFilterSelect.tsx');
const sharedToggleSource = read('components/SettingsLibraryFilterToggle.tsx');
const headerCss = read('components/settings-card-density.css');
const skillToolbarSource = read('skills/SkillLibraryToolbar.tsx');

describe('merged library tab headers', () => {
  it('gives every tab one header with a category filter slot', () => {
    for (const [tab, source] of Object.entries(tabSources)) {
      expect(source, tab).toContain('<SettingsLibraryTabHeader');
      expect(source, tab).toContain('filters={');
      expect(source, tab).toContain('<SettingsLibraryFilterSelect');
    }
  });

  it('keeps one refinement disclosure only on the tabs that refine beyond their category', () => {
    // Experts and connectors are fully described by their single category dropdown.
    expect(tabSources.experts).not.toContain('<SettingsLibraryFilterToggle');
    expect(tabSources.connectors).not.toContain('<SettingsLibraryFilterToggle');
    expect(tabSources.experts).not.toContain('filterPanel=');
    expect(tabSources.connectors).not.toContain('filterPanel=');
    // Skills and environments keep the source/status refinements they always had.
    expect(tabSources.skills).toContain('<SettingsLibraryFilterToggle');
    expect(tabSources.environments).toContain('<SettingsLibraryFilterToggle');
    expect(tabSources.skills).toContain('filterPanel=');
    expect(tabSources.environments).toContain('filterPanel=');
  });

  it('renders every header filter through the one shared dropdown', () => {
    expect(sharedSelectSource).toContain('settings-library-filter-select');
    expect(sharedToggleSource).toContain('settings-library-filter-toggle');
    expect(headerSource).toContain('settings-library-tab-header__filters');
    // No tab restates its own header dropdown.
    for (const [tab, source] of Object.entries(tabSources)) {
      expect(source, tab).not.toContain("className='settings-library-filter-select");
      expect(source, tab).not.toContain("className='settings-library-filter-toggle");
    }
  });

  it('pins the shared control geometry in one stylesheet', () => {
    const select = /\.settings-library-filter-select\s*\{([^}]*)\}/.exec(headerCss)?.[1] ?? '';
    expect(select).toMatch(/height:\s*var\(--ui-control-md\)/);
    expect(select).toMatch(/border-radius:\s*var\(--ui-radius-md\)/);
    expect(select).toMatch(/font-size:\s*var\(--ui-font-body\)/);
    expect(select).toMatch(/padding:\s*0 var\(--ui-control-padding-x\)/);
    expect(headerCss).toMatch(/\.settings-library-tab-header__row\s*\{[^}]*min-height:\s*40px/);
    expect(headerCss).toMatch(/\.settings-library-tab-header__row\s*\{[^}]*gap:\s*8px/);
    expect(headerCss).toMatch(/\.settings-library-tab-header__filters\s*\{[^}]*gap:\s*8px/);
    expect(headerCss).toMatch(/\.settings-library-tab-header__actions\s*\{[^}]*gap:\s*8px/);
    // The filter cluster owns the right alignment so the primary action always
    // stays exactly 8px to the right of the last filter control.
    expect(headerCss).toMatch(/\.settings-library-tab-header__filters\s*\{[^}]*margin-inline-start:\s*auto/);
    expect(headerCss).toMatch(/\.settings-library-tab-header__actions\s*\{[^}]*margin-inline-start:\s*0/);
  });

  it('adds no search box, result summary or maintenance control to the merged header', () => {
    expect(headerSource).not.toMatch(/<input|<Input\b|type='search'/);
    expect(sharedSelectSource).not.toMatch(/<Input\b|type='search'/);
    expect(sharedToggleSource).not.toMatch(/<Input\b|type='search'/);
    // Disclosed refinements stay inside the header's own panel slot.
    expect(headerSource).toContain('settings-library-tab-header__filter-panel');
    for (const [tab, source] of Object.entries(tabSources)) {
      expect(source, tab).not.toContain('settings-library-tab-header__summary');
    }
  });

  it.each([
    ['experts', tabSources.experts, 'settings.expertsSettings.searchPlaceholder', '!compactHeader'],
    ['connectors', tabSources.connectors, "data-testid='synon-biomed-mcp-search'", '!props.compactHeader'],
    ['environments', tabSources.environments, "className='environment-search'", '!compactHeader'],
  ] as const)('keeps the %s search box on the standalone route only', (_tab, source, marker, guard) => {
    const guardIndex = source.indexOf(guard);
    expect(guardIndex).toBeGreaterThanOrEqual(0);
    expect(source.indexOf(marker)).toBeGreaterThan(guardIndex);
  });

  it('keeps the Skills library toolbar, and its search box, off the merged header', () => {
    expect(tabSources.skills.indexOf('!compactHeader')).toBeLessThan(tabSources.skills.indexOf('<SkillLibraryToolbar'));
    expect(skillToolbarSource).toContain("data-testid='input-search-synon-biomed-skills'");
    expect(tabSources.skills).not.toContain("data-testid='input-search-synon-biomed-skills'");
  });
});
