import React, { Suspense } from 'react';
import { render, screen } from '@testing-library/react';
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest';

const moduleLoads = vi.hoisted(() => ({
  structure: vi.fn(),
  table: vi.fn(),
  molecule: vi.fn(),
  artifactPdf: vi.fn(),
  artifactImage: vi.fn(),
  audio: vi.fn(),
  video: vi.fn(),
}));

vi.mock('@/renderer/pages/conversation/Preview/components/viewers/SynonBiomedStructureViewer', () => {
  moduleLoads.structure();
  return { default: () => <div>structure-viewer</div> };
});

vi.mock('@/renderer/pages/conversation/Preview/components/viewers/SynonBiomedTableViewer', () => {
  moduleLoads.table();
  return { default: () => <div>table-viewer</div> };
});

vi.mock('@/renderer/pages/conversation/Preview/components/viewers/SynonBiomedMoleculeViewer', () => {
  moduleLoads.molecule();
  return { default: () => <div>molecule-viewer</div> };
});

vi.mock('@/renderer/pages/artifact/SynonBiomedPdfArtifactViewer', () => {
  moduleLoads.artifactPdf();
  return { default: () => <div>artifact-pdf-viewer</div> };
});

vi.mock('@/renderer/pages/artifact/SynonBiomedImageArtifactViewer', () => {
  moduleLoads.artifactImage();
  return { SynonBiomedImageArtifactViewer: () => <div>artifact-image-viewer</div> };
});

vi.mock('@/renderer/pages/artifact/AudioPreview', () => {
  moduleLoads.audio();
  return { default: () => <div>audio-viewer</div> };
});

vi.mock('@/renderer/pages/artifact/VideoPreview', () => {
  moduleLoads.video();
  return { default: () => <div>video-viewer</div> };
});

describe('scientific preview module loading', () => {
  beforeAll(() => {
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(JSON.stringify({ data: {} }), {
            status: 200,
            headers: { 'content-type': 'application/json' },
          })
      )
    );
  });

  afterAll(() => vi.unstubAllGlobals());

  it('releases a failed import so chunk recovery can retry', async () => {
    const { cachePreviewModule } =
      await import('@/renderer/pages/conversation/Preview/components/viewers/scientificPreviewLoaders');
    const expectedModule = { default: () => null };
    const load = vi.fn().mockRejectedValueOnce(new Error('chunk unavailable')).mockResolvedValueOnce(expectedModule);
    const cached = cachePreviewModule(load);

    await expect(cached()).rejects.toThrow('chunk unavailable');
    await expect(cached()).resolves.toBe(expectedModule);
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('does not execute scientific viewer modules when the preview panel entry module is imported', async () => {
    await import('@/renderer/pages/conversation/Preview/components/PreviewPanel/PreviewPanel');

    expect(moduleLoads.structure).not.toHaveBeenCalled();
    expect(moduleLoads.table).not.toHaveBeenCalled();
    expect(moduleLoads.molecule).not.toHaveBeenCalled();
  });

  it('does not execute scientific or media viewers when the artifact preview entry module is imported', async () => {
    await import('@/renderer/pages/artifact/ArtifactPreview');

    expect(moduleLoads.structure).not.toHaveBeenCalled();
    expect(moduleLoads.table).not.toHaveBeenCalled();
    expect(moduleLoads.molecule).not.toHaveBeenCalled();
    expect(moduleLoads.artifactPdf).not.toHaveBeenCalled();
    expect(moduleLoads.artifactImage).not.toHaveBeenCalled();
    expect(moduleLoads.audio).not.toHaveBeenCalled();
    expect(moduleLoads.video).not.toHaveBeenCalled();
  });

  it('preloads and caches the structure viewer from user intent', async () => {
    const { preloadSynonBiomedStructureViewer } =
      await import('@/renderer/pages/conversation/Preview/components/viewers/scientificPreviewLoaders');

    await preloadSynonBiomedStructureViewer();
    await preloadSynonBiomedStructureViewer();
    expect(moduleLoads.structure).toHaveBeenCalledTimes(1);
  });

  it('loads only the requested viewer module and reuses its cached import', async () => {
    const { LazySynonBiomedStructureViewer } =
      await import('@/renderer/pages/conversation/Preview/components/viewers/scientificPreviewLoaders');

    const rendered = render(
      <Suspense fallback={<div>loading-viewer</div>}>
        <LazySynonBiomedStructureViewer filename='example.pdb' content='HEADER' />
      </Suspense>
    );

    expect(await screen.findByText('structure-viewer')).toBeInTheDocument();
    expect(moduleLoads.structure).toHaveBeenCalledTimes(1);
    expect(moduleLoads.table).not.toHaveBeenCalled();
    expect(moduleLoads.molecule).not.toHaveBeenCalled();

    rendered.rerender(
      <Suspense fallback={<div>loading-viewer</div>}>
        <LazySynonBiomedStructureViewer filename='second.pdb' content='ATOM' />
      </Suspense>
    );
    expect(await screen.findByText('structure-viewer')).toBeInTheDocument();
    expect(moduleLoads.structure).toHaveBeenCalledTimes(1);
  });
});
