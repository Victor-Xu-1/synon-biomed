import { loadSynonBiomedKernels } from '@/renderer/services/synonBiomedNotebook';
import { afterEach, describe, expect, it } from 'vitest';
import { createRealConversationFixture, type RealConversationFixture } from './synonbiomedRealConversationFixture';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
let fixture: RealConversationFixture | null = null;

afterEach(async () => {
  await fixture?.dispose();
  fixture = null;
});

describe('Synon Biomed notebook gateway', () => {
  it('returns the real v1.1 kernel inventory for a newly created frame', async () => {
    fixture = await createRealConversationFixture({ gatewayBaseUrl });

    await expect(
      loadSynonBiomedKernels(fixture.conversationId, {
        baseUrl: gatewayBaseUrl,
        fetchImpl: fixture.fetchImpl,
      })
    ).resolves.toEqual({ kernels: [], hasHistory: false, machine: null });
  });
});
