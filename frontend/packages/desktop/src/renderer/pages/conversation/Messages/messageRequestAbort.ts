const MESSAGE_REQUEST_ABORT_REASONS = new Set([
  'message_window_replaced',
  'conversation_history_load_aborted',
  'message_history_superseded',
  'message_history_invalidated',
]);

export function isMessageRequestAbort(error: unknown): boolean {
  if (typeof error === 'string') return MESSAGE_REQUEST_ABORT_REASONS.has(error);
  if (!error || typeof error !== 'object') return false;

  const candidate = error as { name?: unknown; message?: unknown };
  return (
    candidate.name === 'AbortError' ||
    (typeof candidate.message === 'string' && MESSAGE_REQUEST_ABORT_REASONS.has(candidate.message))
  );
}
