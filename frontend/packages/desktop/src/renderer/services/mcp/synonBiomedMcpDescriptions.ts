/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import enUSSettings from '../i18n/locales/en-US/settings.json';
import zhCNSettings from '../i18n/locales/zh-CN/settings.json';

type McpDescriptionLocale = { mcpBuiltinDescriptions?: Record<string, string> };

const BUILTIN_MCP_DESCRIPTIONS = {
  'en-US': (enUSSettings as McpDescriptionLocale).mcpBuiltinDescriptions ?? {},
  'zh-CN': (zhCNSettings as McpDescriptionLocale).mcpBuiltinDescriptions ?? {},
};

export function resolveSynonBiomedMcpDescription(
  name: string,
  fallback: string | undefined,
  language: string | undefined,
  localized?: Record<string, string>
): string {
  const locale = language?.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en-US';
  const localizedValue = localized?.[locale] ?? localized?.[language ?? ''] ?? localized?.[locale.split('-')[0] ?? ''];
  if (localizedValue) return localizedValue;
  if (locale === 'zh-CN') {
    const translated = BUILTIN_MCP_DESCRIPTIONS['zh-CN'][name];
    if (translated) return translated;
  }
  return fallback || BUILTIN_MCP_DESCRIPTIONS['en-US'][name] || name;
}
