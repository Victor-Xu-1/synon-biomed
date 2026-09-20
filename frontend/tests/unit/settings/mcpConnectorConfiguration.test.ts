import { describe, expect, it } from 'vitest';
import type { SynonBiomedMcpServer } from '@/renderer/services/synonBiomedCapabilities';
import {
  connectorConfigurationState,
  connectorProviderLinks,
  validConnectorKey,
} from '@/renderer/pages/settings/ToolsSettings/mcpConnectorConfiguration';

describe('connector configuration presentation boundary', () => {
  const server = {
    enabled: true,
    health: { ok: false },
    authState: 'unauthorized',
    connectionStatus: 'auth_required',
    apiKeyConfigurable: true,
    apiKeyConfigured: false,
    oauthSupported: false,
  } as SynonBiomedMcpServer;

  it('distinguishes missing keys, OAuth, rejected credentials, transport failure and connection progress', () => {
    expect(connectorConfigurationState(server)).toBe('keyMissing');
    expect(connectorConfigurationState({ ...server, oauthSupported: true })).toBe('authorizationRequired');
    expect(connectorConfigurationState({ ...server, apiKeyConfigured: true })).toBe('authorizationFailed');
    expect(connectorConfigurationState({ ...server, authState: 'authorized', connectionStatus: 'failed' })).toBe(
      'connectionFailed'
    );
    expect(connectorConfigurationState({ ...server, connectionStatus: 'connecting' })).toBe('connecting');
    expect(connectorConfigurationState({ ...server, connectionStatus: 'connected', health: { ok: true } })).toBe(
      'connected'
    );
    expect(connectorConfigurationState({ ...server, enabled: false })).toBe('disabled');
    expect(connectorConfigurationState({ ...server, authState: 'authorized', connectionStatus: 'connected' })).toBe(
      'connectionFailed'
    );
  });

  it('uses canonical application metadata, deduplicates links and rejects unsafe destinations', () => {
    expect(
      connectorProviderLinks([
        {
          name: 'Provider',
          credentialUrl: 'https://provider.example/signup',
          homepageUrl: 'https://provider.example/',
          infoUrl: 'https://provider.example/docs',
        },
        { infoUrl: 'https://provider.example/docs' },
        { credentialUrl: 'javascript:alert(1)', infoUrl: 'https://user:secret@evil.example' },
        { infoUrl: 'http://plain.example' },
        null,
        [],
        'bad',
      ])
    ).toEqual([
      { name: 'Provider', kind: 'account', url: 'https://provider.example/signup' },
      { name: 'Provider', kind: 'documentation', url: 'https://provider.example/docs' },
    ]);
    expect(connectorProviderLinks([{ homepageUrl: 'https://provider.example' }])[0].url).toBe(
      'https://provider.example/'
    );
  });

  it('validates keys with the server single-line UTF-8 byte limit', () => {
    for (const value of ['', '  ', 'one\ntwo', 'one\rtwo', 'x'.repeat(16385), '字'.repeat(5462)])
      expect(validConnectorKey(value)).toBe(false);
    expect(validConnectorKey('  value  ')).toBe(true);
    expect(validConnectorKey('x'.repeat(16384))).toBe(true);
  });
});
