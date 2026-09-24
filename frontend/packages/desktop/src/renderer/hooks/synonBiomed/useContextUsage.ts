/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { useCallback, useEffect, useState } from 'react';
import { fetchContextUsage, type ContextUsageResult } from '@/renderer/services/contextUsage';

export const CONTEXT_USAGE_TIMEOUT_MS = 8000;
export const CONTEXT_USAGE_POLL_MS = 5000;
type UsageState =
  | { conversationId: string; status: 'loading' | 'error' }
  | (ContextUsageResult & { conversationId: string });

/** One bounded request at a time; closing/switching aborts obsolete requests. */
export function useContextUsage(conversationId: string, visible: boolean, active: boolean) {
  const [state, setState] = useState<UsageState>({ conversationId, status: 'loading' });
  const [refresh, setRefresh] = useState(0);
  const retry = useCallback(() => setRefresh((value) => value + 1), []);
  useEffect(() => {
    let disposed = false;
    let poll: ReturnType<typeof setTimeout> | undefined;
    let controller: AbortController | undefined;
    let deadline: ReturnType<typeof setTimeout> | undefined;
    setState({ conversationId, status: conversationId ? 'loading' : 'unavailable' });
    const load = async () => {
      if (!conversationId) return;
      const requestController = new AbortController();
      controller = requestController;
      const stopped = new Promise<never>((_, reject) => {
        requestController.signal.addEventListener(
          'abort',
          () => reject(new DOMException('Context usage request aborted', 'AbortError')),
          { once: true }
        );
      });
      const timeout = new Promise<never>((_, reject) => {
        deadline = setTimeout(() => {
          requestController.abort();
          reject(new Error('Context usage request timed out'));
        }, CONTEXT_USAGE_TIMEOUT_MS);
      });
      try {
        // The deadline settles the UI even if a transport ignores AbortSignal.
        const result = await Promise.race([
          fetchContextUsage(conversationId, requestController.signal),
          stopped,
          timeout,
        ]);
        if (disposed) return;
        setState({ ...result, conversationId });
        // Poll only while the user is inspecting usage or the task is active.
        if (visible || active) poll = setTimeout(() => void load(), CONTEXT_USAGE_POLL_MS);
      } catch {
        if (!disposed) setState({ conversationId, status: 'error' });
        // No unbounded failure loop; reopening or Retry starts a new request.
      } finally {
        clearTimeout(deadline);
      }
    };
    void load();
    return () => {
      disposed = true;
      clearTimeout(poll);
      clearTimeout(deadline);
      controller?.abort();
    };
  }, [conversationId, visible, active, refresh]);
  return {
    state: state.conversationId === conversationId ? state : { conversationId, status: 'loading' as const },
    retry,
  };
}
