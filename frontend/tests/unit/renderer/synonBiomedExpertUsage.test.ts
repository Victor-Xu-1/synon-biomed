import { describe, expect, it, vi } from 'vitest';
import {
  aggregateSynonBiomedExpertUsage,
  findSynonBiomedExpertUsage,
  loadSynonBiomedExpertUsage,
} from '@/renderer/services/agents/synonBiomedExpertUsage';

function jsonResponse(payload: unknown): Response {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  });
}

describe('synonBiomedExpertUsage', () => {
  it('aggregates conversation counts and keeps the latest activity time', () => {
    const usage = aggregateSynonBiomedExpertUsage({}, [
      {
        modified_at: 1_700_000_000_000,
        extra: { agent_name: 'AIDD_EXPERT' },
        assistant: { name: 'ignored-assistant-name' },
      },
      {
        modified_at: '2023-01-01T00:00:00.000Z',
        assistant: { name: 'AIDD_EXPERT' },
      },
      {
        modified_at: 1_600_000_000_000,
        extra: { agent_name: 'OPERON' },
      },
      { modified_at: 1_700_000_000_000, extra: { agent_name: '' } },
      { modified_at: 'not-a-date', assistant: { name: 'AIDD_EXPERT' } },
    ]);

    expect(usage.AIDD_EXPERT).toEqual({
      invocationCount: 3,
      lastUsedAt: new Date(1_700_000_000_000).toISOString(),
    });
    expect(usage.OPERON).toEqual({
      invocationCount: 1,
      lastUsedAt: new Date(1_600_000_000_000).toISOString(),
    });
    expect(findSynonBiomedExpertUsage(usage, 'aidd_expert')).toEqual(usage.AIDD_EXPERT);
    expect(findSynonBiomedExpertUsage(usage, 'missing')).toBeNull();
  });

  it('follows the existing conversation cursor without creating a second backend route', async () => {
    const fetchImpl = vi.fn();
    fetchImpl
      .mockResolvedValueOnce(
        jsonResponse({
          items: [{ modified_at: 1_700_000_000_000, extra: { agent_name: 'AIDD_EXPERT' } }],
          has_more: true,
          next_cursor: 'cursor-1',
        })
      )
      .mockResolvedValueOnce(
        jsonResponse({
          items: [{ modified_at: 1_700_000_100_000, assistant: { name: 'OPERON' } }],
          has_more: false,
          next_cursor: null,
        })
      );

    const usage = await loadSynonBiomedExpertUsage({
      baseUrl: 'http://test.local',
      fetchImpl: fetchImpl as typeof fetch,
    });

    expect(usage.AIDD_EXPERT.invocationCount).toBe(1);
    expect(usage.OPERON.lastUsedAt).toBe(new Date(1_700_000_100_000).toISOString());
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    expect(fetchImpl.mock.calls[0][0]).toBe('http://test.local/api/conversations?limit=1000');
    expect(fetchImpl.mock.calls[1][0]).toBe('http://test.local/api/conversations?limit=1000&cursor=cursor-1');
  });

  it('rejects an incomplete page rather than showing a partial count', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(jsonResponse({ items: [], has_more: true, next_cursor: null }));

    await expect(loadSynonBiomedExpertUsage({ fetchImpl: fetchImpl as typeof fetch })).rejects.toThrow(
      'invalid pagination cursor'
    );
  });
});
