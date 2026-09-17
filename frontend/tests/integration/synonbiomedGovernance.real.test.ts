import { describe, expect, it } from 'vitest';
import { loadSynonBiomedGovernance } from '@/renderer/services/synonBiomedGovernance';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';

describe('Synon Biomed governance integration', () => {
  it('reads memory, approvals and provenance from the real backend', async () => {
    const snapshot = await loadSynonBiomedGovernance({
      baseUrl: gatewayBaseUrl,
      fetchImpl: await createSynonBiomedTestFetch(gatewayBaseUrl),
    });

    expect(snapshot.gateway.healthy).toBe(true);
    expect(snapshot.gateway.agentsRegistered).toBeGreaterThan(0);
    expect(typeof snapshot.memory.enabled).toBe('boolean');
    expect(snapshot.memory.totalRows).toBeGreaterThanOrEqual(0);
    expect(snapshot.approvals.total).toBeGreaterThanOrEqual(0);
    expect(snapshot.approvals.grants.every((grant) => grant.key.length > 0)).toBe(true);
    expect(snapshot.provenance.totalArtifacts).toBeGreaterThan(0);
    expect(snapshot.provenance.classes.some((item) => item.name === 'managed')).toBe(true);
    expect(snapshot.provenance.integrityIssues).toBeGreaterThanOrEqual(0);
  });
});
