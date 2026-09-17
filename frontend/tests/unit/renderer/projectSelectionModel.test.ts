import { describe, expect, it } from 'vitest';

import {
  resolveActiveProjectId,
  resolveMostRecentProjectId,
} from '@/renderer/pages/conversation/GroupedHistory/projectSelectionModel';

describe('project selection model', () => {
  const availableProjectIds = ['proj_recent', 'proj_older'];

  it('prefers route and conversation context over persisted selection', () => {
    expect(
      resolveActiveProjectId({
        routeProjectId: 'proj_older',
        conversationProjectId: 'proj_recent',
        preferredProjectId: 'proj_recent',
        availableProjectIds,
      })
    ).toBe('proj_older');

    expect(
      resolveActiveProjectId({
        conversationProjectId: 'proj_older',
        preferredProjectId: 'proj_recent',
        availableProjectIds,
      })
    ).toBe('proj_older');
  });

  it('restores a valid persisted project and otherwise selects the first backend project', () => {
    expect(resolveActiveProjectId({ preferredProjectId: 'proj_older', availableProjectIds })).toBe('proj_older');
    expect(resolveActiveProjectId({ preferredProjectId: 'proj_missing', availableProjectIds })).toBe('proj_recent');
  });

  it('returns null when the backend has no projects', () => {
    expect(resolveActiveProjectId({ availableProjectIds: [] })).toBeNull();
  });

  it('selects the most recently active project independently of sidebar order', () => {
    expect(
      resolveMostRecentProjectId([
        {
          projectId: 'proj_pinned_first',
          createdAt: '2026-07-01T00:00:00Z',
          updatedAt: '2026-07-20T00:00:00Z',
          lastActiveAt: '2026-07-21T00:00:00Z',
        },
        {
          projectId: 'proj_latest',
          createdAt: '2026-07-02T00:00:00Z',
          updatedAt: '2026-07-30T00:00:00Z',
          lastActiveAt: '2026-08-03T00:00:00Z',
        },
      ])
    ).toBe('proj_latest');
  });
});
