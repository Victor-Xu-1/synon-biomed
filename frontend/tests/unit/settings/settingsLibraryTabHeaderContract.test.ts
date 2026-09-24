/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * The merged library page keeps one compact tab header for experts, skills,
 * connectors and environments. Every module must keep its own domain/category
 * filter, because a filterless catalog is unusable — and it must be exactly
 * one control, a single dropdown. A second refinement disclosure (a "filters"
 * toggle bolted next to the category select) is explicitly forbidden: one tab,
 * one filter. Search and result summary stay on standalone routes; connector
 * health and maintenance actions remain available below the merged header,
 * without becoming another filter or a second page authority.
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
const headerCss = read('components/settings-card-density.css');
const skillToolbarSource = read('skills/SkillLibraryToolbar.tsx');

const countOccurrences = (source: string, marker: string): number => source.split(marker).length - 1;

describe('merged library tab headers', () => {
  it('gives every tab one header with a category filter slot', () => {
    for (const [tab, source] of Object.entries(tabSources)) {
      expect(source, tab).toContain('<SettingsLibraryTabHeader');
      expect(source, tab).toContain('filters={');
      expect(source, tab).toContain('<SettingsLibraryFilterSelect');
    }
  });

  it('keeps exactly one domain filter control per tab and no refinement toggle', () => {
    // The single domain/category dropdown is the only filter control the merged
    // header may render — not two, and never a second disclosure next to it.
    for (const [tab, source] of Object.entries(tabSources)) {
      expect(countOccurrences(source, '<SettingsLibraryFilterSelect'), `${tab} filter selects`).toBe(1);
      expect(source, tab).not.toContain('<SettingsLibraryFilterToggle');
      expect(source, tab).not.toContain("className='settings-library-filter-toggle");
    }
  });

  it('has retired the refinement toggle component and its header panel slot', () => {
    expect(() => read('components/SettingsLibraryFilterToggle.tsx')).toThrow();
    expect(headerSource).not.toContain('filterPanel');
    expect(headerSource).not.toContain('settings-library-tab-header__filter-panel');
  });

  it('renders every header filter through the one shared dropdown', () => {
    expect(sharedSelectSource).toContain('settings-library-filter-select');
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
    // There is no disclosure panel left for extra refinements to hide inside.
    expect(headerSource).not.toContain('filter-panel');
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
