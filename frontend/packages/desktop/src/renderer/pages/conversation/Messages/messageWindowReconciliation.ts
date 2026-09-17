/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { isTextPublicationCovered, preferTextMessageVersion, type TMessage } from '@/common/chat/chatLib';

const getMessageMergeKey = (message: TMessage): string => {
  if (message.msg_id) return `${message.type}:${message.msg_id}`;
  return `id:${message.id}`;
};

const messageStructuralFingerprints = new WeakMap<TMessage, string | null>();

const getMessageStructuralFingerprint = (message: TMessage): string | null => {
  const cached = messageStructuralFingerprints.get(message);
  if (cached !== undefined) return cached;
  try {
    const fingerprint = JSON.stringify(message);
    messageStructuralFingerprints.set(message, fingerprint);
    return fingerprint;
  } catch {
    // Renderer messages are JSON protocol values. If an extension violates
    // that contract, preserve correctness by rendering the new value rather
    // than retaining a possibly stale object.
    messageStructuralFingerprints.set(message, null);
    return null;
  }
};

const preserveMessageIdentityWhenEquivalent = (candidate: TMessage, current: TMessage): TMessage => {
  if (candidate === current) return current;
  const candidateFingerprint = getMessageStructuralFingerprint(candidate);
  return candidateFingerprint !== null && candidateFingerprint === getMessageStructuralFingerprint(current)
    ? current
    : candidate;
};

const preferPersistedOrLiveMessage = (persisted: TMessage, live: TMessage): TMessage => {
  if (persisted.type === 'text' && live.type === 'text') {
    return preserveMessageIdentityWhenEquivalent(preferTextMessageVersion(persisted, live), live);
  }
  return preserveMessageIdentityWhenEquivalent(persisted, live);
};

const canFallbackMergeByKey = (persisted: TMessage, live: TMessage): boolean => {
  // The transcript projector can split one assistant attempt into multiple
  // durable text segments while the live stream temporarily exposes the
  // attempt-level msg_id. Do not reuse that coarse live object for a later
  // segment with a different durable id; doing so renders duplicate keys.
  if (
    persisted.type === 'text' &&
    live.type === 'text' &&
    persisted.id !== live.id &&
    (persisted.content.assistantAttemptId || live.content.assistantAttemptId)
  ) {
    const persistedText = persisted.content.content;
    const liveText = live.content.content;
    // During a running refresh the durable projector often exposes a shorter
    // prefix with a segment id while the direct stream still holds the complete
    // attempt-level preview. Consume the richer prefix-compatible version so
    // it occupies one row; genuinely different segments remain separate.
    return persistedText.startsWith(liveText) || liveText.startsWith(persistedText);
  }
  return true;
};

/**
 * Merge an authoritative durable page into the live projection while retaining
 * stable object/list identities for semantically unchanged rows. Virtuoso can
 * then keep its measured geometry instead of remeasuring the whole transcript
 * at every tool boundary or stale-stream recovery.
 */
export function mergeLoadedPageWithCurrent(
  conversationId: string,
  messages: TMessage[],
  currentList: TMessage[],
  preserveUnmatchedLiveKeys = false
): TMessage[] {
  if (!currentList.length) return messages;

  const sameConversation = currentList.filter((message) => message.conversation_id === conversationId);
  if (!sameConversation.length) return messages;

  const currentById = new Map<string, TMessage>();
  const currentByKey = new Map<string, TMessage[]>();
  for (const message of sameConversation) {
    currentById.set(message.id, message);
    const key = getMessageMergeKey(message);
    const candidates = currentByKey.get(key) ?? [];
    candidates.push(message);
    currentByKey.set(key, candidates);
  }
  const consumedLiveIds = new Set<string>();
  const takeLiveMessage = (message: TMessage | undefined): TMessage | undefined => {
    if (!message || consumedLiveIds.has(message.id)) return undefined;
    consumedLiveIds.add(message.id);
    return message;
  };
  const loadedIds = new Set(messages.map((message) => message.id));
  const loadedKeys = new Set(messages.map(getMessageMergeKey));

  const mergedMessages = messages.map((message) => {
    const exactLive = takeLiveMessage(currentById.get(message.id));
    const live =
      exactLive ??
      currentByKey
        .get(getMessageMergeKey(message))
        ?.map((candidate) => (canFallbackMergeByKey(message, candidate) ? takeLiveMessage(candidate) : undefined))
        .find(Boolean);
    return live ? preferPersistedOrLiveMessage(message, live) : message;
  });
  const mergedByKey = new Map(mergedMessages.map((message) => [getMessageMergeKey(message), message]));
  const liveOnly = sameConversation.filter((message) => {
    const key = getMessageMergeKey(message);
    if (
      consumedLiveIds.has(message.id) ||
      loadedIds.has(message.id) ||
      (!preserveUnmatchedLiveKeys && loadedKeys.has(key))
    )
      return false;
    const represented = mergedByKey.get(key);
    return !(message.type === 'text' && represented?.type === 'text' && isTextPublicationCovered(represented, message));
  });

  if (liveOnly.length) return [...mergedMessages, ...liveOnly];
  if (
    mergedMessages.length === sameConversation.length &&
    mergedMessages.every((message, index) => message === sameConversation[index]) &&
    sameConversation.length === currentList.length
  ) {
    return currentList;
  }
  return mergedMessages;
}

export function prependHistoryMessages(currentList: TMessage[], messages: TMessage[]): TMessage[] {
  if (!messages.length) return currentList;

  const currentIds = new Set(currentList.map((message) => message.id));
  const currentKeys = new Set(currentList.map(getMessageMergeKey));
  const uniqueHistory = messages.filter(
    (message) => !currentIds.has(message.id) && !currentKeys.has(getMessageMergeKey(message))
  );
  return uniqueHistory.length ? [...uniqueHistory, ...currentList] : currentList;
}

export function appendHistoryMessages(currentList: TMessage[], messages: TMessage[]): TMessage[] {
  if (!messages.length) return currentList;

  const currentIds = new Set(currentList.map((message) => message.id));
  const currentKeys = new Set(currentList.map(getMessageMergeKey));
  const uniqueHistory = messages.filter(
    (message) => !currentIds.has(message.id) && !currentKeys.has(getMessageMergeKey(message))
  );
  return uniqueHistory.length ? [...currentList, ...uniqueHistory] : currentList;
}
