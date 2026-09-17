/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import { isBackendHttpError } from '@/common/adapter/httpBridge';
import type { TChatConversation } from '@/common/config/storage';
import { createKeyedSnapshotStore } from '@/renderer/services/keyedSnapshotStore';
import { registerRendererAccountReset, rendererAccountScopedKey } from '@/renderer/services/rendererAccountScope';
import type { useSWRConfig } from 'swr';

type ScopedMutator = ReturnType<typeof useSWRConfig>['mutate'];
type ConversationRouteSnapshot = {
  summary?: TChatConversation;
  detail?: TChatConversation;
  detailRememberedAt?: number;
};
const RECENT_ROUTE_DETAIL_MAX_AGE_MS = 5_000;
const conversationRouteSnapshots = createKeyedSnapshotStore<ConversationRouteSnapshot>({ maxEntries: 64 });
// All renderer detail reads share the same active request, not only the route
// shell and the Synon runtime reconciler. Titlebar/runtime hydration can mount
// in parallel with those consumers during a send; letting each caller invoke
// IPC independently creates avoidable duplicate full conversation snapshots.
const conversationDetailRequests = new Map<string, Promise<TChatConversation | null>>();
type ConversationDetailHandoff = {
  expiresAt: number;
  value?: TChatConversation | null;
};
const conversationDetailHandoffs = new Map<string, ConversationDetailHandoff>();

export function conversationCacheKey(conversationId: string): string {
  return `conversation/${conversationId}`;
}

function modifiedAt(conversation: TChatConversation): number | null {
  const value = Number(conversation.modified_at);
  return Number.isFinite(value) ? value : null;
}

export function preferConversationSnapshot(
  current: TChatConversation | null | undefined,
  candidate: TChatConversation
): TChatConversation {
  if (!current || current.id !== candidate.id) return candidate;
  const currentModifiedAt = modifiedAt(current);
  const candidateModifiedAt = modifiedAt(candidate);
  if (currentModifiedAt !== null && (candidateModifiedAt === null || currentModifiedAt > candidateModifiedAt)) {
    return current;
  }
  return candidate;
}

function conversationRouteSnapshotKey(ownerId: string, conversationId: string): string {
  return JSON.stringify([ownerId.trim(), conversationId]);
}

export function rememberConversationRouteSnapshot(
  ownerId: string,
  conversation: TChatConversation,
  source: 'summary' | 'detail' = 'summary'
): void {
  const normalizedOwnerId = ownerId.trim();
  if (!normalizedOwnerId || !conversation.id) return;
  conversationRouteSnapshots.update(conversationRouteSnapshotKey(normalizedOwnerId, conversation.id), (current) => ({
    ...current,
    [source]: preferConversationSnapshot(current?.[source], conversation),
    ...(source === 'detail' ? { detailRememberedAt: Date.now() } : {}),
  }));
}

export function readConversationRouteSnapshot(ownerId: string, conversationId: string): TChatConversation | undefined {
  const normalizedOwnerId = ownerId.trim();
  if (!normalizedOwnerId || !conversationId) return undefined;
  const snapshot = conversationRouteSnapshots.read(
    conversationRouteSnapshotKey(normalizedOwnerId, conversationId)
  )?.value;
  return snapshot?.detail ?? snapshot?.summary;
}

/**
 * Returns only a detail snapshot produced immediately before route mounting.
 * This lets navigation handoff avoid an identical second IPC read without
 * turning the route cache into a long-lived source of truth.
 */
export function readRecentConversationRouteDetail(
  ownerId: string,
  conversationId: string,
  maxAgeMs = RECENT_ROUTE_DETAIL_MAX_AGE_MS
): TChatConversation | undefined {
  const normalizedOwnerId = ownerId.trim();
  if (!normalizedOwnerId || !conversationId) return undefined;
  const snapshot = conversationRouteSnapshots.read(
    conversationRouteSnapshotKey(normalizedOwnerId, conversationId)
  )?.value;
  if (!snapshot?.detail || snapshot.detailRememberedAt === undefined) return undefined;
  if (Date.now() - snapshot.detailRememberedAt > maxAgeMs) return undefined;
  return snapshot.detail;
}

/**
 * Marks the short interval in which route, titlebar and runtime consumers are
 * mounting for the same navigation. Completed reads may be reused only during
 * this interval; realtime refreshes outside it always reach the backend.
 */
export function beginConversationDetailHandoff(conversationId: string, durationMs = 500): void {
  const conversationKey = conversationId.trim();
  if (!conversationKey) return;
  const key = rendererAccountScopedKey(conversationKey);
  conversationDetailHandoffs.set(key, { expiresAt: Date.now() + Math.max(0, durationMs) });
}

export function getConversationOrNull(conversationId: string): Promise<TChatConversation | null> {
  const conversationKey = conversationId.trim();
  if (!conversationKey) return Promise.resolve(null);
  const key = rendererAccountScopedKey(conversationKey);
  const active = conversationDetailRequests.get(key);
  if (active) return active;
  const handoff = conversationDetailHandoffs.get(key);
  if (handoff && handoff.expiresAt >= Date.now() && 'value' in handoff) {
    return Promise.resolve(handoff.value ?? null);
  }
  if (handoff && handoff.expiresAt < Date.now()) conversationDetailHandoffs.delete(key);

  const request = ipcBridge.conversation.get
    .invoke({ id: conversationKey })
    .then((conversation) => {
      const activeHandoff = conversationDetailHandoffs.get(key);
      if (activeHandoff && activeHandoff.expiresAt >= Date.now()) activeHandoff.value = conversation;
      return conversation;
    })
    .catch((error: unknown): TChatConversation | null => {
      if (isBackendHttpError(error) && error.status === 404) {
        return null;
      }
      throw error;
    })
    .finally(() => {
      if (conversationDetailRequests.get(key) === request) conversationDetailRequests.delete(key);
    });
  conversationDetailRequests.set(key, request);
  return request;
}

/**
 * Explicit name for consumers that participate in route/runtime handoff. The
 * underlying reader also coalesces legacy direct consumers, while retaining
 * no completed snapshot; a later realtime wake therefore still gets a fresh
 * authoritative server read.
 */
export function getConversationOrNullShared(conversationId: string): Promise<TChatConversation | null> {
  return getConversationOrNull(conversationId);
}

export async function refreshConversationCache(conversation_id: string, mutate: ScopedMutator): Promise<void> {
  const conversation = await getConversationOrNull(conversation_id);
  if (!conversation) return;

  await mutate<TChatConversation>(conversationCacheKey(conversation_id), conversation, false);
}

export function clearConversationReadCaches(): void {
  conversationRouteSnapshots.clear();
  conversationDetailRequests.clear();
  conversationDetailHandoffs.clear();
}

registerRendererAccountReset('conversation-read-caches', clearConversationReadCaches);
