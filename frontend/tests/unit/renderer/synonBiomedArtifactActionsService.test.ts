import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  deleteSynonBiomedArtifact,
  renameSynonBiomedArtifact,
  setSynonBiomedArtifactPriority,
} from '@/renderer/services/synonBiomedArtifacts';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('Synon Biomed artifact actions service', () => {
  it('uses the exact priority, rename, and delete artifact contracts', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: 'artifact 1', priority: 'user_starred' })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ artifact_id: 'artifact 1', filename: 'report final.csv' })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ artifact_id: 'artifact 1', deleted: true })));
    vi.stubGlobal('fetch', fetchMock);

    await expect(
      setSynonBiomedArtifactPriority({ artifactId: 'artifact 1', priority: 'user_starred' })
    ).resolves.toMatchObject({ artifactId: 'artifact 1' });
    await expect(
      renameSynonBiomedArtifact({ artifactId: 'artifact 1', filename: 'report final.csv' })
    ).resolves.toMatchObject({
      artifactId: 'artifact 1',
      filename: 'report final.csv',
    });
    await expect(deleteSynonBiomedArtifact('artifact 1')).resolves.toMatchObject({ artifactId: 'artifact 1' });

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/artifacts/artifact%201/priority',
      expect.objectContaining({ method: 'PATCH', body: JSON.stringify({ priority: 'user_starred' }) })
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/artifacts/artifact%201/rename',
      expect.objectContaining({ method: 'PATCH', body: JSON.stringify({ filename: 'report final.csv' }) })
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/artifacts/artifact%201',
      expect.objectContaining({ method: 'DELETE' })
    );
    const deleteInit = fetchMock.mock.calls[2]?.[1] as RequestInit | undefined;
    expect(deleteInit?.body).toBeUndefined();
  });

  it('surfaces the existing requestJson error with server detail', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn<typeof fetch>()
        .mockResolvedValue(
          new Response(JSON.stringify({ detail: 'Artifact artifact-foreign not found' }), { status: 404 })
        )
    );

    await expect(deleteSynonBiomedArtifact('artifact-foreign')).rejects.toThrow(
      'Synon Biomed artifact request failed: 404 {"detail":"Artifact artifact-foreign not found"}'
    );
  });
});
