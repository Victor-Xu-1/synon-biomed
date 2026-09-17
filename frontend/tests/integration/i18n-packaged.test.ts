/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { existsSync, readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import i18nConfig from '../../packages/desktop/src/common/config/i18n-config.json';
import enConversation from '../../packages/desktop/src/renderer/services/i18n/locales/en-US/conversation.json';
import zhConversation from '../../packages/desktop/src/renderer/services/i18n/locales/zh-CN/conversation.json';

const rendererOutput = path.resolve(process.cwd(), 'out/renderer');

function collectJavaScriptFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const entryPath = path.join(directory, entry.name);
    if (entry.isDirectory()) return collectJavaScriptFiles(entryPath);
    return entry.isFile() && entry.name.endsWith('.js') ? [entryPath] : [];
  });
}

describe('packaged renderer translations', () => {
  it('includes the model-list recovery copy for every supported locale', () => {
    const entryHtml = path.join(rendererOutput, 'index.html');
    expect(existsSync(entryHtml), 'out/renderer/index.html is missing; run `npm run build` before this gate').toBe(
      true
    );

    const scripts = collectJavaScriptFiles(rendererOutput);
    expect(scripts.length).toBeGreaterThan(0);

    const packagedJavaScript = scripts.map((file) => readFileSync(file, 'utf8')).join('\n');
    const localizedUnavailableLabels: Record<string, string> = {
      'en-US': enConversation.welcome.modelListUnavailable,
      'zh-CN': zhConversation.welcome.modelListUnavailable,
    };

    expect(Object.keys(localizedUnavailableLabels).toSorted()).toEqual(i18nConfig.supportedLanguages.toSorted());

    for (const label of Object.values(localizedUnavailableLabels)) {
      expect(packagedJavaScript, `missing packaged translation: ${label}`).toContain(label);
    }
  });
});
