import { cleanup, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FilesOverlay } from '@/renderer/components/synonBiomed/files/FilesOverlay';
import { renderWithI18n } from '../i18nTestUtils';

const gateway = vi.hoisted(() => ({
  localHost: vi.fn(),
  providers: vi.fn(),
  credentials: vi.fn(),
  buckets: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedCompute', () => ({
  loadSynonBiomedLocalHostInfo: gateway.localHost,
  loadSynonBiomedComputeProviders: gateway.providers,
}));

vi.mock('@/renderer/services/synonBiomedWorkspaceSettings', () => ({
  loadSynonBiomedCloudCredentials: gateway.credentials,
  loadSynonBiomedCloudBuckets: gateway.buckets,
}));

vi.mock('@/renderer/components/synonBiomed/files/FileBrowser', () => ({
  FileBrowser: ({ projectId }: { projectId: string }) => <div data-testid='file-browser'>{projectId}</div>,
}));

describe('FilesOverlay', () => {
  beforeEach(() => {
    gateway.localHost.mockResolvedValue({ hostLabel: 'Local', hostDetail: 'WSL' });
    gateway.providers.mockResolvedValue([]);
    gateway.credentials.mockResolvedValue([]);
    gateway.buckets.mockResolvedValue([]);
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
    vi.restoreAllMocks();
  });

  it('renders host file controls in English', async () => {
    await renderWithI18n(
      <FilesOverlay visible projectId='project-1' onClose={vi.fn()} onImported={vi.fn()} />,
      'en-US'
    );

    expect(await screen.findByText('Host files')).toBeInTheDocument();
    expect(screen.getByRole('combobox', { name: 'Host' })).toBeInTheDocument();
    expect(await screen.findByTestId('file-browser')).toHaveTextContent('project-1');
  });

  it('shows a localized safe error when host discovery fails', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    gateway.localHost.mockRejectedValueOnce(new Error('secret host diagnostic'));

    await renderWithI18n(<FilesOverlay visible projectId='project-1' onClose={vi.fn()} onImported={vi.fn()} />);

    expect(await screen.findByText('无法加载文件来源。')).toBeInTheDocument();
    expect(screen.queryByText('secret host diagnostic')).not.toBeInTheDocument();
    expect(JSON.stringify(warn.mock.calls)).toContain('secret host diagnostic');
  });
});
