/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

export const CONTEXT_USAGE_CATEGORIES = ['systemPrompt', 'tools', 'messages', 'mcp', 'skills'] as const;
export type ContextUsageCategory = (typeof CONTEXT_USAGE_CATEGORIES)[number];
export type ContextUsageBreakdownRow = { key: ContextUsageCategory; tokens: number };
export type ContextUsageSnapshot = {
  sessionId: string;
  requestId: string;
  model: string;
  observedAt: string;
  state: 'request' | 'complete' | 'failed';
  source: 'provider' | 'estimated';
  usedTokens: number;
  limitTokens: number;
  limitSource: 'configured' | 'runner_default';
  outputTokens: number;
  hasMedia: boolean;
  inputEstimates: ContextUsageBreakdownRow[];
};
export type ContextUsageResult = { status: 'unavailable' } | { status: 'available'; snapshot: ContextUsageSnapshot };

function isObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function isTokenCount(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
}

/** Validate the server contract; absent telemetry is not zero or "loading". */
export function parseContextUsage(payload: unknown, conversationId: string): ContextUsageResult {
  if (!isObject(payload)) throw new Error('Invalid context usage response');
  if (payload.status === 'unavailable') return { status: 'unavailable' };
  const value = payload.snapshot;
  if (
    payload.status !== 'available' ||
    !isObject(value) ||
    value.sessionId !== conversationId ||
    typeof value.requestId !== 'string' ||
    !value.requestId ||
    typeof value.model !== 'string' ||
    typeof value.observedAt !== 'string' ||
    !Number.isFinite(Date.parse(value.observedAt)) ||
    !['request', 'complete', 'failed'].includes(String(value.state)) ||
    !['provider', 'estimated'].includes(String(value.source)) ||
    !['configured', 'runner_default'].includes(String(value.limitSource)) ||
    !isTokenCount(value.usedTokens) ||
    !isTokenCount(value.limitTokens) ||
    value.limitTokens === 0 ||
    !isTokenCount(value.outputTokens) ||
    (isTokenCount(value.outputTokens) && isTokenCount(value.usedTokens) && value.outputTokens > value.usedTokens) ||
    (value.state !== 'complete' && (value.source !== 'estimated' || value.outputTokens !== 0)) ||
    typeof value.hasMedia !== 'boolean' ||
    !Array.isArray(value.inputEstimates) ||
    value.inputEstimates.length !== CONTEXT_USAGE_CATEGORIES.length
  ) {
    throw new Error('Invalid context usage record');
  }
  for (const [index, row] of value.inputEstimates.entries()) {
    if (!isObject(row) || row.key !== CONTEXT_USAGE_CATEGORIES[index] || !isTokenCount(row.tokens)) {
      throw new Error('Invalid context usage breakdown');
    }
  }
  if (
    value.usedTokens > value.outputTokens &&
    value.inputEstimates.every((row) => (row as ContextUsageBreakdownRow).tokens === 0)
  ) {
    throw new Error('Missing context usage input weights');
  }
  return { status: 'available', snapshot: value as ContextUsageSnapshot };
}

/**
 * Provider totals and input estimates have different authorities. Preserve
 * their proportions without pretending the estimates exactly measured usage.
 * BigInt keeps the allocation stable even near the safe-integer boundary.
 */
export function reconcileContextUsageBreakdown(snapshot: ContextUsageSnapshot): ContextUsageBreakdownRow[] {
  const inputTokens = snapshot.usedTokens - snapshot.outputTokens;
  if (inputTokens < 0) throw new Error('Invalid context usage total');
  const totalWeight = snapshot.inputEstimates.reduce((sum, row) => sum + BigInt(row.tokens), BigInt(0));
  if (totalWeight === BigInt(0) && inputTokens > 0) throw new Error('Missing context usage input weights');
  const rows = snapshot.inputEstimates.map((row) => {
    const numerator = BigInt(inputTokens) * BigInt(row.tokens);
    return {
      key: row.key,
      tokens: totalWeight === BigInt(0) ? 0 : Number(numerator / totalWeight),
      remainder: totalWeight === BigInt(0) ? BigInt(0) : numerator % totalWeight,
    };
  });
  const undistributed = inputTokens - rows.reduce((sum, row) => sum + row.tokens, 0);
  const order = rows
    .map((row, index) => ({ index, remainder: row.remainder }))
    .toSorted((left, right) =>
      left.remainder === right.remainder ? left.index - right.index : left.remainder > right.remainder ? -1 : 1
    );
  for (let index = 0; index < undistributed; index += 1) rows[order[index].index].tokens += 1;
  rows[CONTEXT_USAGE_CATEGORIES.indexOf('messages')].tokens += snapshot.outputTokens;
  return rows.map(({ key, tokens }) => ({ key, tokens }));
}

export async function fetchContextUsage(conversationId: string, signal: AbortSignal): Promise<ContextUsageResult> {
  const response = await fetch(`/api/conversations/${encodeURIComponent(conversationId)}/context-usage`, {
    credentials: 'include',
    cache: 'no-store',
    headers: { Accept: 'application/json' },
    signal,
  });
  if (!response.ok) throw new Error(`Context usage request failed: ${response.status}`);
  return parseContextUsage(await response.json(), conversationId);
}
