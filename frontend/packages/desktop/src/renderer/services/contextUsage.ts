/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * Client-side context accounting for the composer usage panel.
 *
 * The conversation already exposes the authoritative context size
 * (`last_token_usage.total_tokens`) plus its window capacity
 * (`last_context_limit`); both feed the existing ring indicator today. This
 * service reuses the same estimation approach the server-side runner applies
 * for compaction pressure, so the category split tracks the same accounting
 * instead of inventing numbers:
 *
 * - 对话消息 (messages): estimated from the durable message payload with the
 *   backend-identical CJK-aware estimator — the dominant share of every
 *   request.
 * - 系统提示词 / 工具及子智能体 / 连接器及MCP / 技能: fixed shares of the
 *   residual after the message estimate. The residual is real (authoritative
 *   used − messages) and always sums with the message row to the total, but
 *   the split among the three capability rows is indicative until the runner
 *   exposes per-part accounting.
 */

export type ContextUsageCategoryKey = 'systemPrompt' | 'toolsAndSubagents' | 'messages' | 'connectorsAndMcp' | 'skills';

export type ContextUsageRow = {
  key: ContextUsageCategoryKey;
  tokens: number;
};

/** Same per-message accounting overhead the server estimator applies. */
const MESSAGE_OVERHEAD_TOKENS = 4;

/** Fixed capability shares of the non-message residual (documented split). */
const CAPABILITY_SHARES: Record<'toolsAndSubagents' | 'connectorsAndMcp' | 'skills', number> = {
  toolsAndSubagents: 0.62,
  connectorsAndMcp: 0.3,
  skills: 0.08,
};

export const CONTEXT_USAGE_COLORS: Record<ContextUsageCategoryKey, string> = {
  systemPrompt: '#4a7dff',
  toolsAndSubagents: '#00b42a',
  messages: '#ff9a2e',
  connectorsAndMcp: '#722ed1',
  skills: '#f5319d',
};

/** Literal i18n keys kept static so the generated key union stays satisfied. */
export const CONTEXT_USAGE_LABEL_KEYS: Record<ContextUsageCategoryKey, string> = {
  systemPrompt: 'conversation.contextUsage.systemPrompt',
  toolsAndSubagents: 'conversation.contextUsage.toolsAndSubagents',
  messages: 'conversation.contextUsage.messages',
  connectorsAndMcp: 'conversation.contextUsage.connectorsAndMcp',
  skills: 'conversation.contextUsage.skills',
};

/**
 * Mirror of the server-side text estimator (internal/server
 * runner_context_scope.estimateTextTokens): ASCII text counts at roughly four
 * runes per token, CJK and other non-ASCII runes count one token each.
 */
export function estimateTextTokens(text: string | null | undefined): number {
  if (typeof text !== 'string' || text.length === 0) return 0;
  let asciiRunes = 0;
  let nonAsciiRunes = 0;
  for (const current of text) {
    const codePoint = current.codePointAt(0) ?? 0;
    if (codePoint <= 0x7f) {
      asciiRunes += 1;
    } else {
      nonAsciiRunes += 1;
    }
  }
  return Math.max(1, nonAsciiRunes + Math.trunc((asciiRunes + 3) / 4));
}

function estimateToolCallTokens(call: unknown): number {
  if (!call || typeof call !== 'object') return 0;
  const record = call as Record<string, unknown>;
  const fn = record.function as Record<string, unknown> | undefined;
  return (
    estimateTextTokens(String(record.id ?? '')) +
    estimateTextTokens(typeof fn?.name === 'string' ? fn.name : '') +
    estimateTextTokens(typeof fn?.arguments === 'string' ? fn.arguments : '')
  );
}

export function estimateChatMessageTokens(item: unknown): number {
  if (!item || typeof item !== 'object') return 0;
  const record = item as Record<string, unknown>;
  let total = MESSAGE_OVERHEAD_TOKENS;
  total += estimateTextTokens(typeof record.role === 'string' ? record.role : '');
  const content = record.content;
  if (typeof content === 'string') {
    total += estimateTextTokens(content);
  } else if (Array.isArray(content)) {
    for (const part of content) {
      if (part && typeof part === 'object' && typeof (part as Record<string, unknown>).text === 'string') {
        total += estimateTextTokens((part as Record<string, unknown>).text as string);
      }
    }
  }
  if (Array.isArray(record.tool_calls)) {
    for (const call of record.tool_calls) total += estimateToolCallTokens(call);
  }
  return total;
}

export function estimateConversationMessagesTokens(items: unknown): number {
  if (!Array.isArray(items)) return 0;
  let total = 0;
  for (const item of items) total += estimateChatMessageTokens(item);
  return total;
}

export type ContextUsageBreakdownInput = {
  /** Authoritative used tokens from the conversation's last usage record. */
  usedTokens: number;
  limitTokens: number;
  /** Backend-parity estimate of the durable message payload, or null when unavailable. */
  messagesTokens: number | null;
};

export type ContextUsageBreakdown = {
  usedTokens: number;
  limitTokens: number;
  rows: ContextUsageRow[];
};

/**
 * Build the five-row breakdown. Every row sums exactly to the authoritative
 * used figure; only the capability split is indicative.
 */
export function buildContextUsageBreakdown(input: ContextUsageBreakdownInput): ContextUsageRow[] {
  const usedTokens = Math.max(0, Math.round(input.usedTokens));
  const messagesTokens =
    input.messagesTokens === null ? null : Math.max(0, Math.min(Math.round(input.messagesTokens), usedTokens));
  const residual = usedTokens - (messagesTokens ?? 0);
  const systemTokens = messagesTokens === null ? 0 : Math.round(residual * 0.18);
  const capabilityResidual = messagesTokens === null ? usedTokens : residual - systemTokens;
  const rows: ContextUsageRow[] = [
    { key: 'systemPrompt', tokens: systemTokens },
    {
      key: 'toolsAndSubagents',
      tokens: Math.round(capabilityResidual * CAPABILITY_SHARES.toolsAndSubagents),
    },
    { key: 'messages', tokens: messagesTokens ?? 0 },
    {
      key: 'connectorsAndMcp',
      tokens: Math.round(capabilityResidual * CAPABILITY_SHARES.connectorsAndMcp),
    },
    { key: 'skills', tokens: Math.round(capabilityResidual * CAPABILITY_SHARES.skills) },
  ];
  // Keep the sum pinned to the authoritative total after rounding.
  const drift = usedTokens - rows.reduce((total, row) => total + row.tokens, 0);
  const anchor = rows.find((row) => row.key === 'toolsAndSubagents');
  if (drift !== 0 && anchor && anchor.tokens + drift >= 0) {
    anchor.tokens += drift;
  }
  return rows;
}
