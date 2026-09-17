import {
  clearSynonBiomedFrameReads,
  invalidateSynonBiomedFrameReads,
  loadCachedSynonBiomedFrame,
  loadCachedSynonBiomedFrameArtifacts,
} from '@/renderer/services/synonBiomedFrameReads';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { resetRendererAccountScopeForTest, setRendererAccountOwner } from '@/renderer/services/rendererAccountScope';

afterEach(() => {
  clearSynonBiomedFrameReads();
  resetRendererAccountScopeForTest();
  vi.unstubAllGlobals();
});

describe('Synon Biomed frame read cache', () => {
  it('coalesces frame and artifact reads while keeping the resources distinct', async () => {
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      return new Response(JSON.stringify(url.endsWith('/artifacts') ? [] : { frame: { id: 'frame-1' } }));
    });
    vi.stubGlobal('fetch', fetchImpl);

    const [frameA, frameB, artifactsA, artifactsB] = await Promise.all([
      loadCachedSynonBiomedFrame('frame-1'),
      loadCachedSynonBiomedFrame('frame-1'),
      loadCachedSynonBiomedFrameArtifacts('frame-1'),
      loadCachedSynonBiomedFrameArtifacts('frame-1'),
    ]);

    expect(frameA).toEqual(frameB);
    expect(artifactsA).toEqual(artifactsB);
    expect(fetchImpl).toHaveBeenCalledTimes(2);
  });

  it('never reuses a settled frame read after the authenticated owner changes', async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ frame: { id: 'frame-shared', owner: 'owner-a' } })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ frame: { id: 'frame-shared', owner: 'owner-b' } })));
    vi.stubGlobal('fetch', fetchImpl);

    setRendererAccountOwner('owner-a');
    await expect(loadCachedSynonBiomedFrame('frame-shared')).resolves.toMatchObject({
      frame: { owner: 'owner-a' },
    });
    setRendererAccountOwner('owner-b');
    await expect(loadCachedSynonBiomedFrame('frame-shared')).resolves.toMatchObject({
      frame: { owner: 'owner-b' },
    });

    expect(fetchImpl).toHaveBeenCalledTimes(2);
  });

  it('invalidates both resources and does not let one consumer abort the shared request', async () => {
    let resolveResponse!: (response: Response) => void;
    const fetchImpl = vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveResponse = resolve;
          })
      )
      .mockResolvedValue(new Response(JSON.stringify({ frame: { id: 'frame-2' } })));
    vi.stubGlobal('fetch', fetchImpl);
    const controller = new AbortController();
    const aborted = loadCachedSynonBiomedFrame('frame-2', { signal: controller.signal });
    const retained = loadCachedSynonBiomedFrame('frame-2');
    controller.abort();
    resolveResponse(new Response(JSON.stringify({ frame: { id: 'frame-2' } })));

    await expect(aborted).rejects.toMatchObject({ name: 'AbortError' });
    await expect(retained).resolves.toEqual({ frame: { id: 'frame-2' } });
    expect(fetchImpl).toHaveBeenCalledTimes(1);

    invalidateSynonBiomedFrameReads('frame-2');
    await loadCachedSynonBiomedFrame('frame-2');
    expect(fetchImpl).toHaveBeenCalledTimes(2);
  });

  it('evicts a shared request after the bounded transport timeout', async () => {
    vi.useFakeTimers();
    try {
      const fetchImpl = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
        return new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener(
            'abort',
            () => reject(new DOMException('Synon Biomed request timed out', 'TimeoutError')),
            { once: true }
          );
        });
      });
      vi.stubGlobal('fetch', fetchImpl);

      const pending = loadCachedSynonBiomedFrame('frame-timeout');
      const pendingRejection = expect(pending).rejects.toMatchObject({ name: 'TimeoutError' });
      await vi.advanceTimersByTimeAsync(15_000);

      await pendingRejection;
      expect(fetchImpl).toHaveBeenCalledTimes(1);

      const retry = loadCachedSynonBiomedFrame('frame-timeout');
      const retryRejection = expect(retry).rejects.toMatchObject({ name: 'TimeoutError' });
      expect(fetchImpl).toHaveBeenCalledTimes(2);
      await vi.advanceTimersByTimeAsync(15_000);
      await retryRejection;
    } finally {
      vi.useRealTimers();
    }
  });
});
