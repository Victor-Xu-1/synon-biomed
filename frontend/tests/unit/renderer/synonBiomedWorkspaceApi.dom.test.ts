import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  deleteSynonBiomedArtifact,
  renameSynonBiomedArtifact,
  uploadSynonBiomedArtifact,
} from '@/renderer/services/synonBiomedWorkspaceApi';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('Synon Biomed workspace API', () => {
  it('uses conversation-scoped mutation routes for rename and delete', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify({ artifact_id: 'artifact-report', filename: 'renamed.md' })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ deleted: true })));
    vi.stubGlobal('fetch', fetchMock);

    await renameSynonBiomedArtifact({
      conversationId: 'frame-stat6',
      artifactId: 'artifact-report',
      filename: 'renamed.md',
    });
    await deleteSynonBiomedArtifact({ conversationId: 'frame-stat6', artifactId: 'artifact-report' });

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/conversations/frame-stat6/workspace/artifacts/artifact-report',
      expect.objectContaining({ method: 'PATCH', body: JSON.stringify({ filename: 'renamed.md' }) })
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/conversations/frame-stat6/workspace/artifacts/artifact-report',
      expect.objectContaining({ method: 'DELETE' })
    );
  });

  it('uploads a file through the real Synon Biomed chunk lifecycle and reports progress', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify({ upload_id: 'upload-1', chunk_size: 4 })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ received: 4 })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ received: 6 })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ artifact_id: 'artifact-uploaded', filename: 'assay.csv' })));
    vi.stubGlobal('fetch', fetchMock);
    const progress = vi.fn();
    const file = new File(['abcdef'], 'assay.csv', { type: 'text/csv' });

    const result = await uploadSynonBiomedArtifact(file, 'frame-stat6', progress, { chunkSize: 4 });

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/conversations/frame-stat6/workspace/uploads',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ filename: 'assay.csv', total_size: 6, content_type: 'text/csv', chunk_size: 4 }),
      })
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/conversations/frame-stat6/workspace/uploads/upload-1/chunks/0',
      expect.objectContaining({ method: 'POST', body: expect.any(Blob) })
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      4,
      '/api/conversations/frame-stat6/workspace/uploads/upload-1/finalize',
      expect.objectContaining({ method: 'POST' })
    );
    expect(progress).toHaveBeenLastCalledWith(100);
    expect(result).toMatchObject({ artifact_id: 'artifact-uploaded', filename: 'assay.csv' });
  });
});
