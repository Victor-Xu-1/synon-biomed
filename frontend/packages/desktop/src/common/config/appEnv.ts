/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { getPlatformServices } from '@/common/platform';

/**
 * Returns baseName unchanged in release builds, or baseName + '-dev' in dev builds.
 * When SYNON_AI_MULTI_INSTANCE=1, appends '-2' to isolate the second dev instance.
 * Used to isolate symlink and directory names between environments.
 *
 * @example
 * getEnvAwareName('.synon-ai')        // release → '.synon-ai',        dev → '.synon-ai-dev'
 * getEnvAwareName('.synon-ai-config') // release → '.synon-ai-config', dev → '.synon-ai-config-dev'
 * // with SYNON_AI_MULTI_INSTANCE=1:  dev → '.synon-ai-dev-2'
 */
export function getEnvAwareName(baseName: string): string {
  if (getPlatformServices().paths.isPackaged() === true) return baseName;
  const suffix = process.env.SYNON_AI_MULTI_INSTANCE === '1' ? '-dev-2' : '-dev';
  return `${baseName}${suffix}`;
}
