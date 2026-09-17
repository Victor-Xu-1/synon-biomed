import { describe, expect, it, vi } from 'vitest';
import {
  loadSynonBiomedAutoMemoryEnabled,
  loadSynonBiomedMemoryEnabled,
  setSynonBiomedAutoMemoryEnabled,
} from '@/renderer/services/synonBiomedMemory';

function json(data: unknown): Response {
  return new Response(JSON.stringify(data), { status: 200, headers: { 'content-type': 'application/json' } });
}

describe('Synon Biomed memory settings service', () => {
  it('keeps automatic extraction on a dedicated preference without changing the master memory endpoint', async () => {
    const fetchImpl = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/api/memory/auto-enabled') && init?.method === 'PUT') return json({ enabled: false });
      if (url.endsWith('/api/memory/auto-enabled')) return json({ enabled: true });
      if (url.endsWith('/api/memory/enabled')) return json({ enabled: true });
      return json({ detail: 'not found' });
    });

    await expect(loadSynonBiomedMemoryEnabled({ fetchImpl })).resolves.toBe(true);
    await expect(loadSynonBiomedAutoMemoryEnabled({ fetchImpl })).resolves.toBe(true);
    await setSynonBiomedAutoMemoryEnabled(false, { fetchImpl });

    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/memory/auto-enabled',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify({ enabled: false }) })
    );
    expect(fetchImpl).not.toHaveBeenCalledWith('/api/memory/enabled', expect.objectContaining({ method: 'PUT' }));
  });
});
