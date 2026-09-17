import { describe, expect, it, vi } from 'vitest';
import { loadSynonBiomedThirdPartyLicenses } from '@/renderer/services/synonBiomedLicenses';

const snapshot = {
  title: 'Third-Party Licenses',
  source: 'runtime/assets/skills/THIRD_PARTY_LICENSES.md',
  bytes: 42,
  sha256: 'a'.repeat(64),
  content: '# Third-Party Licenses\n',
};

describe('synonBiomedLicenses service', () => {
  it('loads and validates the frontend-hosted runtime license inventory', async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValue(
        new Response(JSON.stringify(snapshot), { status: 200, headers: { 'content-type': 'application/json' } })
      );

    await expect(loadSynonBiomedThirdPartyLicenses({ baseUrl: 'http://127.0.0.1:28081/', fetchImpl })).resolves.toEqual(
      snapshot
    );
    expect(fetchImpl).toHaveBeenCalledWith(
      'http://127.0.0.1:28081/api/synonbiomed/licenses/third-party',
      expect.objectContaining({ credentials: 'same-origin', headers: { accept: 'application/json' } })
    );
  });

  it('rejects unavailable and malformed inventories instead of showing partial legal copy', async () => {
    const unavailableFetch = vi.fn().mockResolvedValue(new Response('{}', { status: 502 }));
    await expect(loadSynonBiomedThirdPartyLicenses({ fetchImpl: unavailableFetch })).rejects.toThrow('(502)');

    const malformedFetch = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify({ ...snapshot, sha256: 'not-a-checksum' }), { status: 200 }));
    await expect(loadSynonBiomedThirdPartyLicenses({ fetchImpl: malformedFetch })).rejects.toThrow(
      'response is invalid'
    );
  });
});
