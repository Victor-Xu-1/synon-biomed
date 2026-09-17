/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * Input parameters for determining slash command list availability.
 */
export interface SlashCommandListAvailabilityInput {
  /** Active Synon Biomed conversation transport. */
  conversation_type?: string;
}

/**
 * Determines whether the slash command autocomplete list should be enabled.
 *
 * Slash commands are supported by ACP conversations. Synon Biomed is exposed
 * through the ACP runtime in this product, while retired agent runtimes are
 * kept read-only and must not call runtime tool endpoints.
 *
 * @param input - Conversation type and status information
 * @returns true if slash commands should be enabled
 */
export function isSlashCommandListEnabled(input: SlashCommandListAvailabilityInput): boolean {
  return input.conversation_type === 'acp';
}
