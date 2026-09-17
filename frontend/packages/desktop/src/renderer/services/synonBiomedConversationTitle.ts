/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

const MAX_CONVERSATION_TITLE_LENGTH = 60;
const CONVERSATION_TITLE_PREFIX_LENGTH = 57;

/**
 * Keep the full user request in the message payload while deriving the short,
 * stable conversation label used by the backend and sidebar. These boundaries
 * intentionally match the reference runtime's UTF-16 title semantics.
 */
export function buildSynonBiomedConversationTitle(value: string): string {
  const normalized = value.trim();
  return normalized.length <= MAX_CONVERSATION_TITLE_LENGTH
    ? normalized
    : `${normalized.slice(0, CONVERSATION_TITLE_PREFIX_LENGTH)}…`;
}
