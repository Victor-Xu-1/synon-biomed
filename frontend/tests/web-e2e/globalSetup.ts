import { createSynonBiomedTestFetch } from '../integration/synonbiomedTestAuth';
import {
  assertOfficialGoogleChrome,
  requireOfficialChromeEndpoint,
  requireOfficialChromeProfileId,
} from './officialChromeContract';

type CDPResult = Record<string, unknown>;

async function verifyConfiguredOfficialChrome(): Promise<void> {
  const configuredEndpoint = process.env.SYNON_GO_OFFICIAL_CHROME_CDP?.trim();
  if (!configuredEndpoint) return;
  const endpoint = requireOfficialChromeEndpoint(configuredEndpoint);
  const profileId = requireOfficialChromeProfileId(process.env.SYNON_GO_OFFICIAL_CHROME_PROFILE_ID);
  const socket = new WebSocket(endpoint);
  const pending = new Map<
    number,
    { resolve: (value: CDPResult) => void; reject: (error: Error) => void; timeout: ReturnType<typeof setTimeout> }
  >();
  const rejectPending = () => {
    for (const request of pending.values()) {
      clearTimeout(request.timeout);
      request.reject(new Error('official Chrome CDP disconnected before identity verification'));
    }
    pending.clear();
  };
  socket.addEventListener('message', (event) => {
    if (typeof event.data !== 'string') return;
    let response: { id?: unknown; result?: unknown; error?: unknown };
    try {
      response = JSON.parse(event.data) as typeof response;
    } catch {
      return;
    }
    if (typeof response.id !== 'number') return;
    const request = pending.get(response.id);
    if (!request) return;
    pending.delete(response.id);
    clearTimeout(request.timeout);
    if (response.error || !response.result || typeof response.result !== 'object') {
      request.reject(new Error('official Chrome CDP identity command failed'));
      return;
    }
    request.resolve(response.result as CDPResult);
  });
  socket.addEventListener('close', rejectPending);
  socket.addEventListener('error', rejectPending);
  try {
    await new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error('official Chrome CDP connection timed out')), 5_000);
      socket.addEventListener(
        'open',
        () => {
          clearTimeout(timeout);
          resolve();
        },
        { once: true }
      );
      socket.addEventListener(
        'error',
        () => {
          clearTimeout(timeout);
          reject(new Error('official Chrome CDP connection failed'));
        },
        { once: true }
      );
    });
    let requestId = 0;
    const call = (method: string) =>
      new Promise<CDPResult>((resolve, reject) => {
        const id = ++requestId;
        const timeout = setTimeout(() => {
          pending.delete(id);
          reject(new Error('official Chrome CDP identity command timed out'));
        }, 5_000);
        pending.set(id, { resolve, reject, timeout });
        socket.send(JSON.stringify({ id, method }));
      });
    const [version, commandLine] = await Promise.all([
      call('Browser.getVersion'),
      call('Browser.getBrowserCommandLine'),
    ]);
    assertOfficialGoogleChrome(
      {
        brands: [],
        commandLine: Array.isArray(commandLine.arguments)
          ? commandLine.arguments.filter((value): value is string => typeof value === 'string')
          : [],
        product: typeof version.product === 'string' ? version.product : '',
      },
      profileId
    );
  } finally {
    rejectPending();
    socket.close();
  }
}

export default async function globalSetup(): Promise<void> {
  await verifyConfiguredOfficialChrome();
  if (process.env.SYNON_GO_E2E_KEEP_ONBOARDING === '1') return;
  const baseUrl = process.env.SYNON_GO_WEB_URL?.trim() || 'http://127.0.0.1:8080';
  const fetchImpl = await createSynonBiomedTestFetch(baseUrl);
  for (const path of [
    '/api/preferences/builtin-allowlist/onboarding-seen',
    '/api/preferences/first-run-onboarding/complete',
  ]) {
    const response = await fetchImpl(`${baseUrl}${path}`, { method: 'POST' });
    if (!response.ok) {
      throw new Error(`Playwright global setup failed for ${path}: ${response.status} ${await response.text()}`);
    }
  }
}
