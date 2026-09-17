/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { MessageCursorPage } from '@/common/adapter/ipcBridge';
import type { TMessage } from '@/common/chat/chatLib';
import {
  INTERACTIVE_MESSAGE_PAGE_LIMIT,
  loadLatestConversationMessages,
  type MessageContentMode,
} from '@/renderer/utils/chat/messagePagination';
import { registerRendererAccountReset } from '@/renderer/services/rendererAccountScope';

export const MESSAGE_WINDOW_DEDUPE_MS = 30_000;
export const MESSAGE_WINDOW_SETTLED_REUSE_MS = 500;
export const MAX_MESSAGE_WINDOW_REQUESTS = 16;

export type MessageWindowRequest = {
  ownerId: string;
  conversationId: string;
  branchRevision: number;
  branchId?: string;
  limit?: number;
  contentMode?: MessageContentMode;
  force?: boolean;
  /**
   * A live transcript refresh may share the initial window that is already
   * converging. Terminal and branch refreshes keep their force semantics.
   */
  dedupeInFlight?: boolean;
  /** Interactive initial/live reads surface transient unavailability immediately. */
  retryTransient?: boolean;
  /** Retain one successful click-owned response for the mounting route. */
  prefetch?: boolean;
};

type MessageWindowRequestEntry = {
  request: Readonly<MessageWindowRequest>;
  startedAt: number;
  controller: AbortController;
  promise: Promise<MessageCursorPage<TMessage>>;
  cleanupTimer: ReturnType<typeof setTimeout>;
  prefetched: boolean;
  settled: boolean;
};

const messageWindowRequests = new Map<string, MessageWindowRequestEntry>();

function messageWindowRequestKey(request: MessageWindowRequest): string {
  return JSON.stringify([
    request.ownerId.trim(),
    request.conversationId,
    request.branchRevision,
    request.branchId ?? '',
    request.limit ?? INTERACTIVE_MESSAGE_PAGE_LIMIT,
    request.contentMode ?? 'compact',
    request.retryTransient !== false,
  ]);
}

function pruneMessageWindowRequests(now: number): void {
  for (const [key, entry] of messageWindowRequests) {
    if (now - entry.startedAt >= MESSAGE_WINDOW_DEDUPE_MS) removeMessageWindowRequest(key, entry, true);
  }
}

function removeMessageWindowRequest(key: string, expected?: MessageWindowRequestEntry, abort = false): void {
  const current = messageWindowRequests.get(key);
  if (!current || (expected && current !== expected)) return;
  clearTimeout(current.cleanupTimer);
  messageWindowRequests.delete(key);
  if (abort && !current.settled && !current.controller.signal.aborted) {
    current.controller.abort(new DOMException('message_window_replaced', 'AbortError'));
  }
}

function retainSettledMessageWindowRequest(key: string, entry: MessageWindowRequestEntry): void {
  clearTimeout(entry.cleanupTimer);
  entry.cleanupTimer = setTimeout(() => removeMessageWindowRequest(key, entry), MESSAGE_WINDOW_SETTLED_REUSE_MS);
}

function evictOldestMessageWindowRequest(): void {
  let oldest: [string, MessageWindowRequestEntry] | undefined;
  for (const entry of messageWindowRequests) {
    if (!oldest || entry[1].startedAt < oldest[1].startedAt) oldest = entry;
  }
  if (oldest) removeMessageWindowRequest(oldest[0], oldest[1], true);
}

export function clearMessageWindowRequests(): void {
  for (const [key, entry] of messageWindowRequests) removeMessageWindowRequest(key, entry, true);
}

registerRendererAccountReset('message-window-requests', clearMessageWindowRequests);

export function cancelConversationMessageRequests(ownerId: string, conversationId: string): void {
  const ownerKey = ownerId.trim();
  for (const [key, entry] of messageWindowRequests) {
    if (entry.request.ownerId.trim() !== ownerKey || entry.request.conversationId !== conversationId) continue;
    if (entry.settled) continue;
    removeMessageWindowRequest(key, entry, true);
  }
}

export function loadLatestConversationMessagesShared(
  request: MessageWindowRequest
): Promise<MessageCursorPage<TMessage>> {
  if (!request.ownerId.trim()) {
    return loadLatestConversationMessages(request.conversationId, {
      limit: request.limit ?? INTERACTIVE_MESSAGE_PAGE_LIMIT,
      contentMode: request.contentMode ?? 'compact',
      ...(request.branchId ? { branchId: request.branchId } : {}),
      retryTransient: request.retryTransient,
    });
  }
  const now = Date.now();
  pruneMessageWindowRequests(now);
  const key = messageWindowRequestKey(request);
  const candidate = messageWindowRequests.get(key);
  const existing = request.force
    ? request.dedupeInFlight && candidate && !candidate.settled
      ? candidate
      : undefined
    : candidate;
  if (existing) {
    // A click-owned prefetch is consumed exactly once by the mounting route.
    // Keeping the settled promise until that point closes the race where a
    // fast response finished before React mounted and immediately fetched the
    // same large transcript again.
    const promise = existing.promise;
    if (!request.prefetch && existing.prefetched) {
      existing.prefetched = false;
      if (existing.settled) retainSettledMessageWindowRequest(key, existing);
    }
    return promise;
  }

  if (request.force) removeMessageWindowRequest(key, undefined, true);
  while (messageWindowRequests.size >= MAX_MESSAGE_WINDOW_REQUESTS) {
    evictOldestMessageWindowRequest();
  }

  const controller = new AbortController();
  let entry: MessageWindowRequestEntry;
  const promise = loadLatestConversationMessages(request.conversationId, {
    limit: request.limit ?? INTERACTIVE_MESSAGE_PAGE_LIMIT,
    contentMode: request.contentMode ?? 'compact',
    ...(request.branchId ? { branchId: request.branchId } : {}),
    retryTransient: request.retryTransient,
    signal: controller.signal,
  }).then(
    (page) => {
      entry.settled = true;
      if (!entry.prefetched) retainSettledMessageWindowRequest(key, entry);
      return page;
    },
    (error) => {
      entry.settled = true;
      removeMessageWindowRequest(key, entry);
      throw error;
    }
  );
  entry = {
    request: { ...request },
    startedAt: now,
    controller,
    promise,
    cleanupTimer: setTimeout(() => removeMessageWindowRequest(key, entry, true), MESSAGE_WINDOW_DEDUPE_MS),
    prefetched: request.prefetch === true,
    settled: false,
  };
  messageWindowRequests.set(key, entry);
  return promise;
}

export async function prefetchConversationMessages(request: MessageWindowRequest): Promise<void> {
  if (!request.ownerId.trim() || !request.conversationId) return;
  await loadLatestConversationMessagesShared({ ...request, prefetch: true });
}
