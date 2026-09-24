/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { afterEach, describe, expect, it, vi } from 'vitest';
import { fetchContextUsage, parseContextUsage, type ContextUsageSnapshot } from '@/renderer/services/contextUsage';

const snapshot = (): ContextUsageSnapshot => ({
  sessionId: 'one',
  requestId: 'request',
  model: 'model',
  observedAt: '2026-01-01T00:00:00Z',
  state: 'complete',
  source: 'provider',
  usedTokens: 20,
  limitTokens: 100,
  limitSource: 'configured',
  outputTokens: 3,
  hasMedia: false,
  inputEstimates: [
    { key: 'systemPrompt', tokens: 4 },
    { key: 'messages', tokens: 8 },
    { key: 'toolDefinitions', tokens: 2 },
  ],
});

describe('context usage contract', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('keeps provider total separate from estimated components and accepts zero', () => {
    const value = snapshot();
    expect(parseContextUsage({ status: 'available', snapshot: value }, 'one')).toEqual({
      status: 'available',
      snapshot: value,
    });
    value.usedTokens = 0;
    expect(parseContextUsage({ status: 'available', snapshot: value }, 'one').status).toBe('available');
  });

  it('makes missing telemetry explicit instead of loading or zero', () => {
    expect(parseContextUsage({ status: 'unavailable' }, 'one')).toEqual({ status: 'unavailable' });
  });

  it.each([
    { usedTokens: -1 },
    { usedTokens: Number.NaN },
    { usedTokens: '20' },
    { limitTokens: 0 },
    { usedTokens: Number.MAX_SAFE_INTEGER + 1 },
    { source: 'cumulative' },
    { state: 'invented' },
    { state: 'request', source: 'provider' },
    { state: 'failed', outputTokens: 3 },
    { limitSource: 'model_default' },
    { observedAt: 'bad date' },
    { sessionId: 'other' },
    { inputEstimates: [] },
    { inputEstimates: [{ key: 'invented', tokens: 20 }] },
  ])('rejects invalid or cross-conversation data: %j', (change) => {
    expect(() => parseContextUsage({ status: 'available', snapshot: { ...snapshot(), ...change } }, 'one')).toThrow();
  });

  it('only requests the authenticated uncached context endpoint', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: 'unavailable' }))));
    const signal = new AbortController().signal;
    await expect(fetchContextUsage('one / two', signal)).resolves.toEqual({ status: 'unavailable' });
    expect(fetch).toHaveBeenCalledWith(
      '/api/conversations/one%20%2F%20two/context-usage',
      expect.objectContaining({
        credentials: 'include',
        cache: 'no-store',
        signal,
      })
    );
  });

  it('rejects transport and invalid JSON failures', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(new Response('{}', { status: 503 }))
        .mockResolvedValueOnce(new Response('invalid'))
    );
    await expect(fetchContextUsage('one', new AbortController().signal)).rejects.toThrow();
    await expect(fetchContextUsage('one', new AbortController().signal)).rejects.toThrow();
  });
});
