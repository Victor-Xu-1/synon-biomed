import { describe, expect, it } from 'vitest';
import {
  loadSynonBiomedContactEmail,
  loadSynonBiomedNetworkSettings,
  loadSynonBiomedPermissionGrants,
  loadSynonBiomedSecrets,
  loadSynonBiomedStorageSettings,
  loadSynonBiomedUseIntent,
} from '@/renderer/services/synonBiomedWorkspaceSettings';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

const backendBaseUrl = process.env.SYNON_BIOMED_BACKEND_URL ?? 'http://127.0.0.1:8766';
const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';

describe('Synon Biomed workspace settings real integration', () => {
  it('reads network, permission, credential and storage state from the live v1.1-compatible backend', async () => {
    const options = {
      baseUrl: backendBaseUrl,
      fetchImpl: await createSynonBiomedTestFetch(backendBaseUrl),
    };
    const [network, grants, secrets, storage, intent, contact] = await Promise.all([
      loadSynonBiomedNetworkSettings(options),
      loadSynonBiomedPermissionGrants(options),
      loadSynonBiomedSecrets(options),
      loadSynonBiomedStorageSettings(options),
      loadSynonBiomedUseIntent(options),
      loadSynonBiomedContactEmail(options),
    ]);

    expect(network.groups.length).toBeGreaterThanOrEqual(6);
    expect(network.groups.some((group) => group.id === 'pkg' && group.locked)).toBe(true);
    expect(Array.isArray(grants)).toBe(true);
    expect(grants.every((grant) => grant.kind.length > 0 && grant.key.length > 0)).toBe(true);
    expect(Array.isArray(secrets)).toBe(true);
    expect(storage.dataDirectory.current).toMatch(/^\//);
    expect(storage.diskUsage.availableBytes).toBeGreaterThan(0);
    expect(Array.isArray(storage.cloudCredentials)).toBe(true);
    expect(['commercial', 'noncommercial']).toContain(intent.intent);
    expect(contact.noticeVersion.length).toBeGreaterThan(0);
  }, 30_000);

  it('reads the same private settings through the authenticated production WebHost bridge', async () => {
    const fetchImpl = await createSynonBiomedTestFetch(gatewayBaseUrl);
    const options = { baseUrl: gatewayBaseUrl, fetchImpl };
    const [network, storage, intent, contact] = await Promise.all([
      loadSynonBiomedNetworkSettings(options),
      loadSynonBiomedStorageSettings(options),
      loadSynonBiomedUseIntent(options),
      loadSynonBiomedContactEmail(options),
    ]);

    expect(network.groups.some((group) => group.id === 'pkg')).toBe(true);
    expect(storage.dataDirectory.current).toMatch(/^\//);
    expect(['commercial', 'noncommercial']).toContain(intent.intent);
    expect(contact.noticeVersion.length).toBeGreaterThan(0);
  }, 30_000);
});
