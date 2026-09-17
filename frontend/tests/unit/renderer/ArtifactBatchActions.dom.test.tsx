import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { SynonBiomedProjectArtifact } from '@/renderer/services/synonBiomedGateway';
import { renderWithI18n } from '../i18nTestUtils';

const mocks = vi.hoisted(() => ({
  credentials: vi.fn(),
  buckets: vi.fn(),
  exportBatch: vi.fn(),
  success: vi.fn(),
  warning: vi.fn(),
  error: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedGateway', () => ({
  getSynonBiomedArtifactContentUrl: (artifactId: string) => `/api/artifacts/${artifactId}/content`,
}));

vi.mock('@/renderer/services/synonBiomedWorkspaceSettings', () => ({
  loadSynonBiomedCloudCredentials: mocks.credentials,
  loadSynonBiomedCloudBuckets: mocks.buckets,
  exportSynonBiomedArtifactsToCloud: mocks.exportBatch,
}));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: {
      ...actual.Message,
      success: mocks.success,
      warning: mocks.warning,
      error: mocks.error,
    },
  };
});

import { ArtifactBatchActions, buildCloudExportItems } from '@/renderer/pages/project/ArtifactBatchActions';

const artifact = (artifactId: string, filename: string): SynonBiomedProjectArtifact => ({
  artifactId,
  versionId: `version-${artifactId}`,
  versionNumber: 1,
  projectId: 'project-1',
  rootFrameId: null,
  frameId: null,
  creatingFrameId: null,
  filename,
  contentType: 'text/plain',
  sizeBytes: 12,
  createdAt: '2026-07-14T08:00:00Z',
  updatedAt: '2026-07-14T08:00:00Z',
  checksum: null,
  filePath: null,
  folderId: null,
  priority: null,
  isUserUpload: false,
  agentName: 'OPERON',
  isIntermediate: false,
});

const artifacts = [artifact('artifact-a1234567', 'report.txt'), artifact('artifact-b7654321', 'report.txt')];

describe('ArtifactBatchActions', () => {
  beforeEach(() => {
    mocks.credentials.mockResolvedValue([
      { id: 'cloud-1', name: 'Research S3', provider: 's3', connected: true, defaultBucket: 'results' },
    ]);
    mocks.buckets.mockResolvedValue(['results']);
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

  it('builds unique object keys for duplicate filenames', () => {
    expect(buildCloudExportItems(artifacts, '/exports/run-1/')).toEqual([
      { artifactId: 'artifact-a1234567', key: 'exports/run-1/report.txt' },
      { artifactId: 'artifact-b7654321', key: 'exports/run-1/report-artifact.txt' },
    ]);
  });

  it('downloads sequentially, reports partial failure and keeps only failures selected', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response('first'))
      .mockResolvedValueOnce(new Response('failed', { status: 503 }));
    const createObjectURL = vi.fn(() => 'blob:test');
    const revokeObjectURL = vi.fn();
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    vi.stubGlobal('fetch', fetchMock);
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: createObjectURL });
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: revokeObjectURL });
    const onSelectionChange = vi.fn();

    await renderWithI18n(<ArtifactBatchActions artifacts={artifacts} onSelectionChange={onSelectionChange} />, 'en-US');
    fireEvent.click(screen.getByRole('button', { name: 'Batch download' }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      '/api/artifacts/artifact-a1234567/content',
      '/api/artifacts/artifact-b7654321/content',
    ]);
    expect(click).toHaveBeenCalledTimes(1);
    expect(onSelectionChange).toHaveBeenCalledWith(['artifact-b7654321']);
    expect(await screen.findByRole('alert')).toHaveTextContent('report.txt: Failed');
    expect(mocks.warning).toHaveBeenCalled();
  });

  it('loads cloud destinations and preserves failed exports for retry', async () => {
    mocks.exportBatch.mockImplementation(async (_id, request, onProgress) => {
      onProgress?.({ completed: 1, total: 2, current: request.items[0] });
      onProgress?.({ completed: 2, total: 2, current: request.items[1], error: 'permission denied' });
      return { completed: [request.items[0]], failed: [{ ...request.items[1], error: 'permission denied' }] };
    });
    const onSelectionChange = vi.fn();
    await renderWithI18n(<ArtifactBatchActions artifacts={artifacts} onSelectionChange={onSelectionChange} />, 'en-US');

    fireEvent.click(screen.getByRole('button', { name: 'Batch cloud export' }));
    await waitFor(() => expect(mocks.buckets).toHaveBeenCalledWith('cloud-1'));
    fireEvent.change(screen.getByRole('textbox', { name: 'Object path prefix' }), { target: { value: 'exports' } });
    fireEvent.click(screen.getByRole('button', { name: 'Export' }));

    await waitFor(() => expect(mocks.exportBatch).toHaveBeenCalledTimes(1));
    expect(mocks.exportBatch.mock.calls[0][0]).toBe('cloud-1');
    expect(mocks.exportBatch.mock.calls[0][1]).toEqual({
      bucket: 'results',
      items: buildCloudExportItems(artifacts, 'exports'),
    });
    expect(onSelectionChange).toHaveBeenCalledWith(['artifact-b7654321']);
    expect(mocks.warning).toHaveBeenCalled();
  });

  it('recovers from a rejected cloud export and leaves the dialog retryable', async () => {
    mocks.exportBatch.mockRejectedValueOnce(new Error('service unavailable'));
    await renderWithI18n(<ArtifactBatchActions artifacts={artifacts} onSelectionChange={vi.fn()} />, 'en-US');

    fireEvent.click(screen.getByRole('button', { name: 'Batch cloud export' }));
    await waitFor(() => expect(mocks.buckets).toHaveBeenCalledWith('cloud-1'));
    const exportButton = screen.getByRole('button', { name: 'Export' });
    fireEvent.click(exportButton);

    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith('Batch cloud export failed.'));
    expect(screen.getByRole('button', { name: 'Export' })).toBeEnabled();
    expect(screen.getByRole('dialog', { name: 'Batch cloud export' })).toBeInTheDocument();
  });
});
