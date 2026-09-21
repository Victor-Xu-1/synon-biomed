/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from 'vitest';

import {
  buildContextUsageBreakdown,
  estimateChatMessageTokens,
  estimateConversationMessagesTokens,
  estimateTextTokens,
} from '@/renderer/services/contextUsage';

describe('context usage estimation', () => {
  it('mirrors the server-side text estimator for ASCII and CJK payloads', () => {
    expect(estimateTextTokens('')).toBe(0);
    expect(estimateTextTokens(null)).toBe(0);
    expect(estimateTextTokens('abcd')).toBe(1);
    expect(estimateTextTokens('abcdefgh')).toBe(2);
    // CJK runes count one token each.
    expect(estimateTextTokens('分子模拟')).toBe(4);
  });

  it('adds the per-message overhead and tool-call accounting', () => {
    const message = {
      role: 'user',
      content: 'abcd',
      tool_calls: [
        {
          id: 'call_1',
          function: { name: 'search', arguments: '{"query":"abcd"}' },
        },
      ],
    };
    const total = estimateChatMessageTokens(message);
    expect(total).toBeGreaterThan(4);
    expect(estimateConversationMessagesTokens([message, message])).toBe(total * 2);
  });

  it('counts the structured content shape returned by conversation history', () => {
    const projectedText = {
      type: 'text',
      position: 'right',
      content: { content: 'abcdefgh' },
    };
    const projectedTool = {
      type: 'tool_call',
      content: { name: 'search', input: { query: 'abcd' }, output: 'efgh' },
    };
    expect(estimateChatMessageTokens(projectedText)).toBe(4 + 1 + 2);
    expect(estimateChatMessageTokens(projectedTool)).toBeGreaterThan(4);
  });

  it('pins the breakdown rows to the authoritative used total', () => {
    const rows = buildContextUsageBreakdown({ usedTokens: 133_000, limitTokens: 300_000, messagesTokens: 84_000 });
    const sum = rows.reduce((total, row) => total + row.tokens, 0);
    expect(sum).toBe(133_000);
    const keys = rows.map((row) => row.key);
    expect(keys).toEqual(['systemPrompt', 'toolsAndSubagents', 'messages', 'connectorsAndMcp', 'skills']);
    expect(rows.find((row) => row.key === 'messages')?.tokens).toBe(84_000);
  });

  it('keeps every row non-negative and clamps message estimates to the total', () => {
    const rows = buildContextUsageBreakdown({ usedTokens: 10, limitTokens: 300_000, messagesTokens: 99_999 });
    for (const row of rows) expect(row.tokens).toBeGreaterThanOrEqual(0);
    expect(rows.reduce((total, row) => total + row.tokens, 0)).toBe(10);
  });

  it('falls back to capability shares when the message estimate is unavailable', () => {
    const rows = buildContextUsageBreakdown({ usedTokens: 50_000, limitTokens: 300_000, messagesTokens: null });
    expect(rows.reduce((total, row) => total + row.tokens, 0)).toBe(50_000);
    expect(rows.find((row) => row.key === 'messages')?.tokens).toBe(0);
  });
});
