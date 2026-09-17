import { describe, expect, it } from 'vitest';
import type { SynonBiomedProjectBench } from '@/renderer/services/synonBiomedGateway';
import {
  resolveLatestProjectConversationId,
  resolveProjectNavigationTarget,
  resolveProjectSummaryNavigationTarget,
} from '@/renderer/pages/project/projectNavigationModel';

const bench = (overrides: Partial<SynonBiomedProjectBench>): SynonBiomedProjectBench => ({
  frameId: 'frame-default',
  rootFrameId: 'frame-default',
  parentFrameId: null,
  projectId: 'project-a',
  name: 'Task',
  taskSummary: null,
  agentName: null,
  status: null,
  statusDescription: null,
  createdAt: null,
  updatedAt: null,
  completedAt: null,
  lastActivityAt: null,
  hasImageOutput: false,
  ...overrides,
});

describe('resolveLatestProjectConversationId', () => {
  it('opens the latest conversation directly from the bounded project summary', () => {
    expect(
      resolveProjectSummaryNavigationTarget({
        projectId: 'project a',
        latestConversationId: 'frame/latest',
        name: 'Project A',
        description: null,
        context: null,
        conversationCount: 9,
        artifactCount: 2,
        createdAt: null,
        updatedAt: null,
        lastActiveAt: null,
      })
    ).toEqual({ pathname: '/conversation/frame%2Flatest' });
  });

  it('keeps a legacy non-empty summary on the authoritative bench fallback', () => {
    expect(
      resolveProjectSummaryNavigationTarget({
        projectId: 'project-a',
        latestConversationId: null,
        name: 'Project A',
        description: null,
        context: null,
        conversationCount: 3,
        artifactCount: 0,
        createdAt: null,
        updatedAt: null,
        lastActiveAt: null,
      })
    ).toBeNull();
  });

  it('selects the root conversation with the most recent activity', () => {
    expect(
      resolveLatestProjectConversationId([
        bench({ frameId: 'older', rootFrameId: 'older', updatedAt: '2026-07-10T10:00:00Z' }),
        bench({ frameId: 'newer', rootFrameId: 'newer', lastActivityAt: '2026-07-12T10:00:00Z' }),
      ])
    ).toBe('newer');
  });

  it('attributes child-frame activity to its root task', () => {
    expect(
      resolveLatestProjectConversationId([
        bench({ frameId: 'root-a', rootFrameId: 'root-a', updatedAt: '2026-07-10T10:00:00Z' }),
        bench({
          frameId: 'child-a',
          rootFrameId: 'root-a',
          parentFrameId: 'root-a',
          updatedAt: '2026-07-13T10:00:00Z',
        }),
        bench({ frameId: 'root-b', rootFrameId: 'root-b', updatedAt: '2026-07-12T10:00:00Z' }),
      ])
    ).toBe('root-a');
  });

  it('returns null for a project without tasks', () => {
    expect(resolveLatestProjectConversationId([])).toBeNull();
  });

  it('builds a direct route to the latest conversation', () => {
    expect(
      resolveProjectNavigationTarget('project a', [
        bench({ frameId: 'older', rootFrameId: 'older', updatedAt: '2026-07-10T10:00:00Z' }),
        bench({ frameId: 'latest/task', rootFrameId: 'latest/task', updatedAt: '2026-07-12T10:00:00Z' }),
      ])
    ).toEqual({ pathname: '/conversation/latest%2Ftask' });
  });

  it('builds a project-bound new-task route when the project has no conversations', () => {
    expect(resolveProjectNavigationTarget('project a', [])).toEqual({
      pathname: '/guid',
      state: {
        resetAssistant: true,
        workspace: 'synonbiomed://project/project%20a',
      },
    });
  });
});
