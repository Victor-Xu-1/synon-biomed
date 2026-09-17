import { requestSynonBiomedJson, type SynonBiomedGatewayOptions } from './synonBiomedHttp';
import { registerRendererAccountReset, rendererAccountScopedKey } from './rendererAccountScope';

const FRAME_READ_CACHE_TTL_MS = 30_000;
const FRAME_READ_REQUEST_TIMEOUT_MS = 15_000;
const MAX_RETAINED_FRAME_READS = 8;

type FrameReadEntry = {
  expiresAt: number;
  promise: Promise<unknown>;
};

const frameReads = new Map<string, FrameReadEntry>();

function frameReadKey(kind: 'frame' | 'artifacts', frameId: string): string {
  return rendererAccountScopedKey(`${kind}:${frameId}`);
}

function trimFrameReads(): void {
  while (frameReads.size > MAX_RETAINED_FRAME_READS) {
    const oldest = frameReads.keys().next().value;
    if (typeof oldest !== 'string') return;
    frameReads.delete(oldest);
  }
}

function waitForFrameRead(promise: Promise<unknown>, signal?: AbortSignal): Promise<unknown> {
  if (!signal) return promise;
  if (signal.aborted) return Promise.reject(new DOMException('The request was aborted', 'AbortError'));
  return new Promise((resolve, reject) => {
    const abort = () => reject(new DOMException('The request was aborted', 'AbortError'));
    signal.addEventListener('abort', abort, { once: true });
    promise.then(resolve, reject).finally(() => signal.removeEventListener('abort', abort));
  });
}

function readFrameResource(
  kind: 'frame' | 'artifacts',
  frameId: string,
  options: SynonBiomedGatewayOptions
): Promise<unknown> {
  // Custom transports and explicit origins are isolation boundaries. They
  // must not share browser-origin response state with the production cache.
  if (options.fetchImpl || options.baseUrl) {
    const suffix = kind === 'artifacts' ? '/artifacts' : '';
    return requestSynonBiomedJson<unknown>(`/api/frames/${encodeURIComponent(frameId)}${suffix}`, {}, options);
  }

  const key = frameReadKey(kind, frameId);
  const now = Date.now();
  const current = frameReads.get(key);
  if (current && current.expiresAt > now) {
    frameReads.delete(key);
    frameReads.set(key, current);
    return waitForFrameRead(current.promise, options.signal);
  }
  if (current) frameReads.delete(key);

  const suffix = kind === 'artifacts' ? '/artifacts' : '';
  const promise = requestSynonBiomedJson<unknown>(
    `/api/frames/${encodeURIComponent(frameId)}${suffix}`,
    {},
    { timeoutMs: FRAME_READ_REQUEST_TIMEOUT_MS }
  ).catch((error: unknown) => {
    if (frameReads.get(key)?.promise === promise) frameReads.delete(key);
    throw error;
  });
  frameReads.set(key, { promise, expiresAt: now + FRAME_READ_CACHE_TTL_MS });
  trimFrameReads();
  return waitForFrameRead(promise, options.signal);
}

export function loadCachedSynonBiomedFrame(frameId: string, options: SynonBiomedGatewayOptions = {}): Promise<unknown> {
  return readFrameResource('frame', frameId, options);
}

export function loadCachedSynonBiomedFrameArtifacts(
  frameId: string,
  options: SynonBiomedGatewayOptions = {}
): Promise<unknown> {
  return readFrameResource('artifacts', frameId, options);
}

export function invalidateSynonBiomedFrameReads(frameId: string): void {
  frameReads.delete(frameReadKey('frame', frameId));
  frameReads.delete(frameReadKey('artifacts', frameId));
}

export function clearSynonBiomedFrameReads(): void {
  frameReads.clear();
}

registerRendererAccountReset('synon-biomed-frame-reads', clearSynonBiomedFrameReads);
