import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  loadSynonBiomedAssistantComposerCapabilities,
  loadSynonBiomedComposerCapabilities,
  loadSynonBiomedProjectBenches,
} from '@/renderer/services/synonBiomedGateway';

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

describe('Synon Biomed project gateway request authority', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('coalesces only concurrent default bench reads and revalidates after settlement', async () => {
    const first = deferred<Response>();
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockImplementationOnce(() => first.promise)
      .mockResolvedValueOnce(
        new Response(JSON.stringify([{ id: 'frame-a', root_frame_id: 'frame-a', name: 'Bench A' }]), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        })
      );
    vi.stubGlobal('fetch', fetchMock);

    const routeRead = loadSynonBiomedProjectBenches('project-a');
    const sidebarRead = loadSynonBiomedProjectBenches('project-a');
    expect(fetchMock).toHaveBeenCalledTimes(1);

    first.resolve(
      new Response(JSON.stringify([{ id: 'frame-a', root_frame_id: 'frame-a', name: 'Bench A' }]), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      })
    );
    await expect(Promise.all([routeRead, sidebarRead])).resolves.toEqual([
      [expect.objectContaining({ frameId: 'frame-a' })],
      [expect.objectContaining({ frameId: 'frame-a' })],
    ]);

    await loadSynonBiomedProjectBenches('project-a');
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('does not retain a failed bench request', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(null, { status: 503 }))
      .mockResolvedValueOnce(
        new Response(JSON.stringify([{ id: 'frame-b', root_frame_id: 'frame-b', name: 'Bench B' }]), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        })
      );
    vi.stubGlobal('fetch', fetchMock);

    await expect(loadSynonBiomedProjectBenches('project-b')).rejects.toThrow('503');
    await expect(loadSynonBiomedProjectBenches('project-b')).resolves.toEqual([
      expect.objectContaining({ frameId: 'frame-b' }),
    ]);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('normalizes the exact composer Skill and MCP authority returned by the conversation endpoint', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          skills: ['single-cell-analysis', ' single-cell-analysis ', '', 12],
          mcp_statuses: [
            { id: 'pubmed-id', name: 'PubMed', status: 'loaded' },
            { id: 'PUBMED-ID', name: 'duplicate', status: 'loaded' },
            { id: 'broken-id', name: 'Broken', status: 'unknown' },
          ],
        }),
        { status: 200, headers: { 'content-type': 'application/json' } }
      )
    );

    await expect(loadSynonBiomedComposerCapabilities('conversation/a', { fetchImpl: fetchMock })).resolves.toEqual({
      skills: ['single-cell-analysis'],
      mcpStatuses: [{ id: 'pubmed-id', name: 'PubMed', status: 'loaded' }],
    });
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/conversations/conversation%2Fa/composer-capabilities',
      expect.objectContaining({ headers: { Accept: 'application/json' } })
    );
  });

  it('uses the same normalized authority contract for a new assistant draft', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          skills: ['autodock-vina'],
          mcp_statuses: [{ id: 'bundled:pubmed', name: 'PubMed', status: 'loaded' }],
        }),
        { status: 200, headers: { 'content-type': 'application/json' } }
      )
    );

    await expect(
      loadSynonBiomedAssistantComposerCapabilities('synonbiomed:operon', { fetchImpl: fetchMock })
    ).resolves.toEqual({
      skills: ['autodock-vina'],
      mcpStatuses: [{ id: 'bundled:pubmed', name: 'PubMed', status: 'loaded' }],
    });
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/assistants/synonbiomed%3Aoperon/composer-capabilities',
      expect.objectContaining({ headers: { Accept: 'application/json' } })
    );
  });
});
