import { describe, expect, it, vi } from 'vitest';

import { deleteSynonBiomedProject } from '@/renderer/services/synonBiomedGateway';
import { deleteSynonBiomedSession } from '@/renderer/services/synonBiomedSessionActions';

const response = (status: number, body = '') =>
  new Response(body, {
    status,
    headers: body ? { 'content-type': 'application/json' } : undefined,
  });

describe('Synon Biomed deletion actions', () => {
  it('treats an already-removed session as an idempotent success', async () => {
    const fetchImpl = vi.fn<typeof fetch>().mockResolvedValue(response(404, '{"detail":"missing"}'));

    await expect(deleteSynonBiomedSession('frame-1', { fetchImpl })).resolves.toBeUndefined();
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/frames/frame-1',
      expect.objectContaining({ method: 'DELETE', credentials: 'include' })
    );
  });

  it('keeps a real session deletion failure visible to the caller', async () => {
    const fetchImpl = vi.fn<typeof fetch>().mockResolvedValue(response(500, '{"detail":"internal"}'));

    await expect(deleteSynonBiomedSession('frame-1', { fetchImpl })).rejects.toMatchObject({ status: 500 });
  });

  it('treats an already-removed project as an idempotent success', async () => {
    const fetchImpl = vi.fn<typeof fetch>().mockResolvedValue(response(404, '{"detail":"missing"}'));

    await expect(deleteSynonBiomedProject('project-1', { fetchImpl })).resolves.toBeUndefined();
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/projects/project-1',
      expect.objectContaining({ method: 'DELETE', credentials: 'include' })
    );
  });

  it('keeps a real project deletion failure visible to the caller', async () => {
    const fetchImpl = vi.fn<typeof fetch>().mockResolvedValue(response(500, '{"detail":"internal"}'));

    await expect(deleteSynonBiomedProject('project-1', { fetchImpl })).rejects.toMatchObject({ status: 500 });
  });
});
