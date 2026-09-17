/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import enPreview from '@/renderer/services/i18n/locales/en-US/preview.json';
import zhPreview from '@/renderer/services/i18n/locales/zh-CN/preview.json';
import { describe, expect, it } from 'vitest';

function collectLeafPaths(value: unknown, prefix = ''): string[] {
  if (value === null || typeof value !== 'object') return [prefix];
  if (Array.isArray(value)) {
    return value.flatMap((entry, index) => collectLeafPaths(entry, `${prefix}[${index}]`));
  }
  return Object.entries(value).flatMap(([key, entry]) => collectLeafPaths(entry, prefix ? `${prefix}.${key}` : key));
}

describe('preview i18n resources', () => {
  it('keeps English and Chinese translation keys structurally identical', () => {
    expect(collectLeafPaths(enPreview).toSorted()).toEqual(collectLeafPaths(zhPreview).toSorted());
  });
});
