import { act, renderHook, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

const listLocalHistoryMock = vi.fn();
const saveLocalHistoryMock = vi.fn();
const getLocalHistoryContentMock = vi.fn();
const loadArtifactVersionsMock = vi.fn(async () => [
  {
    versionId: 'version-2',
    versionNumber: 2,
    artifactId: 'artifact-1',
    frameId: 'frame-1',
    agentName: 'OPERON',
    language: 'markdown',
    contentType: 'text/markdown',
    sizeBytes: 128,
    createdAt: '2026-07-11T02:00:00.000Z',
    filePath: '/state/v2-report.md',
    parentVersionId: 'version-1',
  },
]);
const loadVersionTextMock = vi.fn(async () => '# version 2');
const createArtifactVersionMock = vi.fn(async () => ({
  artifactId: 'artifact-1',
  versionId: 'version-3',
  filename: 'report.md',
  folderId: null,
  raw: {},
}));
const messageMocks = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }));
const translateMock = vi.hoisted(
  () => (key: string, options?: { version?: number }) =>
    key === 'preview.versionLabel' ? `版本 ${options?.version ?? ''}` : key
);

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: translateMock }),
}));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const original = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...original,
    Message: {
      ...original.Message,
      useMessage: () => [messageMocks, null],
    },
  };
});

vi.mock('@/common', () => ({
  ipcBridge: {
    previewHistory: {
      list: { invoke: (...args: unknown[]) => listLocalHistoryMock(...args) },
      save: { invoke: (...args: unknown[]) => saveLocalHistoryMock(...args) },
      getContent: { invoke: (...args: unknown[]) => getLocalHistoryContentMock(...args) },
    },
  },
}));

vi.mock('@/renderer/services/synonBiomedArtifacts', () => ({
  loadSynonBiomedArtifactVersions: (...args: unknown[]) => loadArtifactVersionsMock(...args),
  loadSynonBiomedArtifactVersionText: (...args: unknown[]) => loadVersionTextMock(...args),
  getSynonBiomedArtifactVersionContentUrl: (versionId: string) => `/api/artifacts/versions/${versionId}`,
  createSynonBiomedTextArtifactVersion: (...args: unknown[]) => createArtifactVersionMock(...args),
}));

import { usePreviewHistory } from '@/renderer/pages/conversation/Preview/hooks/usePreviewHistory';

describe('Synon Biomed preview history integration', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('loads and selects backend artifact versions through the existing SynonAI preview history hook', async () => {
    const updateContent = vi.fn();
    const markContentSaved = vi.fn();
    const activeTab = {
      content_type: 'markdown',
      content: '# current',
      title: 'report.md',
      metadata: {
        file_name: 'report.md',
        artifactId: 'artifact-1',
        versionId: 'version-1',
        contentUrl: '/api/artifacts/artifact-1',
      },
    };
    const { result } = renderHook(() =>
      usePreviewHistory({
        activeTab,
        updateContent,
        markContentSaved,
      })
    );

    await waitFor(() => expect(result.current.historyVersions).toHaveLength(1));
    expect(loadArtifactVersionsMock).toHaveBeenCalledWith('artifact-1');
    expect(listLocalHistoryMock).not.toHaveBeenCalled();
    expect(result.current.historyVersions[0]).toMatchObject({
      id: 'version-2',
      label: '版本 2',
      file_path: '/api/artifacts/versions/version-2',
    });

    await act(async () => {
      await result.current.handleSnapshotSelect(result.current.historyVersions[0]!);
    });

    expect(loadVersionTextMock).toHaveBeenCalledWith('version-2');
    expect(updateContent).toHaveBeenCalledWith('# version 2');
    expect(markContentSaved).toHaveBeenCalledWith({
      versionId: 'version-2',
      contentUrl: '/api/artifacts/versions/version-2',
    });
    expect(getLocalHistoryContentMock).not.toHaveBeenCalled();
  });

  it('saves edited artifact text as a new version and advances the active version authority', async () => {
    const markContentSaved = vi.fn();
    const activeTab = {
      content_type: 'markdown',
      content: '# edited report',
      title: 'report.md',
      metadata: {
        file_name: 'report.md',
        artifactId: 'artifact-1',
        versionId: 'version-2',
        contentUrl: '/api/artifacts/versions/version-2',
      },
    };
    const { result } = renderHook(() =>
      usePreviewHistory({
        activeTab,
        updateContent: vi.fn(),
        markContentSaved,
      })
    );

    await waitFor(() => expect(result.current.historyVersions).toHaveLength(1));
    let saved = false;
    await act(async () => {
      saved = await result.current.handleSaveSnapshot();
    });

    expect(saved).toBe(true);
    expect(createArtifactVersionMock).toHaveBeenCalledWith({
      artifactId: 'artifact-1',
      content: '# edited report',
      contentType: 'text/markdown',
      parentVersionId: 'version-2',
    });
    expect(markContentSaved).toHaveBeenCalledWith({
      versionId: 'version-3',
      contentUrl: '/api/artifacts/versions/version-3',
    });
  });
});
