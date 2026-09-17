import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const render = (ui: React.ReactElement) => renderWithI18n(ui);

afterEach(() => vi.restoreAllMocks());

const loadArtifactMock = vi.fn(async () => ({
  artifactId: 'artifact-1',
  versionId: 'version-1',
  versionNumber: 1,
  projectId: 'proj_example',
  rootFrameId: 'frame-1',
  frameId: 'frame-1',
  filename: 'qc_metrics.png',
  contentType: 'image/png',
  sizeBytes: 2048,
  createdAt: null,
  updatedAt: null,
  isUserUpload: false,
  agentName: 'OPERON',
  isIntermediate: false,
  creatingFrameId: 'frame-1',
  checksum: 'sha256-example',
  filePath: '/state/artifacts/qc_metrics.png',
  folderId: 'folder-1',
  priority: 'normal',
}));

const loadVersionsMock = vi.fn(async () => [
  {
    versionId: 'version-1',
    versionNumber: 1,
    artifactId: 'artifact-1',
    frameId: 'frame-1',
    agentName: 'OPERON',
    language: null,
    contentType: 'image/png',
    sizeBytes: 2048,
    createdAt: '2026-07-11T01:00:00.000Z',
    filePath: '/state/artifacts/qc_metrics.png',
    parentVersionId: null,
  },
]);

const loadLineageMock = vi.fn(async () => ({
  artifactId: 'artifact-1',
  versionId: 'version-1',
  versionNumber: 1,
  filename: 'qc_metrics.png',
  code: null,
  codeDescription: '由质量控制流程生成',
  messages: null,
  environmentSnapshot: { python: '3.12' },
  language: null,
  interactions: null,
  hasCellSources: false,
  hasMessages: false,
  hasEnvironment: true,
  pending: false,
  dependencyMappings: null,
}));

const copyArtifactMock = vi.fn(async () => ({
  artifactId: 'artifact-copy',
  versionId: 'version-copy',
  filename: 'qc_metrics-copy.png',
}));

const moveArtifactMock = vi.fn(async () => ({ artifactId: 'artifact-1', folderId: 'folder-2' }));
const exportArtifactMock = vi.fn(async () => ({ exported: true }));
const loadCloudBucketsMock = vi.fn(async () => ['bucket-a']);
const loadStorageMock = vi.fn(async () => ({
  dataDirectory: {
    current: '/data',
    resolved: null,
    defaultPath: '/data',
    source: 'flag',
    usageBytes: 1,
    freeBytes: 2,
    activeFrames: 0,
  },
  diskUsage: { artifactsBytes: 1, workspaceBytes: 1, toolResultsBytes: 1, condaBytes: 1, availableBytes: 2 },
  cloudCredentials: [
    {
      id: 'cloud-1',
      provider: 's3',
      name: 'Research',
      credentialType: 'access_key',
      connected: true,
      defaultBucket: 'bucket-a',
      region: 'us-east-1',
    },
  ],
}));

const navigateMock = vi.fn();

vi.mock('@/common/config/configService', () => ({
  configService: {
    whenReady: vi.fn().mockResolvedValue(undefined),
    get: vi.fn(() => undefined),
    set: vi.fn().mockResolvedValue(undefined),
    setLocal: vi.fn(),
    setBatch: vi.fn().mockResolvedValue(undefined),
    remove: vi.fn().mockResolvedValue(undefined),
    subscribe: vi.fn(() => () => {}),
  },
}));

vi.mock('@/renderer/pages/conversation/Preview/components/editors/CodeEditor', () => ({
  default: ({ value }: { value: string }) => <pre>{value}</pre>,
}));

vi.mock('@/renderer/pages/artifact/SynonBiomedPdfArtifactViewer', () => ({
  default: () => null,
}));

vi.mock('react-router', () => ({
  useNavigate: () => navigateMock,
  useParams: () => ({ artifactId: 'artifact-1' }),
  useLocation: () => ({ search: '' }),
}));

vi.mock('@/renderer/services/synonBiomedGateway', () => ({
  loadSynonBiomedArtifact: (...args: unknown[]) => loadArtifactMock(...args),
  loadSynonBiomedProjectArtifacts: async () => [await loadArtifactMock()],
  getSynonBiomedArtifactContentUrl: (artifactId: string) => `/api/artifacts/${artifactId}`,
  loadSynonBiomedProjectFolders: async () => [
    {
      folderId: 'folder-1',
      projectId: 'proj_example',
      parentId: null,
      rootFrameId: null,
      name: '用户上传',
      sortOrder: -1000,
      artifactCount: 1,
      isConversationFolder: false,
      isUserUploadsFolder: true,
    },
    {
      folderId: 'folder-2',
      projectId: 'proj_example',
      parentId: null,
      rootFrameId: 'frame-1',
      name: '质量控制',
      sortOrder: 0,
      artifactCount: 3,
      isConversationFolder: true,
      isUserUploadsFolder: false,
    },
  ],
}));

vi.mock('@/renderer/services/synonBiomedArtifacts', () => ({
  loadSynonBiomedArtifactVersions: (...args: unknown[]) => loadVersionsMock(...args),
  loadSynonBiomedArtifactLineage: (...args: unknown[]) => loadLineageMock(...args),
  copySynonBiomedArtifact: (...args: unknown[]) => copyArtifactMock(...args),
  moveSynonBiomedArtifact: (...args: unknown[]) => moveArtifactMock(...args),
  getSynonBiomedArtifactVersionContentUrl: (versionId: string) => `/api/artifacts/versions/${versionId}`,
}));

vi.mock('@/renderer/services/synonBiomedWorkspaceSettings', () => ({
  loadSynonBiomedStorageSettings: (...args: unknown[]) => loadStorageMock(...args),
  loadSynonBiomedCloudBuckets: (...args: unknown[]) => loadCloudBucketsMock(...args),
  exportSynonBiomedArtifactToCloud: (...args: unknown[]) => exportArtifactMock(...args),
}));

import ArtifactPreview from '@/renderer/pages/artifact/ArtifactPreview';
import AudioPreview from '@/renderer/pages/artifact/AudioPreview';
import VideoPreview from '@/renderer/pages/artifact/VideoPreview';

describe('ArtifactPreview', () => {
  it('renders a Synon Biomed image artifact using the backend content endpoint', async () => {
    await render(<ArtifactPreview />);

    expect(await screen.findByRole('heading', { name: 'qc_metrics.png' })).toBeInTheDocument();
    expect(await screen.findByRole('img', { name: 'qc_metrics.png' })).toHaveAttribute(
      'src',
      '/api/artifacts/artifact-1'
    );
    expect(loadArtifactMock).toHaveBeenCalledWith('artifact-1');
    expect(screen.getByRole('button', { name: '笔记' })).toBeInTheDocument();
    expect(await screen.findByRole('tab', { name: '详情' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: '版本' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: '来源' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '复制文件' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '移动到文件夹' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '导出到云存储' })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('tab', { name: '版本' }));
    expect(await screen.findByRole('button', { name: /版本 1/ })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('tab', { name: '来源' }));
    expect(await screen.findByText('由质量控制流程生成')).toBeInTheDocument();
  });

  it('exports the artifact to a connected cloud bucket', async () => {
    await render(<ArtifactPreview />);
    await screen.findByRole('heading', { name: 'qc_metrics.png' });

    fireEvent.click(screen.getByRole('button', { name: '导出到云存储' }));
    const dialog = await screen.findByRole('dialog', { name: '导出到云存储' });
    await waitFor(() => expect(loadCloudBucketsMock).toHaveBeenCalledWith('cloud-1'));
    expect(within(dialog).getByRole('combobox', { name: '导出 bucket' })).toHaveTextContent('bucket-a');
    fireEvent.change(within(dialog).getByRole('textbox', { name: '云对象路径' }), {
      target: { value: 'reports/qc_metrics.png' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: '导出' }));

    await waitFor(() =>
      expect(exportArtifactMock).toHaveBeenCalledWith('cloud-1', {
        artifactId: 'artifact-1',
        bucket: 'bucket-a',
        key: 'reports/qc_metrics.png',
      })
    );
  });

  it('loads JSON into the structured preview and preserves the exact source view', async () => {
    loadArtifactMock.mockResolvedValueOnce({
      ...(await loadArtifactMock()),
      filename: 'analysis.json',
      contentType: 'application/json',
      sizeBytes: 32,
    });
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      if (String(input) === '/api/artifacts/artifact-1') {
        return new Response('{"status":"completed","hits":3}', {
          status: 200,
          headers: { 'content-type': 'application/json' },
        });
      }
      return new Response(JSON.stringify({ data: {} }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      });
    });

    await render(<ArtifactPreview />);

    await screen.findByRole('heading', { name: 'analysis.json' });
    const preview = await screen.findByLabelText('analysis.json 内容', {}, { timeout: 5_000 });
    expect(within(preview).getByRole('tab', { name: '结构' })).toHaveAttribute('aria-selected', 'true');
    expect(preview).toHaveTextContent('statuscompleted');
    expect(preview).toHaveTextContent('hits3');

    fireEvent.click(within(preview).getByRole('tab', { name: '源码' }));
    expect(preview).toHaveTextContent('"status": "completed"');
    expect(preview).toHaveTextContent('"hits": 3');
    expect(fetchMock).toHaveBeenCalledWith('/api/artifacts/artifact-1', {
      credentials: 'same-origin',
      headers: {
        Accept: 'text/plain, text/markdown, application/json, text/csv, text/tab-separated-values, text/html',
      },
      signal: expect.any(AbortSignal),
    });
    fetchMock.mockRestore();
  });

  it('renders artifact navigation and inspector chrome in English', async () => {
    await renderWithI18n(<ArtifactPreview />, 'en-US');

    expect(await screen.findByRole('heading', { name: 'qc_metrics.png' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Back to project' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Details' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Versions' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy file' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Move to folder' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Export to cloud storage' })).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: '详情' })).not.toBeInTheDocument();
  });

  it.each([
    { filename: 'assay-audio.wav', contentType: 'audio/wav', tagName: 'AUDIO' },
    { filename: 'assay-video.mp4', contentType: 'video/mp4', tagName: 'VIDEO' },
  ])('renders $contentType with loading, playback and bounded error states', async (media) => {
    loadArtifactMock.mockResolvedValueOnce({
      ...(await loadArtifactMock()),
      filename: media.filename,
      contentType: media.contentType,
    });

    await render(<ArtifactPreview />);

    const player = await screen.findByLabelText(media.filename);
    expect(player.tagName).toBe(media.tagName);
    expect(player).toHaveAttribute('src', '/api/artifacts/artifact-1');
    expect(player).toHaveAttribute('preload', 'metadata');
    expect(player).toHaveAttribute('controls');
    expect(screen.getByRole('status')).toBeInTheDocument();

    fireEvent.loadedMetadata(player);
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
    expect(player).toHaveStyle({ display: 'block' });

    fireEvent.error(player);
    expect(await screen.findByRole('alert')).toHaveTextContent(
      media.tagName === 'AUDIO' ? '音频加载失败' : '视频加载失败'
    );
  });

  it.each([
    { Component: AudioPreview, filename: 'assay-audio.wav', tagName: 'AUDIO' },
    { Component: VideoPreview, filename: 'assay-video.mp4', tagName: 'VIDEO' },
  ])('resets and releases the $tagName player lifecycle', async ({ Component, filename, tagName }) => {
    const pause = vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => undefined);
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
    const firstUrl = `/api/artifacts/${tagName.toLowerCase()}-1`;
    const secondUrl = `/api/artifacts/${tagName.toLowerCase()}-2`;
    const view = await render(<Component url={firstUrl} filename={filename} />);

    const firstPlayer = await screen.findByLabelText(filename);
    view.rerender(<Component url={secondUrl} filename={filename} />);
    await waitFor(() => expect(screen.getByRole('status')).toBeInTheDocument());
    const secondPlayer = await screen.findByLabelText(filename);
    expect(secondPlayer).not.toBe(firstPlayer);
    expect(secondPlayer).toHaveAttribute('src', secondUrl);
    if (tagName === 'VIDEO') expect(secondPlayer).toHaveAttribute('playsinline');

    fireEvent.error(firstPlayer);
    fireEvent.loadedMetadata(firstPlayer);
    fireEvent.canPlay(firstPlayer);
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.getByRole('status')).toBeInTheDocument();

    fireEvent.canPlay(secondPlayer);
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
    document.dispatchEvent(new Event('visibilitychange'));
    expect(pause).toHaveBeenCalledTimes(1);

    fireEvent.error(secondPlayer);
    const alert = await screen.findByRole('alert');
    expect(alert).not.toHaveTextContent(secondUrl);

    view.unmount();
    document.dispatchEvent(new Event('visibilitychange'));
    expect(pause).toHaveBeenCalledTimes(1);
  });
});
