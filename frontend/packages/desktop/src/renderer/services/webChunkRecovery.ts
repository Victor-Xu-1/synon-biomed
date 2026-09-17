const RECOVERY_MARKER_KEY = 'synon.webChunkRecovery.v1';
const DEFAULT_RECOVERY_GUARD_MS = 5 * 60 * 1000;

type RecoveryStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;
type RecoveryEventTarget = Pick<Window, 'addEventListener' | 'removeEventListener'>;

type RecoveryMarker = {
  href: string;
  attemptedAt: number;
};

type RecoveryMarkerRead = {
  available: boolean;
  marker?: RecoveryMarker;
};

export type WebChunkRecoveryOptions = {
  target?: RecoveryEventTarget;
  storage?: RecoveryStorage;
  href?: () => string;
  reload?: () => void;
  now?: () => number;
  schedule?: (callback: () => void) => unknown;
  guardMs?: number;
};

const chunkFailurePatterns = [
  /failed to fetch dynamically imported module/iu,
  /importing a module script failed/iu,
  /error loading dynamically imported module/iu,
  /loading chunk .+ failed/iu,
  /chunkloaderror/iu,
];

function errorText(value: unknown): string {
  if (value instanceof Error) return `${value.name}: ${value.message}`;
  return typeof value === 'string' ? value : '';
}

export function isWebChunkLoadFailure(value: unknown): boolean {
  const text = errorText(value);
  return text !== '' && chunkFailurePatterns.some((pattern) => pattern.test(text));
}

function readMarker(storage: RecoveryStorage, now: number, guardMs: number): RecoveryMarkerRead {
  try {
    const raw = storage.getItem(RECOVERY_MARKER_KEY);
    if (!raw) return { available: true };
    const marker = JSON.parse(raw) as Partial<RecoveryMarker>;
    if (
      typeof marker.href !== 'string' ||
      typeof marker.attemptedAt !== 'number' ||
      !Number.isFinite(marker.attemptedAt) ||
      marker.attemptedAt > now ||
      now - marker.attemptedAt > guardMs
    ) {
      storage.removeItem(RECOVERY_MARKER_KEY);
      return { available: true };
    }
    // The marker is tab-scoped rather than route-scoped. One failed deployment
    // can strand several lazy routes; changing hashes must not trigger a reload
    // loop as the user navigates between them.
    return { available: true, marker: { href: marker.href, attemptedAt: marker.attemptedAt } };
  } catch {
    return { available: false };
  }
}

function persistMarker(storage: RecoveryStorage, marker: RecoveryMarker): boolean {
  try {
    storage.setItem(RECOVERY_MARKER_KEY, JSON.stringify(marker));
    return true;
  } catch {
    // Reloading without a durable loop guard can trap the user in an endless
    // refresh cycle. Keep the current error visible when storage is unavailable.
    return false;
  }
}

/**
 * Recover one long-lived browser tab when a deployment replaces a lazy chunk.
 *
 * The deployment authority retains the previous content-addressed assets. This
 * client guard covers the remaining race: if a stale tab requests a missing
 * dynamic chunk, reload the exact current URL once so it adopts the new entry
 * graph. A session marker prevents another automatic reload for the same URL
 * during the guard window. No ordinary application error triggers a reload.
 */
export function installWebChunkRecovery(options: WebChunkRecoveryOptions = {}): () => void {
  const target = options.target ?? window;
  const storage = options.storage ?? window.sessionStorage;
  const currentHref = options.href ?? (() => window.location.href);
  const reload = options.reload ?? (() => window.location.reload());
  const now = options.now ?? (() => Date.now());
  const schedule = options.schedule ?? ((callback) => window.setTimeout(callback, 0));
  const guardMs = options.guardMs ?? DEFAULT_RECOVERY_GUARD_MS;
  let recoveryScheduled = false;

  const recover = (reason: unknown, event: Event): void => {
    if (recoveryScheduled || !isWebChunkLoadFailure(reason)) return;
    const href = currentHref();
    const timestamp = now();
    const marker = readMarker(storage, timestamp, guardMs);
    if (!marker.available || marker.marker) return;
    if (!persistMarker(storage, { href, attemptedAt: timestamp })) return;
    recoveryScheduled = true;
    if (event.cancelable) event.preventDefault();
    schedule(reload);
  };

  const onPreloadError: EventListener = (event) => {
    // Vite emits this event only for a failed dependency preload. Browser
    // payload text varies (network error, HTML MIME mismatch, CSP refusal), so
    // the typed event is the authority instead of a brittle message classifier.
    recover('Failed to fetch dynamically imported module', event);
  };
  const onUnhandledRejection: EventListener = (event) => {
    recover((event as PromiseRejectionEvent).reason, event);
  };

  target.addEventListener('vite:preloadError', onPreloadError);
  target.addEventListener('unhandledrejection', onUnhandledRejection);
  return () => {
    target.removeEventListener('vite:preloadError', onPreloadError);
    target.removeEventListener('unhandledrejection', onUnhandledRejection);
  };
}
