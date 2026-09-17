import { fireEvent, screen, waitFor } from '@testing-library/react';
import { Message } from '@arco-design/web-react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import SynonBiomedCloudBrowser from '@/renderer/pages/settings/components/SynonBiomedCloudBrowser';
import { renderWithSettingsI18n } from './settingsI18nTestUtils';

const mocks = vi.hoisted(() => ({
  buckets: vi.fn(),
  folder: vi.fn(),
  importObject: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedWorkspaceSettings', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/renderer/services/synonBiomedWorkspaceSettings')>();
  return {
    ...actual,
    loadSynonBiomedCloudBuckets: mocks.buckets,
    loadSynonBiomedCloudFolder: mocks.folder,
    importSynonBiomedCloudObject: mocks.importObject,
  };
});

const credential = {
  id: 'cloud-1',
  provider: 's3',
  name: 'Research storage',
  credentialType: 'access_key',
  connected: true,
  defaultBucket: 'bucket-a',
  region: 'us-east-1',
};

const project = {
  projectId: 'project-1',
  name: 'STAT6',
  description: null,
  context: null,
  conversationCount: 1,
  artifactCount: 2,
  createdAt: null,
  updatedAt: null,
  lastActiveAt: null,
};

describe('SynonBiomedCloudBrowser', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.spyOn(Message, 'success').mockImplementation(() => ({ close: vi.fn() }));
    vi.spyOn(Message, 'error').mockImplementation(() => ({ close: vi.fn() }));
    mocks.buckets.mockResolvedValue(['bucket-a']);
    mocks.folder.mockResolvedValue({
      folders: ['results/'],
      files: [{ key: 'input.txt', size: 12, lastModified: '2026-07-12T12:00:00Z' }],
    });
    mocks.importObject.mockResolvedValue({ id: 'artifact-1' });
  });

  it('browses folders and imports a cloud object into a real project coordinate', async () => {
    await renderWithSettingsI18n(<SynonBiomedCloudBrowser credential={credential} projects={[project]} />);

    await waitFor(() => expect(screen.getByText('input.txt')).toBeInTheDocument());
    expect(mocks.folder).toHaveBeenCalledWith('cloud-1', 'bucket-a', '');
    expect(screen.getByTitle('下载')).toHaveAttribute(
      'href',
      '/api/cloud-credentials/cloud-1/download?bucket=bucket-a&key=input.txt'
    );

    fireEvent.click(screen.getByText('results'));
    await waitFor(() => expect(mocks.folder).toHaveBeenCalledWith('cloud-1', 'bucket-a', 'results/'));

    fireEvent.click(screen.getByTitle('导入项目'));
    expect(screen.getByText('选择导入目标项目')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '确定' }));
    await waitFor(() =>
      expect(mocks.importObject).toHaveBeenCalledWith('cloud-1', {
        bucket: 'bucket-a',
        key: 'input.txt',
        projectId: 'project-1',
      })
    );
  });

  it('renders cloud browsing actions in English', async () => {
    await renderWithSettingsI18n(<SynonBiomedCloudBrowser credential={credential} projects={[project]} />, 'en-US');

    expect(await screen.findByTitle('Download')).toBeInTheDocument();
    expect(screen.getByTitle('Import into project')).toBeInTheDocument();
    expect(screen.getByTitle('Refresh cloud folder')).toBeInTheDocument();
  });
});
