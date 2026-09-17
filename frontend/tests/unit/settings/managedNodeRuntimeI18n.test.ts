/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';

function localeRoot(): URL {
  return new URL('../../../packages/desktop/src/renderer/services/i18n/locales/', import.meta.url);
}

function settingsLanguages(): string[] {
  return readdirSync(localeRoot(), { withFileTypes: true })
    .filter((entry) => entry.isDirectory())
    .map((entry) => entry.name);
}

function loadSettingsLocale(language: string): Record<string, string> {
  const url = new URL(`${language}/settings.json`, localeRoot());
  return JSON.parse(readFileSync(url, 'utf8')) as Record<string, string>;
}

function loadCommonLocale(language: string): Record<string, unknown> {
  const url = new URL(
    `../../../packages/desktop/src/renderer/services/i18n/locales/${language}/common.json`,
    import.meta.url
  );
  return JSON.parse(readFileSync(url, 'utf8')) as Record<string, unknown>;
}

function loadConversationLocale(language: string): Record<string, unknown> {
  const url = new URL(
    `../../../packages/desktop/src/renderer/services/i18n/locales/${language}/conversation.json`,
    import.meta.url
  );
  return JSON.parse(readFileSync(url, 'utf8')) as Record<string, unknown>;
}

describe('Synon Biomed runtime settings copy', () => {
  it('directs missing MCP commands to the Synon Biomed runtime contract', () => {
    const en = loadSettingsLocale('en-US');
    const zh = loadSettingsLocale('zh-CN');

    expect(en.mcpErrorNodeCommandNotFound).not.toContain('Install Node.js');
    expect(en.mcpErrorNodeCommandNotFound).toContain('Synon Biomed runtime');

    expect(zh.mcpErrorNodeCommandNotFound).not.toContain('安装 Node.js');
    expect(zh.mcpErrorNodeCommandNotFound).toContain('Synon Biomed 运行时');
  });

  it('uses the product name for the manual restart instruction', () => {
    const zh = loadSettingsLocale('zh-CN');

    expect(zh.restartManualRequired).toContain('Synon Biomed 服务');
    expect(zh.restartManualRequired).not.toContain('Synon Go');
  });

  it('keeps the warmup hint generic until the backend can prove node-specific preparation', () => {
    const en = loadConversationLocale('en-US');
    const zh = loadConversationLocale('zh-CN');

    expect((en.runtimePreparing as Record<string, string>).sendboxHint).toContain('runtime environment');
    expect((en.runtimePreparing as Record<string, string>).sendboxHint).not.toContain('managed Node');

    expect((zh.runtimePreparing as Record<string, string>).sendboxHint).toContain('运行环境');
    expect((zh.runtimePreparing as Record<string, string>).sendboxHint).not.toContain('托管的 Node');
  });

  it('defines only the Synon Biomed backend startup message in every common locale', () => {
    for (const language of settingsLanguages()) {
      const common = loadCommonLocale(language);
      const backendStartup = common.backendStartup as Record<string, unknown>;
      const synonBiomed = backendStartup.synonBiomed as Record<string, string>;

      expect(Object.keys(backendStartup), language).toEqual(['synonBiomed']);
      expect(synonBiomed.title, language).toBeTruthy();
      expect(synonBiomed.description, language).toContain('Synon Biomed');
    }
  });
});
