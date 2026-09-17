import { act, renderHook, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { BackendHttpError } from '@/common/adapter/httpBridge';

const mocks = vi.hoisted(() => ({
  getWorkspace: vi.fn(),
  getWorkspacePage: vi.fn(),
  dispatchEvent: vi.fn(),
  authUser: { id: 'owner-a', username: 'victor' } as { id: string; username: string } | undefined,
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    conversation: {
      getWorkspace: { invoke: mocks.getWorkspace },
      getWorkspacePage: { invoke: mocks.getWorkspacePage },
    },
  },
}));

vi.mock('@/renderer/utils/workspace/workspaceEvents', () => ({
  dispatchWorkspaceHasFilesEvent: mocks.dispatchEvent,
}));

vi.mock('@/renderer/utils/emitter', () => ({
  emitter: { emit: vi.fn() },
}));

vi.mock('@/renderer/hooks/context/AuthContext', () => ({
  useAuth: () => ({ user: mocks.authUser }),
}));

import { useWorkspaceTree } from '@/renderer/pages/conversation/Workspace/hooks/useWorkspaceTree';

describe('useWorkspaceTree initial loading', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.getWorkspace.mockReset();
    mocks.getWorkspacePage.mockReset();
    mocks.authUser = { id: 'owner-a', username: 'victor' };
  });

  it('loads the canonical project artifact collection directly for Synon Biomed', async () => {
    const response = [
      {
        name: 'project-files',
        fullPath: 'synonbiomed://project-1/project-files',
        relativePath: 'project-files',
        isDir: true,
        isFile: false,
        children: [
          {
            name: 'task-1',
            fullPath: 'synonbiomed://project-1/project-files/report.md',
            relativePath: 'project-files/report.md',
            isDir: false,
            isFile: true,
            artifactId: 'artifact-report',
          },
        ],
      },
    ];
    mocks.getWorkspacePage.mockResolvedValue({ items: response, total: 1, hasMore: false });
    const { result } = renderHook(() =>
      useWorkspaceTree({
        workspace: 'synonbiomed://project-1',
        conversation_id: 'conversation-1',
        projectId: 'project-1',
        eventPrefix: 'acp',
      })
    );

    await act(async () => result.current.ensureWorkspace());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(mocks.getWorkspacePage).toHaveBeenCalledTimes(1);
    expect(mocks.getWorkspacePage).toHaveBeenCalledWith(
      {
        path: 'synonbiomed://project-1/project-files',
        workspace: 'synonbiomed://project-1',
        conversation_id: 'conversation-1',
        search: '',
        limit: 24,
      },
      expect.any(AbortSignal)
    );
    expect(result.current.files).toEqual(response);
    expect(result.current.expandedKeys).toEqual([]);
  });

  it('keeps ordinary workspaces on the generic root contract', async () => {
    mocks.getWorkspace.mockResolvedValue([]);
    const { result } = renderHook(() =>
      useWorkspaceTree({ workspace: '/workspace/project-1', conversation_id: 'conversation-1', eventPrefix: 'acp' })
    );
    await act(async () => {
      await result.current.loadWorkspace('/workspace/project-1');
    });
    expect(mocks.getWorkspace).toHaveBeenCalledWith(
      {
        path: '/workspace/project-1',
        workspace: '/workspace/project-1',
        conversation_id: 'conversation-1',
        search: '',
      },
      expect.any(AbortSignal)
    );
  });

  it('keeps project artifact pages isolated when switching conversations', async () => {
    const treeA = [
      {
        name: 'project-files',
        fullPath: 'synonbiomed://project-cache/project-files',
        relativePath: 'project-files',
        isDir: true,
        isFile: false,
        children: [],
      },
    ];
    const treeB = [
      {
        ...treeA[0],
        fullPath: 'synonbiomed://project-cache/project-files',
        children: [
          {
            name: 'conversation-b.csv',
            fullPath: 'synonbiomed://project-cache/project-files/conversation-b.csv',
            relativePath: 'project-files/conversation-b.csv',
            isDir: false,
            isFile: true,
            artifactId: 'artifact-b',
          },
        ],
      },
    ];
    mocks.getWorkspacePage
      .mockResolvedValueOnce({ items: treeA, total: 0, hasMore: false })
      .mockResolvedValueOnce({ items: treeB, total: 1, hasMore: false });
    const rendered = renderHook(
      ({ conversation }) =>
        useWorkspaceTree({
          workspace: 'synonbiomed://project-cache',
          conversation_id: conversation,
          projectId: 'project-cache',
          eventPrefix: 'acp',
        }),
      { initialProps: { conversation: 'conversation-cache-a' } }
    );

    await act(async () => rendered.result.current.ensureWorkspace());
    expect(rendered.result.current.files).toEqual(treeA);
    rendered.rerender({ conversation: 'conversation-cache-b' });
    await waitFor(() => expect(rendered.result.current.files).toEqual([]));
    await act(async () => rendered.result.current.ensureWorkspace());
    expect(rendered.result.current.files).toEqual(treeB);
    rendered.rerender({ conversation: 'conversation-cache-a' });
    await act(async () => rendered.result.current.ensureWorkspace());

    expect(rendered.result.current.files).toEqual(treeA);
    expect(mocks.getWorkspacePage).toHaveBeenCalledTimes(2);
    expect(mocks.getWorkspacePage).toHaveBeenNthCalledWith(
      2,
      {
        path: 'synonbiomed://project-cache/project-files',
        workspace: 'synonbiomed://project-cache',
        conversation_id: 'conversation-cache-b',
        search: '',
        limit: 24,
      },
      expect.any(AbortSignal)
    );
  });

  it('reloads the same project for a different conversation', async () => {
    mocks.getWorkspacePage.mockImplementation(async ({ workspace }: { workspace: string }) => ({
      items: [
        {
          name: 'project-files',
          fullPath: `${workspace}/project-files`,
          relativePath: 'project-files',
          isDir: true,
          isFile: false,
          children: [],
        },
      ],
      total: 0,
      hasMore: false,
    }));
    const rendered = renderHook(
      ({ workspace, projectId, conversation }) =>
        useWorkspaceTree({ workspace, projectId, conversation_id: conversation, eventPrefix: 'acp' }),
      {
        initialProps: {
          workspace: 'synonbiomed://project-switch-a',
          projectId: 'project-switch-a',
          conversation: 'conversation-switch-a',
        },
      }
    );

    await act(async () => rendered.result.current.ensureWorkspace());
    rendered.rerender({
      workspace: 'synonbiomed://project-switch-a',
      projectId: 'project-switch-a',
      conversation: 'conversation-switch-b',
    });
    await act(async () => rendered.result.current.ensureWorkspace());
    expect(mocks.getWorkspacePage).toHaveBeenCalledTimes(2);

    rendered.rerender({
      workspace: 'synonbiomed://project-switch-b',
      projectId: 'project-switch-b',
      conversation: 'conversation-switch-c',
    });
    await act(async () => rendered.result.current.ensureWorkspace());
    expect(mocks.getWorkspacePage).toHaveBeenLastCalledWith(
      {
        path: 'synonbiomed://project-switch-b/project-files',
        workspace: 'synonbiomed://project-switch-b',
        conversation_id: 'conversation-switch-c',
        search: '',
        limit: 24,
      },
      expect.any(AbortSignal)
    );
  });

  it('does not request or cache a project before the authenticated owner is known', async () => {
    mocks.authUser = undefined;
    const rendered = renderHook(
      ({ revision }) => {
        void revision;
        return useWorkspaceTree({
          workspace: 'synonbiomed://project-auth',
          projectId: 'project-auth',
          conversation_id: 'conversation-auth',
          eventPrefix: 'acp',
        });
      },
      { initialProps: { revision: 0 } }
    );

    await act(async () => rendered.result.current.ensureWorkspace());
    expect(mocks.getWorkspacePage).not.toHaveBeenCalled();
    expect(rendered.result.current.files).toEqual([]);

    mocks.authUser = { id: 'owner-a', username: 'victor' };
    mocks.getWorkspacePage.mockResolvedValue({ items: [], total: 0, hasMore: false });
    rendered.rerender({ revision: 1 });
    await act(async () => rendered.result.current.ensureWorkspace());
    expect(mocks.getWorkspacePage).toHaveBeenCalledTimes(1);
  });

  it('atomically accepts an authoritative empty project refresh instead of restoring deleted artifacts', async () => {
    const populated = [
      {
        name: 'project-files',
        fullPath: 'synonbiomed://project-empty/project-files',
        relativePath: 'project-files',
        isDir: true,
        isFile: false,
        children: [
          {
            name: 'old.md',
            fullPath: 'synonbiomed://project-empty/project-files/old.md',
            relativePath: 'project-files/old.md',
            isDir: false,
            isFile: true,
          },
        ],
      },
    ];
    const empty = [{ ...populated[0], children: [] }];
    mocks.getWorkspacePage
      .mockResolvedValueOnce({ items: populated, total: 1, hasMore: false })
      .mockResolvedValueOnce({ items: empty, total: 0, hasMore: false });
    const rendered = renderHook(() =>
      useWorkspaceTree({
        workspace: 'synonbiomed://project-empty',
        projectId: 'project-empty',
        conversation_id: 'conversation-empty',
        eventPrefix: 'acp',
      })
    );

    await act(async () => rendered.result.current.ensureWorkspace());
    expect(rendered.result.current.files[0]?.children).toHaveLength(1);
    await act(async () => rendered.result.current.refreshWorkspace());
    expect(rendered.result.current.files).toEqual(empty);
  });

  it('pages and searches a 100k project without truncating or retaining the previous query', async () => {
    const page = (names: string[]) => [
      {
        name: 'project-files',
        fullPath: 'synonbiomed://project-large/project-files',
        relativePath: 'project-files',
        isDir: true,
        isFile: false,
        children: names.map((name) => ({
          name,
          fullPath: `synonbiomed://project-large/project-files/${name}`,
          relativePath: `project-files/${name}`,
          isDir: false,
          isFile: true,
          artifactId: `artifact-${name}`,
          versionId: `version-${name}`,
        })),
      },
    ];
    mocks.getWorkspacePage
      .mockResolvedValueOnce({ items: page(['a.dat', 'b.dat']), total: 100_000, hasMore: true, nextCursor: 'cursor-2' })
      .mockResolvedValueOnce({ items: page(['c.dat', 'd.dat']), total: 100_000, hasMore: false })
      .mockResolvedValueOnce({ items: page(['needle.dat']), total: 1, hasMore: false });
    const rendered = renderHook(() =>
      useWorkspaceTree({
        workspace: 'synonbiomed://project-large',
        projectId: 'project-large',
        conversation_id: 'conversation-large',
        eventPrefix: 'acp',
      })
    );

    await act(async () => rendered.result.current.ensureWorkspace());
    expect(rendered.result.current.projectArtifactPage.total).toBe(100_000);
    expect(rendered.result.current.files[0]?.children?.map((item) => item.name)).toEqual(['a.dat', 'b.dat']);

    await act(async () => expect(await rendered.result.current.loadMoreWorkspace()).toBe(true));
    expect(mocks.getWorkspacePage).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ cursor: 'cursor-2', limit: 24, search: '' }),
      expect.any(AbortSignal)
    );
    expect(rendered.result.current.files[0]?.children?.map((item) => item.name)).toEqual([
      'a.dat',
      'b.dat',
      'c.dat',
      'd.dat',
    ]);

    await act(async () => rendered.result.current.searchWorkspace('needle'));
    expect(mocks.getWorkspacePage).toHaveBeenNthCalledWith(
      3,
      expect.objectContaining({ search: 'needle', limit: 24 }),
      expect.any(AbortSignal)
    );
    expect(rendered.result.current.projectArtifactPage.total).toBe(1);
    expect(rendered.result.current.files[0]?.children?.map((item) => item.name)).toEqual(['needle.dat']);
  });

  it('recovers a stale project cursor once from the authoritative first page', async () => {
    const page = (name: string) => [
      {
        name: 'project-files',
        fullPath: 'synonbiomed://project-stale/project-files',
        relativePath: 'project-files',
        isDir: true,
        isFile: false,
        children: [
          {
            name,
            fullPath: `synonbiomed://project-stale/project-files/${name}`,
            relativePath: `project-files/${name}`,
            isDir: false,
            isFile: true,
            artifactId: `artifact-${name}`,
            versionId: `version-${name}`,
          },
        ],
      },
    ];
    mocks.getWorkspacePage
      .mockResolvedValueOnce({ items: page('old.dat'), total: 2, hasMore: true, nextCursor: 'stale-cursor' })
      .mockRejectedValueOnce(
        new BackendHttpError({
          method: 'GET',
          path: '/api/conversations/conversation-stale/workspace',
          status: 409,
          body: { message: 'workspace artifact index changed; refresh and retry' },
        })
      )
      .mockResolvedValueOnce({ items: page('current.dat'), total: 1, hasMore: false });
    const rendered = renderHook(() =>
      useWorkspaceTree({
        workspace: 'synonbiomed://project-stale',
        projectId: 'project-stale',
        conversation_id: 'conversation-stale',
        eventPrefix: 'acp',
      })
    );

    await act(async () => rendered.result.current.ensureWorkspace());
    await act(async () => expect(await rendered.result.current.loadMoreWorkspace()).toBe(true));

    expect(mocks.getWorkspacePage).toHaveBeenCalledTimes(3);
    expect(mocks.getWorkspacePage).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ cursor: 'stale-cursor' }),
      expect.any(AbortSignal)
    );
    expect(mocks.getWorkspacePage).toHaveBeenNthCalledWith(
      3,
      expect.not.objectContaining({ cursor: expect.anything() }),
      expect.any(AbortSignal)
    );
    expect(rendered.result.current.files[0]?.children?.map((item) => item.name)).toEqual(['current.dat']);
    expect(rendered.result.current.projectArtifactPage).toMatchObject({
      hasMore: false,
      loadingMore: false,
      loadMoreError: false,
    });
  });
});
