/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { FILE_EXTENSION_MAP } from '@/renderer/pages/conversation/Preview/fileUtils';

/**
 * Keep the workspace context-menu capability gate derived from the canonical
 * preview registry. Unsupported formats are included deliberately: they still
 * have a safe read-only fallback with an original-file download/open action.
 */
export const PREVIEW_SUPPORTED_EXTENSIONS: Set<string> = new Set(
  Object.values(FILE_EXTENSION_MAP)
    .flat()
    .map((extension) => extension.toLowerCase())
);

/**
 * Check whether a file supports in-app preview based on its extension
 */
export function isPreviewSupportedExt(filename: string): boolean {
  if (!filename) return false;
  const normalized = filename.toLowerCase();
  return Array.from(PREVIEW_SUPPORTED_EXTENSIONS).some(
    (extension) => normalized === extension || normalized.endsWith(`.${extension}`)
  );
}
