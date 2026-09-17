import { describe, expect, it, vi } from 'vitest';
import {
  loadSynonBiomedAccountInsights,
  parseAccountOverview,
} from '@/renderer/services/account/synonBiomedAccountInsights';

function response(payload: unknown, status = 200): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

function validActivityDays() {
  const start = new Date(Date.UTC(2025, 7, 10));
  return Array.from({ length: 53 * 7 }, (_, index) => {
    const date = new Date(start);
    date.setUTCDate(start.getUTCDate() + index);
    return {
      date: date.toISOString().slice(0, 10),
      count: index % 19 === 0 ? 2 : 0,
      tokenCount: index % 19 === 0 ? 24_800 : 0,
      isFuture: index > 365,
    };
  });
}

function validOverview() {
  return {
    metrics: {
      totalTasks: 9,
      completedTasks: 7,
      projectCount: 3,
      artifactCount: 12,
      currentStreak: 2,
      longestStreak: 5,
      completionRate: 7 / 9,
      recentTaskCount: 4,
      activeDayCount: 21,
    },
    activityDays: validActivityDays(),
    topSkills: [{ name: 'docking', invocationCount: 8, lastUsedAt: '2026-08-10T06:00:00Z' }],
    availability: { projects: true, skills: true, tokenUsage: true },
    loadedAt: '2026-08-10T06:00:00Z',
  };
}

describe('synonBiomedAccountInsights', () => {
  it('loads one authenticated, timezone-aware account overview request', async () => {
    const fetchImpl = vi.fn<typeof fetch>().mockResolvedValue(response(validOverview()));

    const insights = await loadSynonBiomedAccountInsights({
      baseUrl: 'http://account.test',
      fetchImpl,
    });

    expect(insights.metrics).toMatchObject({ totalTasks: 9, completedTasks: 7, artifactCount: 12 });
    expect(insights.activityDays).toHaveLength(371);
    expect(insights.activityDays[0].tokenCount).toBe(24_800);
    expect(insights.topSkills[0]).toMatchObject({ name: 'docking', invocationCount: 8 });
    expect(fetchImpl).toHaveBeenCalledTimes(1);
    expect(fetchImpl.mock.calls[0][0]).toMatch(
      /^http:\/\/account\.test\/api\/account\/overview\?utc_offset_minutes=-?\d+$/
    );
    expect(fetchImpl.mock.calls[0][1]).toEqual(expect.objectContaining({ method: 'GET', credentials: 'include' }));
  });

  it('keeps an unavailable optional projection distinct from a real zero', () => {
    const payload = validOverview();
    payload.metrics.projectCount = null as unknown as number;
    payload.metrics.artifactCount = null as unknown as number;
    payload.availability.projects = false;
    payload.availability.skills = false;
    payload.topSkills = [];

    const insights = parseAccountOverview(payload);
    expect(insights.metrics.projectCount).toBeNull();
    expect(insights.metrics.artifactCount).toBeNull();
    expect(insights.availability).toEqual({ projects: false, skills: false, tokenUsage: true });
  });

  it('rejects truncated calendars and malformed metrics instead of rendering false totals', () => {
    const truncated = validOverview();
    truncated.activityDays = truncated.activityDays.slice(1);
    expect(() => parseAccountOverview(truncated)).toThrow('account overview response is invalid');

    const malformed = validOverview();
    malformed.metrics.totalTasks = -1;
    expect(() => parseAccountOverview(malformed)).toThrow('account overview response is invalid');
  });

  it('surfaces endpoint failures without falling back to client-side partial totals', async () => {
    const fetchImpl = vi.fn<typeof fetch>().mockResolvedValue(response({ message: 'unavailable' }, 503));
    await expect(loadSynonBiomedAccountInsights({ fetchImpl })).rejects.toThrow();
  });
});
