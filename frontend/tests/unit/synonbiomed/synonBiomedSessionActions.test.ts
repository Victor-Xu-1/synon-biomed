import {
  deleteSynonBiomedSession,
  getSynonBiomedSessionArtifactsDownloadUrl,
  getSynonBiomedSessionExportUrl,
  moveSynonBiomedSession,
  updateSynonBiomedSession,
} from '@/renderer/services/synonBiomedSessionActions';
import { describe, expect, it, vi } from 'vitest';

describe('Synon Biomed session actions', () => {
  it('uses the v1.1 frame contracts for edit, move, delete, and downloads', async () => {
    const fetchImpl = vi.fn(
      async () => new Response('{}', { status: 200, headers: { 'content-type': 'application/json' } })
    );
    const options = { baseUrl: 'http://gateway.test', fetchImpl };

    await updateSynonBiomedSession('frame/one', { name: 'Renamed', taskSummary: 'Updated summary' }, options);
    await moveSynonBiomedSession('frame/one', 'project-two', options);
    await deleteSynonBiomedSession('frame/one', options);

    expect(fetchImpl).toHaveBeenNthCalledWith(
      1,
      'http://gateway.test/api/frames/frame%2Fone',
      expect.objectContaining({
        method: 'PATCH',
        body: JSON.stringify({ name: 'Renamed', task_summary: 'Updated summary' }),
      })
    );
    expect(fetchImpl).toHaveBeenNthCalledWith(
      2,
      'http://gateway.test/api/frames/frame%2Fone/move',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ target_project_id: 'project-two' }),
      })
    );
    expect(fetchImpl).toHaveBeenNthCalledWith(
      3,
      'http://gateway.test/api/frames/frame%2Fone',
      expect.objectContaining({ method: 'DELETE' })
    );
    expect(getSynonBiomedSessionExportUrl('frame/one')).toBe('/api/frames/frame%2Fone/export');
    expect(getSynonBiomedSessionArtifactsDownloadUrl('frame/one')).toBe(
      '/api/frames/frame%2Fone/artifacts/download?include_metadata=true'
    );
  });
});
