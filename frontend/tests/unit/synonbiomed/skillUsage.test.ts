import { describe, expect, it, vi } from 'vitest';
import {
  aggregateSynonBiomedSkillUsage,
  findSynonBiomedSkillUsage,
  loadSynonBiomedSkillUsage,
} from '@/renderer/services/skills/synonBiomedSkillUsage';

describe('Synon Biomed skill usage service', () => {
  it('aggregates durable invocation records and keeps the latest valid timestamp', () => {
    const usage = aggregateSynonBiomedSkillUsage([
      {
        value: { skill: 'governed_vina_docking', createdAt: '2026-08-14T08:00:00Z' },
        updatedAt: '2026-08-14T08:00:01Z',
      },
      {
        value: { skill: 'governed_vina_docking', created_at: '2026-08-15T08:00:00Z' },
      },
      { value: { skill: 'other-skill', createdAt: 'not-a-date' } },
      { value: { skill: '' } },
      { value: { description: 'not an invocation' } },
    ]);

    expect(usage).toEqual({
      governed_vina_docking: { invocationCount: 2, lastUsedAt: '2026-08-15T08:00:00Z' },
      'other-skill': { invocationCount: 1, lastUsedAt: null },
    });
    expect(findSynonBiomedSkillUsage(usage, 'GOVERNED_VINA_DOCKING')).toEqual({
      invocationCount: 2,
      lastUsedAt: '2026-08-15T08:00:00Z',
    });
  });

  it('reads the authenticated aggregated usage endpoint', async () => {
    const fetchImpl = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(
        JSON.stringify({
          usage: [{ name: 'my-skill', invocationCount: 2, lastUsedAt: '2026-08-16T02:02:03Z' }],
        }),
        { status: 200, headers: { 'content-type': 'application/json' } }
      )
    );

    await expect(loadSynonBiomedSkillUsage({ baseUrl: 'http://fusion.test', fetchImpl })).resolves.toEqual({
      'my-skill': { invocationCount: 2, lastUsedAt: '2026-08-16T02:02:03Z' },
    });
    expect(fetchImpl).toHaveBeenCalledWith(
      'http://fusion.test/api/skills/usage',
      expect.objectContaining({
        method: 'GET',
      })
    );
  });

  it('rejects malformed usage responses instead of showing false zero usage', async () => {
    const fetchImpl = vi
      .fn<typeof fetch>()
      .mockResolvedValue(
        new Response(JSON.stringify({ usage: [{ name: 'broken', invocationCount: '3' }] }), { status: 200 })
      );

    await expect(loadSynonBiomedSkillUsage({ baseUrl: 'http://fusion.test', fetchImpl })).rejects.toThrow(
      'skill usage response is invalid'
    );
  });
});
