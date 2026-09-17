import { isBackendHttpError } from '@/common/adapter/httpBridge';

// Transcript projection is asynchronous after a tool result or terminal
// event. Keep the UI authoritative by waiting for the bounded projection
// window instead of turning a normal convergence gap into a user-visible
// history failure.
export const TRANSIENT_HISTORY_RETRY_DELAYS_MS = [
  250, 500, 1_000, 2_000, 4_000, 8_000, 12_000, 20_000, 30_000,
] as const;

export function isConversationHistoryNotReady(error: unknown): boolean {
  if (!isBackendHttpError(error)) return false;
  if (error.code === 'HISTORY_NOT_READY') return true;
  return error.status === 503 && error.backendMessage.toLowerCase().includes('transcript runtime is not available');
}

function sleepAbortable(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(signal.reason ?? new Error('conversation_history_load_aborted'));
      return;
    }
    const timer = setTimeout(resolve, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(signal.reason ?? new Error('conversation_history_load_aborted'));
    };
    signal?.addEventListener('abort', onAbort, { once: true });
  });
}

export async function withTransientHistoryRetry<T>(operation: () => Promise<T>, signal?: AbortSignal): Promise<T> {
  let attempt = 0;
  for (;;) {
    try {
      // Retries are deliberately sequential so each attempt observes the
      // transcript projection produced by the previous bounded wait.
      // eslint-disable-next-line no-await-in-loop
      return await operation();
    } catch (error) {
      if (!isConversationHistoryNotReady(error) || attempt >= TRANSIENT_HISTORY_RETRY_DELAYS_MS.length) {
        throw error;
      }
      const delayMs = TRANSIENT_HISTORY_RETRY_DELAYS_MS[attempt];
      attempt += 1;
      // eslint-disable-next-line no-await-in-loop
      await sleepAbortable(delayMs, signal);
    }
  }
}
