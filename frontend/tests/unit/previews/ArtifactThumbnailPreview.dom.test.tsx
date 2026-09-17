import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const thumbnailMocks = vi.hoisted(() => ({
  render: vi.fn(),
}));

vi.mock('@/renderer/pages/conversation/Preview/components/viewers/renderStructureThumbnail', () => ({
  renderStructureThumbnail: thumbnailMocks.render,
}));

import ArtifactThumbnailPreview from '@/renderer/components/synonBiomed/files/ArtifactThumbnailPreview';

describe('ArtifactThumbnailPreview structure rendering', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.stubGlobal('IntersectionObserver', undefined);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it('shows the PNG produced from the real Mol* render path without a synthetic SVG', async () => {
    thumbnailMocks.render.mockResolvedValue('data:image/png;base64,REAL_MOLSTAR_FRAME');

    await renderWithI18n(
      <ArtifactThumbnailPreview
        filename='9cuo.cif'
        contentUrl='/api/artifacts/9cuo/content'
        contentType='chemical/x-mmcif'
        previewKind='structure'
        sizeBytes={1024}
      />,
      'zh-CN'
    );

    expect(screen.getByRole('status', { name: '正在渲染真实三维结构预览' })).toBeInTheDocument();
    const image = await screen.findByRole('img', { name: '9cuo.cif' });
    expect(image).toHaveAttribute('src', 'data:image/png;base64,REAL_MOLSTAR_FRAME');
    expect(thumbnailMocks.render).toHaveBeenCalledWith(
      expect.objectContaining({
        contentUrl: '/api/artifacts/9cuo/content',
        filename: '9cuo.cif',
        signal: expect.any(AbortSignal),
      })
    );
    expect(document.querySelector('svg')).not.toBeInTheDocument();
    expect(document.querySelector('polyline')).not.toBeInTheDocument();
  });

  it('renders an explicit failure state and never substitutes a fake structure graphic', async () => {
    thumbnailMocks.render.mockRejectedValue(new Error('invalid mmCIF'));

    await renderWithI18n(
      <ArtifactThumbnailPreview
        filename='broken.cif'
        contentUrl='/api/artifacts/broken/content'
        contentType='chemical/x-mmcif'
        previewKind='structure'
        sizeBytes={1024}
      />,
      'zh-CN'
    );

    await waitFor(() => expect(screen.getByText('三维结构渲染失败')).toBeInTheDocument());
    expect(document.querySelector('svg')).not.toBeInTheDocument();
    expect(screen.queryByTestId('artifact-structure-thumbnail-image')).not.toBeInTheDocument();
  });

  it('cancels the real render task when the virtualized card unmounts', async () => {
    let capturedSignal: AbortSignal | undefined;
    thumbnailMocks.render.mockImplementation(
      ({ signal }: { signal: AbortSignal }) =>
        new Promise<string>((_resolve, reject) => {
          capturedSignal = signal;
          signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true });
        })
    );

    const view = await renderWithI18n(
      <ArtifactThumbnailPreview
        filename='large-complex.pdb'
        contentUrl='/api/artifacts/large-complex/content'
        contentType='chemical/x-pdb'
        previewKind='structure'
        sizeBytes={1024}
      />,
      'en-US'
    );

    await waitFor(() => expect(capturedSignal).toBeDefined());
    view.unmount();
    expect(capturedSignal?.aborted).toBe(true);
  });

  it('renders a bounded image artifact directly and falls back to an image thumbnail when it fails', async () => {
    await renderWithI18n(
      <ArtifactThumbnailPreview
        filename='fig_temperature.png'
        contentUrl='/api/artifacts/fig-temperature/content'
        contentType='application/octet-stream'
        sizeBytes={96 * 1024}
      />,
      'en-US'
    );

    const image = screen.getByTestId('artifact-image-thumbnail');
    expect(image).toHaveAttribute('src', '/api/artifacts/fig-temperature/content');
    expect(image).toHaveAttribute('alt', 'fig_temperature.png');

    fireEvent.error(image);
    await waitFor(() => expect(screen.queryByTestId('artifact-image-thumbnail')).not.toBeInTheDocument());
    expect(screen.getByRole('img', { name: 'fig_temperature.png' })).toBeInTheDocument();
  });
});
