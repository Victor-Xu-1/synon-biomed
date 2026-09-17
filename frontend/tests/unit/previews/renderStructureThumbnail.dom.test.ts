import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

let persistentThumbnailCache: Map<string, string>;
let cacheOpenMock: ReturnType<typeof vi.fn>;

const engineMocks = vi.hoisted(() => {
  const engine = {
    load: vi.fn(async () => undefined),
    captureImage: vi.fn(async () => 'data:image/png;base64,MOLSTAR_CAPTURE'),
    dispose: vi.fn(),
  };
  return {
    create: vi.fn(async () => engine),
    engine,
  };
});

vi.mock('@/renderer/pages/conversation/Preview/components/viewers/molstarStructureEngine', () => ({
  createMolstarStructureEngine: engineMocks.create,
}));

import { renderStructureThumbnail } from '@/renderer/pages/conversation/Preview/components/viewers/renderStructureThumbnail';

describe('renderStructureThumbnail', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    persistentThumbnailCache = new Map();
    cacheOpenMock = vi.fn(async () => ({
      match: vi.fn(async (request: string) => {
        const cached = persistentThumbnailCache.get(String(request));
        return cached ? new Response(cached, { status: 200 }) : undefined;
      }),
      put: vi.fn(async (request: string, response: Response) => {
        persistentThumbnailCache.set(String(request), await response.text());
      }),
    }));
    vi.stubGlobal('caches', { open: cacheOpenMock });
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response('data_9CUO\n#\nloop_\n_atom_site.group_PDB\n#', {
            status: 200,
            headers: { 'content-type': 'chemical/x-mmcif' },
          })
      )
    );
  });

  afterEach(() => vi.unstubAllGlobals());

  it('loads the exact CIF response through Mol*, captures a real PNG and frees the hidden WebGL host', async () => {
    const imageUrl = await renderStructureThumbnail({
      contentUrl: '/api/artifacts/9cuo/content',
      filename: '9cuo.cif',
      signal: new AbortController().signal,
    });

    expect(fetch).toHaveBeenCalledWith(
      '/api/artifacts/9cuo/content',
      expect.objectContaining({ headers: { accept: 'text/plain, chemical/*' } })
    );
    expect(engineMocks.create).toHaveBeenCalledWith(expect.any(HTMLElement), { mode: 'thumbnail' });
    expect(engineMocks.engine.load).toHaveBeenCalledWith(expect.stringContaining('data_9CUO'), '9cuo.cif', 'mmcif');
    expect(engineMocks.engine.captureImage).toHaveBeenCalledWith({ width: 304, height: 184 });
    expect(imageUrl).toBe('data:image/png;base64,MOLSTAR_CAPTURE');
    expect(engineMocks.engine.dispose).toHaveBeenCalledTimes(1);
    expect(document.body.querySelector('[aria-hidden="true"]')).not.toBeInTheDocument();
  });

  it('disposes the Mol* engine when parsing or rendering fails', async () => {
    engineMocks.engine.load.mockRejectedValueOnce(new Error('invalid structure'));

    await expect(
      renderStructureThumbnail({
        contentUrl: '/api/artifacts/broken/content',
        filename: 'broken.cif',
        signal: new AbortController().signal,
      })
    ).rejects.toThrow('invalid structure');

    expect(engineMocks.engine.captureImage).not.toHaveBeenCalled();
    expect(engineMocks.engine.dispose).toHaveBeenCalledTimes(1);
    expect(document.body.querySelector('[aria-hidden="true"]')).not.toBeInTheDocument();
  });

  it('coalesces simultaneous requests for the same immutable artifact into one Mol* render', async () => {
    let releaseCapture: ((value: string) => void) | undefined;
    engineMocks.engine.captureImage.mockImplementationOnce(
      () =>
        new Promise<string>((resolve) => {
          releaseCapture = resolve;
        })
    );

    const first = renderStructureThumbnail({
      contentUrl: '/api/artifacts/shared/versions/version-1',
      filename: 'shared.cif',
      signal: new AbortController().signal,
    });
    const second = renderStructureThumbnail({
      contentUrl: '/api/artifacts/shared/versions/version-1',
      filename: 'shared.cif',
      signal: new AbortController().signal,
    });

    await vi.waitFor(() => expect(releaseCapture).toBeDefined());
    expect(engineMocks.create).toHaveBeenCalledTimes(1);
    releaseCapture?.('data:image/png;base64,SHARED_CAPTURE');
    await expect(Promise.all([first, second])).resolves.toEqual([
      'data:image/png;base64,SHARED_CAPTURE',
      'data:image/png;base64,SHARED_CAPTURE',
    ]);
    expect(engineMocks.engine.load).toHaveBeenCalledTimes(1);
    expect(engineMocks.engine.dispose).toHaveBeenCalledTimes(1);
  });

  it('keeps the shared render alive when one duplicate thumbnail unmounts', async () => {
    let releaseCapture: ((value: string) => void) | undefined;
    engineMocks.engine.captureImage.mockImplementationOnce(
      () =>
        new Promise<string>((resolve) => {
          releaseCapture = resolve;
        })
    );
    const firstController = new AbortController();
    const secondController = new AbortController();
    const first = renderStructureThumbnail({
      contentUrl: '/api/artifacts/shared/versions/version-3',
      filename: 'shared-abort.cif',
      signal: firstController.signal,
    });
    const second = renderStructureThumbnail({
      contentUrl: '/api/artifacts/shared/versions/version-3',
      filename: 'shared-abort.cif',
      signal: secondController.signal,
    });

    await vi.waitFor(() => expect(releaseCapture).toBeDefined());
    firstController.abort();
    await expect(first).rejects.toMatchObject({ name: 'AbortError' });
    expect(engineMocks.create).toHaveBeenCalledTimes(1);

    releaseCapture?.('data:image/png;base64,REMAINING_CONSUMER');
    await expect(second).resolves.toBe('data:image/png;base64,REMAINING_CONSUMER');
    expect(engineMocks.engine.dispose).toHaveBeenCalledTimes(1);
  });

  it('restores a generated thumbnail from Cache Storage after the renderer module reloads', async () => {
    const request = {
      contentUrl: '/api/artifacts/persisted/versions/version-2',
      filename: 'persisted.pdb',
      signal: new AbortController().signal,
    };
    const firstModule =
      await import('@/renderer/pages/conversation/Preview/components/viewers/renderStructureThumbnail');

    await expect(firstModule.renderStructureThumbnail(request)).resolves.toBe('data:image/png;base64,MOLSTAR_CAPTURE');
    expect(engineMocks.create).toHaveBeenCalledTimes(1);
    expect(persistentThumbnailCache.size).toBe(1);

    vi.resetModules();
    const reloadedModule =
      await import('@/renderer/pages/conversation/Preview/components/viewers/renderStructureThumbnail');
    await expect(
      reloadedModule.renderStructureThumbnail({ ...request, signal: new AbortController().signal })
    ).resolves.toBe('data:image/png;base64,MOLSTAR_CAPTURE');
    expect(engineMocks.create).toHaveBeenCalledTimes(1);
    expect(cacheOpenMock).toHaveBeenCalled();
  });
});
