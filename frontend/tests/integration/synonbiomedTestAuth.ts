const DEFAULT_USERNAME = 'victor';
const DEFAULT_PASSWORD = '12345678';

export function resolveSynonBiomedTestUsername(): string {
  return process.env.SYNON_GO_WEB_USERNAME ?? process.env.SYNON_BIOMED_WEBUI_USERNAME ?? DEFAULT_USERNAME;
}

function resolveSynonBiomedTestPassword(): string {
  return process.env.SYNON_GO_WEB_PASSWORD ?? process.env.SYNON_BIOMED_WEBUI_PASSWORD ?? DEFAULT_PASSWORD;
}

export async function createSynonBiomedTestFetch(baseUrl: string): Promise<typeof fetch> {
  const normalizedBaseUrl = baseUrl.replace(/\/+$/, '');
  const gatewayOrigin = new URL(normalizedBaseUrl).origin;
  const login = await fetch(`${normalizedBaseUrl}/login`, {
    method: 'POST',
    headers: { accept: 'application/json', 'content-type': 'application/json' },
    body: JSON.stringify({
      username: resolveSynonBiomedTestUsername(),
      password: resolveSynonBiomedTestPassword(),
      remember: false,
    }),
  });
  if (!login.ok) {
    throw new Error(`Synon Biomed test login failed: ${login.status} ${await login.text()}`);
  }
  const setCookie = login.headers.get('set-cookie') ?? '';
  const session = setCookie.match(/(?:^|,\s*)(synon_session=[^;,\s]+)/)?.[1];
  const csrfToken = setCookie.match(/(?:^|,\s*)synon_csrf=([^;,\s]+)/)?.[1];
  if (!session || !csrfToken) {
    throw new Error('Synon Biomed test login did not return session and CSRF cookies');
  }

  return ((input: string | URL | Request, init: RequestInit = {}) => {
    const request = input instanceof Request ? input : undefined;
    const headers = new Headers(request?.headers);
    new Headers(init.headers).forEach((value, key) => headers.set(key, value));
    const target = new URL(request?.url ?? String(input), normalizedBaseUrl);
    if (target.origin === gatewayOrigin) {
      headers.set('cookie', `${session}; synon_csrf=${csrfToken}`);
      const method = (init.method ?? request?.method ?? 'GET').toUpperCase();
      if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
        headers.set('X-Synon-CSRF-Token', decodeURIComponent(csrfToken));
      }
    }
    return fetch(target, { ...init, headers });
  }) as typeof fetch;
}
