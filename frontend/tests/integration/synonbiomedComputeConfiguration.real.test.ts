import { loadSynonBiomedBioNemoSettings, loadSynonBiomedModalSettings } from '@/renderer/services/synonBiomedCompute';
import { describe, expect, it } from 'vitest';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';

describe('Synon Biomed real compute configuration gateway', () => {
  it('loads the native v1.1 Modal and BioNeMo configuration contracts', async () => {
    const authenticatedFetch = await createSynonBiomedTestFetch(gatewayBaseUrl);
    const fetchImpl: typeof fetch = (input, init) => {
      const endpoint = typeof input === 'string' && input.startsWith('/') ? `${gatewayBaseUrl}${input}` : input;
      return authenticatedFetch(endpoint, init);
    };

    const [modal, bioNemo] = await Promise.all([
      loadSynonBiomedModalSettings(fetchImpl),
      loadSynonBiomedBioNemoSettings(fetchImpl),
    ]);

    expect(modal).toMatchObject({
      provider: 'modal',
      enabled: expect.any(Boolean),
      profiles: expect.any(Array),
      tomlMissing: expect.any(Boolean),
      hasStoredCredential: expect.any(Boolean),
    });
    expect(['unrestricted', 'allowlist', 'blocked', null]).toContain(modal.egressPolicy?.mode ?? null);
    expect(bioNemo).toMatchObject({
      enabled: expect.any(Boolean),
      mode: expect.stringMatching(/^(hosted|local)$/),
      hostedHost: expect.any(String),
    });
  });
});
