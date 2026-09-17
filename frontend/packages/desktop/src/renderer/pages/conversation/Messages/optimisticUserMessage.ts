/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

export const SYNON_BIOMED_OPTIMISTIC_USER_MESSAGE_PREFIX = 'synonbiomed-optimistic-user:';

export function isSynonBiomedOptimisticUserMessage(messageId: string | null | undefined): boolean {
  return Boolean(messageId?.startsWith(SYNON_BIOMED_OPTIMISTIC_USER_MESSAGE_PREFIX));
}
