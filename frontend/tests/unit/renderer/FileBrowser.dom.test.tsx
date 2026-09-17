import { act, cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';
import { SynonBiomedFileImportError } from '@/renderer/services/synonBiomedFileImportError';

const mocks = vi.hoisted(() => ({
  loadLocal: vi.fn(),
  loadRemote: vi.fn(),
  importLocal: vi.fn(),
  importRemote: vi.fn(),
  loadCloud: vi.fn(),
  importCloud: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedCompute', () => ({
  loadSynonBiomedLocalDirectory: mocks.loadLocal,
  loadSynonBiomedRemoteDirectory: mocks.loadRemote,
  importSynonBiomedLocalFile: mocks.importLocal,
  importSynonBiomedRemoteFile: mocks.importRemote,
  getSynonBiomedLocalFileUrl: (path: string, disposition: string) =>
    '/api/compute/local/download?path=' + encodeURIComponent(path) + '&disposition=' + disposition,
  getSynonBiomedRemoteFileUrl: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedWorkspaceSettings', () => ({
  loadSynonBiomedCloudFolder: mocks.loadCloud,
  importSynonBiomedCloudObject: mocks.importCloud,
  getSynonBiomedCloudDownloadUrl: vi.fn(),
}));

vi.mock('@/renderer/pages/artifact/SynonBiomedPdfArtifactViewer', () => ({
  SynonBiomedPdfArtifactViewer: () => <div>PDF preview</div>,
}));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: { ...actual.Message, success: mocks.success, error: mocks.error },
  };
});

import { FileBrowser } from '@/renderer/components/synonBiomed/files/FileBrowser';

describe('FileBrowser', () => {
  beforeEach(() => {
    mocks.loadLocal.mockResolvedValue({
      entries: [
        { name: 'folder', isDirectory: true, size: 0, mtime: 1 },
        { name: 'note.txt', isDirectory: false, size: 12, mtime: 2 },
      ],
      truncated: false,
      resolvedPath: '/home/user',
      roots: { home: '/home/user' },
    });
    mocks.importLocal.mockResolvedValue({ artifactId: 'artifact-1', versionId: null, filename: 'note.txt' });
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockResolvedValue(new Response('hello from host')));
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: vi.fn().mockReturnValue({
        matches: false,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      }),
    });
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
    vi.unstubAllGlobals();
  });

  it('loads a real host directory, previews text and imports the selected file', async () => {
    const onImported = vi.fn();
    await renderFileBrowser(
      <FileBrowser
        host={{ id: 'local', kind: 'local', label: 'Workstation' }}
        projectId='project-1'
        onImported={onImported}
      />
    );

    fireEvent.click(await screen.findByRole('button', { name: '选择文件 note.txt' }));
    expect(await screen.findByText('hello from host')).toBeInTheDocument();
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: '导入项目' }));
    });

    await waitFor(() => expect(mocks.importLocal).toHaveBeenCalledWith('/home/user/note.txt', 'project-1'));
    expect(onImported).toHaveBeenCalledTimes(1);
    expect(mocks.success).toHaveBeenCalledWith('文件已导入项目');
    expect(screen.getByTestId('file-import-status-/home/user/note.txt')).toHaveTextContent('已导入');
  });

  it('shows a stable path-level reason when a file is too large', async () => {
    mocks.importLocal.mockRejectedValueOnce(new SynonBiomedFileImportError(413, 'too_large'));

    await renderFileBrowser(
      <FileBrowser
        host={{ id: 'local', kind: 'local', label: 'Workstation' }}
        projectId='project-1'
        onImported={vi.fn()}
      />
    );

    fireEvent.click(await screen.findByRole('button', { name: '选择文件 note.txt' }));
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: '导入项目' }));
    });

    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith('文件超过 50 MiB 导入上限。'));
    expect(screen.getByTestId('file-import-status-/home/user/note.txt')).toHaveTextContent('导入失败');
    expect(mocks.success).not.toHaveBeenCalled();
  });

  it('makes a truncated host directory visible instead of silently hiding omitted files', async () => {
    mocks.loadLocal.mockResolvedValueOnce({
      entries: [{ name: 'note.txt', isDirectory: false, size: 12, mtime: 2 }],
      truncated: true,
      resolvedPath: '/home/user',
      roots: { home: '/home/user' },
    });

    await renderFileBrowser(
      <FileBrowser
        host={{ id: 'local', kind: 'local', label: 'Workstation' }}
        projectId='project-1'
        onImported={vi.fn()}
      />
    );

    expect(await screen.findByTestId('file-browser-truncated')).toHaveTextContent('当前只显示有界的前一部分文件');
  });

  it('shows a load error and retries the same host', async () => {
    mocks.loadLocal.mockRejectedValueOnce(new Error('host unavailable')).mockResolvedValueOnce({
      entries: [],
      truncated: false,
      resolvedPath: '/home/user',
      roots: { home: '/home/user' },
    });

    await renderFileBrowser(
      <FileBrowser
        host={{ id: 'local', kind: 'local', label: 'Workstation' }}
        projectId='project-1'
        onImported={vi.fn()}
      />
    );

    expect(await screen.findByRole('alert')).toHaveTextContent('目录加载失败');
    expect(screen.getByRole('alert')).not.toHaveTextContent('host unavailable');
    fireEvent.click(screen.getByRole('button', { name: '重试' }));
    await waitFor(() => expect(mocks.loadLocal).toHaveBeenCalledTimes(2));
    expect(await screen.findByText('目录为空')).toBeInTheDocument();
  });

  it('renders file navigation and actions in English', async () => {
    await renderFileBrowser(
      <FileBrowser
        host={{ id: 'local', kind: 'local', label: 'Workstation' }}
        projectId='project-1'
        onImported={vi.fn()}
      />,
      'en-US'
    );

    fireEvent.click(await screen.findByRole('button', { name: 'Select file note.txt' }));
    expect(await screen.findByText('hello from host')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Import into project' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy path' })).toBeInTheDocument();
  });
});

async function renderFileBrowser(ui: React.ReactElement, language: 'zh-CN' | 'en-US' = 'zh-CN'): Promise<void> {
  await act(async () => {
    await renderWithI18n(ui, language);
    await Promise.resolve();
    await Promise.resolve();
  });
}
