/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { isSynonBiomedAssistant, type Assistant } from '@/common/types/agent/assistantTypes';

/**
 * Single source of truth for which assistants appear in a *selection* list
 * (home expert picker, scheduled-task dropdown, and related surfaces) and in what order.
 *
 * Rules for the Synon Biomed fusion build:
 *  - Only enabled Synon Biomed expert assistants are selectable.
 *  - Groups are ordered by source as a deterministic fallback, but old bare
 *    CLI and user-created non-biomedical assistants are filtered out first.
 *  - Within a group, order follows `sort_order` (which the user controls for
 *    CLI/user via drag; official order is manifest-owned by the backend).
 *
 */

/** Group weight — lower comes first. Bare CLI < user-created < official. */
const sourceGroupWeight = (source: string): number => {
  switch (source) {
    case 'generated':
      return 0;
    case 'user':
      return 1;
    case 'builtin':
      return 2;
    default:
      return 1;
  }
};

/**
 * Return the enabled assistants ordered for a selection list:
 * bare → user → builtin, each group sorted by `sort_order`.
 */
export const selectableAssistants = (assistants: Assistant[]): Assistant[] =>
  [...assistants]
    .filter((assistant) => assistant.enabled !== false && isSynonBiomedAssistant(assistant))
    .sort((left, right) => {
      const groupDelta = sourceGroupWeight(left.source) - sourceGroupWeight(right.source);
      if (groupDelta !== 0) return groupDelta;
      return left.sort_order - right.sort_order;
    });
