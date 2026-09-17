import { act, renderHook } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { IDirOrFile } from '@/common/adapter/ipcBridge';
import { useWorkspaceFileOps } from '@/renderer/pages/conversation/Workspace/hooks/useWorkspaceFileOps';
import { SYNON_BIOMED_TEXT_ACCEPT_HEADER } from '@/renderer/services/synonBiomedArtifactPreview';

const ipcMocks = vi.hoisted(() => ({
  readFile: vi.fn(),
  getImageBase64: vi.fn(),
  openFile: vi.fn(),
  showItemInFolder: vi.fn(),
}));
const downloadMocks = vi.hoisted(() => ({
  fromPath: vi.fn(),
  fromUrl: vi.fn(),
}));
const renameWorkspaceEntry = vi.fn();
const synonArtifactMocks = vi.hoisted(() => ({
  rename: vi.fn(),
  remove: vi.fn(),
}));
const composerReferenceMocks = vi.hoisted(() => ({ insert: vi.fn() }));
const emitterMocks = vi.hoisted(() => ({ emit: vi.fn() }));

vi.mock('@/common', () => ({
  ipcBridge: {
    fs: {
      readFile: { invoke: ipcMocks.readFile },
      getImageBase64: { invoke: ipcMocks.getImageBase64 },
    },
    shell: {
      openFile: { invoke: ipcMocks.openFile },
      showItemInFolder: { invoke: ipcMocks.showItemInFolder },
    },
  },
}));

vi.mock('@/renderer/utils/file/workspaceFs', () => ({
  removeWorkspaceEntry: vi.fn(),
  renameWorkspaceEntry: (...args: unknown[]) => renameWorkspaceEntry(...args),
}));

vi.mock('@/renderer/utils/file/download', () => ({
  downloadFileFromPath: downloadMocks.fromPath,
  downloadFileFromUrl: downloadMocks.fromUrl,
}));

vi.mock('@/renderer/components/chat/SendBox/composerReferenceBridge', () => ({
  insertArtifactReferenceIntoActiveComposer: composerReferenceMocks.insert,
}));

vi.mock('@/renderer/utils/emitter', () => ({
  emitter: { emit: emitterMocks.emit },
}));

vi.mock('@/renderer/services/synonBiomedWorkspaceApi', () => ({
  renameSynonBiomedArtifact: synonArtifactMocks.rename,
  deleteSynonBiomedArtifact: synonArtifactMocks.remove,
}));

describe('useWorkspaceFileOps rename', () => {
  beforeEach(() => {
    renameWorkspaceEntry.mockReset();
    renameWorkspaceEntry.mockResolvedValue({ new_path: '\\\\?\\D:\\CODE\\CLAUDE.md1' });
    synonArtifactMocks.rename.mockReset();
    synonArtifactMocks.rename.mockResolvedValue({ artifact_id: 'artifact-report', filename: 'renamed.md' });
    synonArtifactMocks.remove.mockReset();
    synonArtifactMocks.remove.mockResolvedValue({ deleted: true });
    composerReferenceMocks.insert.mockReset();
    composerReferenceMocks.insert.mockReturnValue(true);
    emitterMocks.emit.mockReset();
  });

  it('adds a remote artifact as an exact composer reference without a local attachment', () => {
    const artifact: IDirOrFile = {
      name: 'STAT6_report.md',
      fullPath: 'synonbiomed://proj_stat6/frame-stat6/artifact-report',
      relativePath: 'frame-stat6/artifact-report',
      isDir: false,
      isFile: true,
      readOnly: true,
      artifactId: 'artifact-report',
      versionId: 'version-report',
    };
    const messageApi = { success: vi.fn(), error: vi.fn(), warning: vi.fn() };

    const { result } = renderHook(() =>
      useWorkspaceFileOps({
        conversation_id: 'frame-stat6',
        workspace: 'synonbiomed://proj_stat6',
        eventPrefix: 'acp',
        messageApi,
        t: (key) => key,
        setSelected: vi.fn(),
        selectedKeysRef: { current: [] },
        selectedNodeRef: { current: null },
        ensureNodeSelected: vi.fn(),
        refreshWorkspace: vi.fn(),
        renameModal: { visible: false, value: '', target: null },
        deleteModal: { visible: false, target: null, loading: false },
        renameLoading: false,
        setRenameLoading: vi.fn(),
        closeRenameModal: vi.fn(),
        closeDeleteModal: vi.fn(),
        closeContextMenu: vi.fn(),
        setRenameModal: vi.fn(),
        setDeleteModal: vi.fn(),
        openPreview: vi.fn(),
      })
    );

    act(() => result.current.handleAddToChat(artifact));

    expect(composerReferenceMocks.insert).toHaveBeenCalledWith({
      filename: 'STAT6_report.md',
      artifactId: 'artifact-report',
      versionId: 'version-report',
      conversationId: 'frame-stat6',
    });
    expect(emitterMocks.emit).not.toHaveBeenCalled();
    expect(messageApi.success).toHaveBeenCalledWith('conversation.workspace.contextMenu.addedToChat');
  });

  it('renames a Synon Biomed artifact through the conversation-scoped backend contract', async () => {
    const target: IDirOrFile = {
      name: 'STAT6_report.md',
      fullPath: 'synonbiomed://proj_stat6/frame-stat6/artifact-report',
      relativePath: 'frame-stat6/artifact-report',
      isDir: false,
      isFile: true,
      readOnly: true,
      artifactId: 'artifact-report',
      canRename: true,
    };
    const refreshWorkspace = vi.fn();

    const { result } = renderHook(() =>
      useWorkspaceFileOps({
        conversation_id: 'frame-stat6',
        workspace: 'synonbiomed://proj_stat6',
        eventPrefix: 'acp',
        messageApi: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
        t: (key) => key,
        setSelected: vi.fn(),
        selectedKeysRef: { current: [] },
        selectedNodeRef: { current: null },
        ensureNodeSelected: vi.fn(),
        refreshWorkspace,
        renameModal: { visible: true, value: 'renamed.md', target },
        deleteModal: { visible: false, target: null, loading: false },
        renameLoading: false,
        setRenameLoading: vi.fn(),
        closeRenameModal: vi.fn(),
        closeDeleteModal: vi.fn(),
        closeContextMenu: vi.fn(),
        setRenameModal: vi.fn(),
        setDeleteModal: vi.fn(),
        openPreview: vi.fn(),
      })
    );

    await act(async () => {
      await result.current.handleRenameConfirm();
    });

    expect(synonArtifactMocks.rename).toHaveBeenCalledWith({
      conversationId: 'frame-stat6',
      artifactId: 'artifact-report',
      filename: 'renamed.md',
    });
    expect(renameWorkspaceEntry).not.toHaveBeenCalled();
    expect(refreshWorkspace).toHaveBeenCalledTimes(1);
  });

  it('deletes a Synon Biomed artifact through the conversation-scoped backend contract', async () => {
    const target: IDirOrFile = {
      name: 'STAT6_report.md',
      fullPath: 'synonbiomed://proj_stat6/frame-stat6/artifact-report',
      relativePath: 'frame-stat6/artifact-report',
      isDir: false,
      isFile: true,
      readOnly: true,
      artifactId: 'artifact-report',
      canDelete: true,
    };
    const refreshWorkspace = vi.fn();

    const { result } = renderHook(() =>
      useWorkspaceFileOps({
        conversation_id: 'frame-stat6',
        workspace: 'synonbiomed://proj_stat6',
        eventPrefix: 'acp',
        messageApi: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
        t: (key) => key,
        setSelected: vi.fn(),
        selectedKeysRef: { current: [] },
        selectedNodeRef: { current: null },
        ensureNodeSelected: vi.fn(),
        refreshWorkspace,
        renameModal: { visible: false, value: '', target: null },
        deleteModal: { visible: true, target, loading: false },
        renameLoading: false,
        setRenameLoading: vi.fn(),
        closeRenameModal: vi.fn(),
        closeDeleteModal: vi.fn(),
        closeContextMenu: vi.fn(),
        setRenameModal: vi.fn(),
        setDeleteModal: vi.fn(),
        openPreview: vi.fn(),
      })
    );

    await act(async () => {
      await result.current.handleDeleteConfirm();
    });

    expect(synonArtifactMocks.remove).toHaveBeenCalledWith({
      conversationId: 'frame-stat6',
      artifactId: 'artifact-report',
    });
    await act(async () => {
      await new Promise((resolve) => window.setTimeout(resolve, 220));
    });
    expect(refreshWorkspace).toHaveBeenCalledTimes(1);
  });

  it('previews a read-only Synon Biomed markdown artifact from its real content URL', async () => {
    const artifact: IDirOrFile = {
      name: 'STAT6_report.md',
      fullPath: 'synonbiomed://proj_stat6/frame-stat6/artifact-report',
      relativePath: 'frame-stat6/artifact-report',
      isDir: false,
      isFile: true,
      readOnly: true,
      artifactId: 'artifact-report',
      versionId: 'version-report',
      contentType: 'text/markdown',
      sizeBytes: 4096,
      contentUrl: '/api/artifacts/artifact-report',
    };
    const openPreview = vi.fn();
    const fetchMock = vi.fn().mockResolvedValue(
      new Response('# STAT6 report', {
        status: 200,
        headers: { 'content-type': 'text/markdown' },
      })
    );
    vi.stubGlobal('fetch', fetchMock);

    const { result } = renderHook(() =>
      useWorkspaceFileOps({
        conversation_id: 'frame-stat6',
        workspace: 'synonbiomed://proj_stat6',
        eventPrefix: 'acp',
        messageApi: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
        t: (key) => key,
        setSelected: vi.fn(),
        selectedKeysRef: { current: [] },
        selectedNodeRef: { current: null },
        ensureNodeSelected: vi.fn(),
        refreshWorkspace: vi.fn(),
        renameModal: { visible: false, value: '', target: null },
        deleteModal: { visible: false, target: null, loading: false },
        renameLoading: false,
        setRenameLoading: vi.fn(),
        closeRenameModal: vi.fn(),
        closeDeleteModal: vi.fn(),
        closeContextMenu: vi.fn(),
        setRenameModal: vi.fn(),
        setDeleteModal: vi.fn(),
        openPreview,
      })
    );

    await act(async () => {
      await result.current.handlePreviewFile(artifact, [
        artifact,
        {
          ...artifact,
          name: 'binding.png',
          artifactId: 'artifact-binding',
          versionId: 'version-binding',
          contentType: 'image/png',
          contentUrl: '/api/artifacts/artifact-binding',
        },
      ]);
    });

    expect(fetchMock).toHaveBeenCalledWith('/api/artifacts/artifact-report', {
      headers: { accept: SYNON_BIOMED_TEXT_ACCEPT_HEADER },
    });
    expect(ipcMocks.readFile).not.toHaveBeenCalled();
    expect(openPreview).toHaveBeenCalledWith(
      '# STAT6 report',
      'markdown',
      expect.objectContaining({
        title: 'STAT6_report.md',
        file_name: 'STAT6_report.md',
        artifactId: 'artifact-report',
        contentUrl: '/api/artifacts/artifact-report',
        rootFrameId: 'frame-stat6',
        editable: true,
        companionArtifactUrls: {
          'stat6_report.md': '/api/artifacts/artifact-report',
          'binding.png': '/api/artifacts/artifact-binding',
        },
      }),
      { presentation: 'board' }
    );
  });
  it('downloads a read-only Synon Biomed artifact from its content URL', async () => {
    const artifact: IDirOrFile = {
      name: 'STAT6_report.md',
      fullPath: 'synonbiomed://proj_stat6/frame-stat6/artifact-report',
      relativePath: 'frame-stat6/artifact-report',
      isDir: false,
      isFile: true,
      readOnly: true,
      artifactId: 'artifact-report',
      contentUrl: '/api/artifacts/artifact-report',
    };
    const { result } = renderHook(() =>
      useWorkspaceFileOps({
        workspace: 'synonbiomed://proj_stat6',
        eventPrefix: 'acp',
        messageApi: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
        t: (key) => key,
        setSelected: vi.fn(),
        selectedKeysRef: { current: [] },
        selectedNodeRef: { current: null },
        ensureNodeSelected: vi.fn(),
        refreshWorkspace: vi.fn(),
        renameModal: { visible: false, value: '', target: null },
        deleteModal: { visible: false, target: null, loading: false },
        renameLoading: false,
        setRenameLoading: vi.fn(),
        closeRenameModal: vi.fn(),
        closeDeleteModal: vi.fn(),
        closeContextMenu: vi.fn(),
        setRenameModal: vi.fn(),
        setDeleteModal: vi.fn(),
        openPreview: vi.fn(),
      })
    );

    await act(async () => {
      await result.current.handleDownloadFile(artifact);
    });

    expect(downloadMocks.fromUrl).toHaveBeenCalledWith('/api/artifacts/artifact-report', 'STAT6_report.md');
    expect(downloadMocks.fromPath).not.toHaveBeenCalled();
  });

  it('previews a remote artifact even when it has no local filesystem path', async () => {
    const artifact: IDirOrFile = {
      name: 'events.jsonl',
      fullPath: '',
      relativePath: 'project-files/events.jsonl',
      isDir: false,
      isFile: true,
      artifactId: 'artifact-events',
      versionId: 'version-events',
      contentType: 'application/octet-stream',
      contentUrl: '/api/artifacts/artifact-events',
    };
    const openPreview = vi.fn();
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{"id":1}\n', { status: 200 })));
    const { result } = renderHook(() =>
      useWorkspaceFileOps({
        workspace: 'synonbiomed://proj_stat6',
        eventPrefix: 'acp',
        messageApi: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
        t: (key) => key,
        setSelected: vi.fn(),
        selectedKeysRef: { current: [] },
        selectedNodeRef: { current: null },
        ensureNodeSelected: vi.fn(),
        refreshWorkspace: vi.fn(),
        renameModal: { visible: false, value: '', target: null },
        deleteModal: { visible: false, target: null, loading: false },
        renameLoading: false,
        setRenameLoading: vi.fn(),
        closeRenameModal: vi.fn(),
        closeDeleteModal: vi.fn(),
        closeContextMenu: vi.fn(),
        setRenameModal: vi.fn(),
        setDeleteModal: vi.fn(),
        openPreview,
      })
    );

    await act(async () => result.current.handlePreviewFile(artifact));

    expect(openPreview).toHaveBeenCalledWith(
      '{"id":1}\n',
      'code',
      expect.objectContaining({ artifactId: 'artifact-events', language: 'jsonl' }),
      { presentation: 'board' }
    );
  });
  it('refreshes workspace after rename instead of patching local tree paths', async () => {
    const target: IDirOrFile = {
      name: 'CLAUDE.md',
      fullPath: 'D:\\CODE\\CLAUDE.md',
      relativePath: 'CLAUDE.md',
      isDir: false,
      isFile: true,
    };
    const refreshWorkspace = vi.fn();
    const setSelected = vi.fn();
    const selectedKeysRef = { current: ['CLAUDE.md'] };
    const selectedNodeRef: { current: { relativePath: string; fullPath: string } | null } = {
      current: { relativePath: 'CLAUDE.md', fullPath: 'D:\\CODE\\CLAUDE.md' },
    };

    const { result } = renderHook(() =>
      useWorkspaceFileOps({
        workspace: 'D:\\CODE',
        eventPrefix: 'acp',
        messageApi: {
          success: vi.fn(),
          error: vi.fn(),
          warning: vi.fn(),
        },
        t: (key) => key,
        setSelected,
        selectedKeysRef,
        selectedNodeRef,
        ensureNodeSelected: vi.fn(),
        refreshWorkspace,
        renameModal: {
          visible: true,
          value: 'CLAUDE.md1',
          target,
        },
        deleteModal: { visible: false, target: null, loading: false },
        renameLoading: false,
        setRenameLoading: vi.fn(),
        closeRenameModal: vi.fn(),
        closeDeleteModal: vi.fn(),
        closeContextMenu: vi.fn(),
        setRenameModal: vi.fn(),
        setDeleteModal: vi.fn(),
        openPreview: vi.fn(),
      })
    );

    await act(async () => {
      await result.current.handleRenameConfirm();
    });

    expect(renameWorkspaceEntry).toHaveBeenCalledWith('D:\\CODE\\CLAUDE.md', 'CLAUDE.md1', 'D:\\CODE');
    expect(refreshWorkspace).toHaveBeenCalledTimes(1);
    expect(setSelected).toHaveBeenCalledWith([]);
    expect(selectedKeysRef.current).toEqual([]);
    expect(selectedNodeRef.current).toBeNull();
  });
});
