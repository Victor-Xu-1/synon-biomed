/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { configService } from '@/common/config/configService';
import { isSystemThemeId } from '@/common/theme/constants';
import { setActiveTheme } from './applyTheme';
import { watchSystemPrefersDark } from './systemAppearance';

/** Re-resolve the current family when the OS appearance changes. */
export function startSystemThemeWatcher(): () => void {
  return watchSystemPrefersDark(() => {
    const activeId = configService.get('theme.activeId') as string | undefined;
    if (!activeId || !isSystemThemeId(activeId)) return;
    void setActiveTheme(activeId).catch((error) => console.error('re-apply system theme failed', error));
  });
}
