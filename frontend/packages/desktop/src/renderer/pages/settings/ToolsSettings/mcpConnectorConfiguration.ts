import type { SynonBiomedMcpServer } from '@/renderer/services/synonBiomedCapabilities';

export function connectorConfigurationState(server: SynonBiomedMcpServer) {
  if (!server.enabled) return 'disabled';
  if (server.connectionStatus === 'connecting') return 'connecting';
  if (server.connectionStatus === 'connected' && server.health.ok) return 'connected';
  const requiresAuth =
    server.connectionStatus === 'auth_required' || ['required', 'unauthorized'].includes(server.authState);
  if (requiresAuth) {
    if (server.apiKeyConfigured) return 'authorizationFailed';
    return server.apiKeyConfigurable && !server.oauthSupported ? 'keyMissing' : 'authorizationRequired';
  }
  return 'connectionFailed';
}

export type ConnectorProviderLink = { name: string; url: string; kind: 'account' | 'documentation' };

export function connectorProviderLinks(upstreams: unknown[]): ConnectorProviderLink[] {
  const result: ConnectorProviderLink[] = [];
  const seen = new Set<string>();
  for (const value of upstreams) {
    if (!value || typeof value !== 'object' || Array.isArray(value)) continue;
    const entry = value as Record<string, unknown>;
    const name = typeof entry.name === 'string' ? entry.name : '';
    for (const [kind, raw] of [
      ['account', entry.credentialUrl || entry.homepageUrl],
      ['documentation', entry.infoUrl],
    ] as const) {
      const url = safeProviderUrl(raw);
      if (!url || seen.has(url)) continue;
      seen.add(url);
      result.push({ name, url, kind });
    }
  }
  return result;
}

function safeProviderUrl(value: unknown): string | null {
  if (typeof value !== 'string' || value.length > 2048) return null;
  try {
    const url = new URL(value);
    if (url.protocol !== 'https:' || url.username || url.password) return null;
    return url.toString();
  } catch {
    return null;
  }
}

export function validConnectorKey(value: string): boolean {
  const trimmed = value.trim();
  return trimmed.length > 0 && !/[\r\n]/.test(trimmed) && new TextEncoder().encode(trimmed).length <= 16 * 1024;
}
