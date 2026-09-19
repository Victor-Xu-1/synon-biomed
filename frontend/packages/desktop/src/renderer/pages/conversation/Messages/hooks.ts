/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import { isBackendHttpError } from '@/common/adapter/httpBridge';
import type {
  AgentStreamErrorInfo,
  ArtifactReferenceMergeMode,
  IMessageText,
  IMessageTips,
  TMessage,
} from '@/common/chat/chatLib';
import {
  composeMessage,
  mergeAcpToolCallContent,
  mergeArtifactReferences,
  mergeTextMessageContent,
  mergeTextPublicationCoverage,
  isTextPublicationCovered,
  normalizeAgentStreamError,
  normalizeTextMessageContent,
  patchTextMessageArtifactReferences,
  sanitizeAcpToolCallContent,
} from '@/common/chat/chatLib';
import { decodeArtifactReferences, type ArtifactReferenceWire } from '@/common/adapter/messageStreamProtocol';
import { useCallback, useEffect, useLayoutEffect, useRef } from 'react';
import {
  SYNON_BIOMED_BRANCH_SELECTION_EVENT,
  getSynonBiomedBranchSelectionRevision,
  resetSynonBiomedBranchSelection,
  rollbackSynonBiomedBranchSelection,
  selectSynonBiomedBranch,
  type SynonBiomedBranchSelectionEventDetail,
} from '@/renderer/services/synonBiomedConversationBranches';
import { createContext } from '@renderer/utils/ui/createContext';
import {
  INTERACTIVE_MESSAGE_PAGE_LIMIT,
  loadConversationAnchorWindow,
  loadConversationMessagePage,
} from '@/renderer/utils/chat/messagePagination';
import { cancelConversationMessageRequests, loadLatestConversationMessagesShared } from './messageWindowPrefetch';
import { isMessageRequestAbort } from './messageRequestAbort';
import {
  TRANSIENT_HISTORY_RETRY_DELAYS_MS,
  isConversationHistoryNotReady,
} from '@/renderer/services/synonBiomedHistoryRetry';
import { scheduleConversationRoutePerformanceStage } from '../conversationRoutePerformance';
import { registerRendererAccountReset } from '@/renderer/services/rendererAccountScope';
import {
  appendHistoryMessages,
  mergeLoadedPageWithCurrent,
  prependHistoryMessages,
} from './messageWindowReconciliation';

const [useMessageList, MessageListProvider, useUpdateMessageList] = createContext([] as TMessage[]);
const [useMessageListLoading, MessageListLoadingProvider, useUpdateMessageListLoading] = createContext(false);

export type MessageListLoadSource =
  | 'initial'
  | 'terminal-mount'
  | 'live'
  | 'refresh'
  | 'branch'
  | 'rebase'
  | 'cursor-reset';
export type MessageListLoadError = { source: MessageListLoadSource };
const [useMessageListLoadError, MessageListLoadErrorProvider, useUpdateMessageListLoadError] =
  createContext<MessageListLoadError | null>(null);

const OPTIMISTIC_USER_MESSAGE_PREFIX = 'synonbiomed-optimistic-user:';

const withoutOptimisticUserMessages = (list: TMessage[]): TMessage[] =>
  list.filter((message) => !message.id.startsWith(OPTIMISTIC_USER_MESSAGE_PREFIX));

const [useChatKey, ChatKeyProvider] = createContext('');

export type MessagePaginationState = {
  /** Conversation that owns every cursor and publication boundary below. */
  conversationId?: string;
  oldestCursor?: string;
  newestCursor?: string;
  hasMoreBefore: boolean;
  hasMoreAfter: boolean;
  isLoadingBefore: boolean;
  isLoadingAfter: boolean;
  isLoadingAnchor: boolean;
  branchId?: string;
  branchGeneration?: number;
  /** Durable Transcript publication boundary represented by this window. */
  historyRevision?: number;
  /**
   * Presentation boundaries introduced by cursor-page prepends. Tool and file
   * summaries must not merge across one of these IDs because doing so would
   * replace the stable row that is currently anchoring the viewport.
   */
  groupBoundaryMessageIds?: string[];
};

const EMPTY_MESSAGE_PAGINATION_STATE: MessagePaginationState = {
  hasMoreBefore: false,
  hasMoreAfter: false,
  isLoadingBefore: false,
  isLoadingAfter: false,
  isLoadingAnchor: false,
};

const [useMessagePaginationState, MessagePaginationProvider, useUpdateMessagePaginationState] =
  createContext<MessagePaginationState>(EMPTY_MESSAGE_PAGINATION_STATE);

let nextMessageHistoryRequestEpoch = 0;
const activeMessageHistoryRequestEpochs = new Map<string, number>();
type AbortableMessageHistoryRequest = Readonly<{
  epoch: number;
  controller: AbortController;
}>;
const abortableMessageHistoryRequests = new Map<string, AbortableMessageHistoryRequest>();

type CachedMessageWindow = {
  ownerId: string;
  branchRevision: number;
  messages: TMessage[];
  pagination: MessagePaginationState;
};

const MAX_CACHED_MESSAGE_WINDOWS = 8;
const cachedMessageWindows = new Map<string, CachedMessageWindow>();

function clearConversationMessageHistoryState(): void {
  for (const request of abortableMessageHistoryRequests.values()) {
    if (!request.controller.signal.aborted) request.controller.abort('account_scope_changed');
  }
  abortableMessageHistoryRequests.clear();
  activeMessageHistoryRequestEpochs.clear();
  cachedMessageWindows.clear();
}

registerRendererAccountReset('conversation-message-history', clearConversationMessageHistoryState);

function messagePageHistoryRevision(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : undefined;
}

function messageWindowOwnerKey(ownerId: string): string {
  return ownerId.trim() || '__anonymous__';
}

function messageWindowCacheKey(conversationId: string, ownerId: string, branchRevision: number): string {
  return JSON.stringify([messageWindowOwnerKey(ownerId), conversationId, branchRevision]);
}

function readCachedMessageWindow(
  conversationId: string,
  ownerId: string,
  branchRevision: number
): CachedMessageWindow | null {
  const cacheKey = messageWindowCacheKey(conversationId, ownerId, branchRevision);
  const cached = cachedMessageWindows.get(cacheKey);
  if (!cached) return null;
  if (cached.ownerId !== messageWindowOwnerKey(ownerId) || cached.branchRevision !== branchRevision) {
    cachedMessageWindows.delete(cacheKey);
    return null;
  }
  cachedMessageWindows.delete(cacheKey);
  cachedMessageWindows.set(cacheKey, cached);
  return cached;
}

function writeCachedMessageWindow(
  conversationId: string,
  ownerId: string,
  branchRevision: number,
  messages: TMessage[],
  pagination: MessagePaginationState
): void {
  if (!conversationId || messages.some((message) => message.conversation_id !== conversationId)) return;
  const cacheKey = messageWindowCacheKey(conversationId, ownerId, branchRevision);
  cachedMessageWindows.delete(cacheKey);
  cachedMessageWindows.set(cacheKey, {
    ownerId: messageWindowOwnerKey(ownerId),
    branchRevision,
    messages,
    pagination: { ...pagination },
  });
  while (cachedMessageWindows.size > MAX_CACHED_MESSAGE_WINDOWS) {
    const oldestKey = cachedMessageWindows.keys().next().value as string | undefined;
    if (!oldestKey) break;
    cachedMessageWindows.delete(oldestKey);
  }
}

function beginMessageHistoryRequest(conversationId: string): number {
  const active = abortableMessageHistoryRequests.get(conversationId);
  if (active) {
    abortableMessageHistoryRequests.delete(conversationId);
    if (!active.controller.signal.aborted) active.controller.abort('message_history_superseded');
  }
  const next = ++nextMessageHistoryRequestEpoch;
  activeMessageHistoryRequestEpochs.set(conversationId, next);
  return next;
}

function invalidateMessageHistoryRequest(conversationId: string): void {
  const active = abortableMessageHistoryRequests.get(conversationId);
  if (active) {
    abortableMessageHistoryRequests.delete(conversationId);
    if (!active.controller.signal.aborted) active.controller.abort('message_history_invalidated');
  }
  activeMessageHistoryRequestEpochs.delete(conversationId);
}

function releaseMessageHistoryRequest(conversationId: string, requestEpoch: number): void {
  if (activeMessageHistoryRequestEpochs.get(conversationId) === requestEpoch) {
    activeMessageHistoryRequestEpochs.delete(conversationId);
  }
}

function beginAbortableMessageHistoryRequest(conversationId: string): AbortableMessageHistoryRequest {
  const request = {
    epoch: beginMessageHistoryRequest(conversationId),
    controller: new AbortController(),
  };
  abortableMessageHistoryRequests.set(conversationId, request);
  return request;
}

function releaseAbortableMessageHistoryRequest(conversationId: string, request: AbortableMessageHistoryRequest): void {
  if (abortableMessageHistoryRequests.get(conversationId) === request) {
    abortableMessageHistoryRequests.delete(conversationId);
  }
  releaseMessageHistoryRequest(conversationId, request.epoch);
}

function isCurrentMessageHistoryRequest(conversationId: string, requestEpoch: number, branchRevision: number): boolean {
  return (
    activeMessageHistoryRequestEpochs.get(conversationId) === requestEpoch &&
    getSynonBiomedBranchSelectionRevision(conversationId) === branchRevision
  );
}

const beforeUpdateMessageListStack: Array<(list: TMessage[]) => TMessage[]> = [];

// 消息索引缓存类型定义
// Message index cache type definitions
interface MessageIndex {
  msgIdIndex: Map<string, number>; // msg_id -> index
  call_idIndex: Map<string, number>; // tool_call.call_id -> index
  tool_call_idIndex: Map<string, number>; // acp_tool_call.update.tool_call_id -> index
  permission_call_idIndex: Map<string, number>; // permission.content.call_id -> index
}

function getMessageIndexKey(message: TMessage): string | undefined {
  if (!message.msg_id) return undefined;
  return `${message.type}:${message.msg_id}`;
}

function getToolCallIdentity(message: Extract<TMessage, { type: 'tool_call' }>): string | undefined {
  const callId = message.content?.call_id?.trim();
  if (!callId) return undefined;
  const attempt = message.content.attempt;
  return Number.isSafeInteger(attempt) && Number(attempt) >= 1
    ? `${Number(attempt)}:${message.content.operation_id?.trim() || callId}`
    : callId;
}

function isTerminalToolCallStatus(status: string | undefined): boolean {
  return (
    status === 'completed' ||
    status === 'error' ||
    status === 'canceled' ||
    status === 'interrupted' ||
    status === 'unknown'
  );
}

function isActiveToolCallStatus(status: string | undefined): boolean {
  return status === 'pending' || status === 'running' || status === 'waiting' || status === 'blocked';
}

function compatibleToolCallInput(
  existing: Extract<TMessage, { type: 'tool_call' }>,
  incoming: Extract<TMessage, { type: 'tool_call' }>
): boolean {
  const existingInput = existing.content.input ?? existing.content.args;
  const incomingInput = incoming.content.input ?? incoming.content.args;
  if (existingInput === undefined || incomingInput === undefined) return true;
  try {
    return JSON.stringify(existingInput) === JSON.stringify(incomingInput);
  } catch {
    return false;
  }
}

function findCompatibleActiveToolIndex(
  list: TMessage[],
  incoming: Extract<TMessage, { type: 'tool_call' }>
): number | undefined {
  const operationId = incoming.content.operation_id?.trim() || incoming.content.call_id?.trim();
  const name = incoming.content.name?.trim();
  if (!operationId || !name || !isTerminalToolCallStatus(incoming.content.status)) return undefined;
  const candidates = list.flatMap((candidate, candidateIndex) => {
    if (
      candidate.type !== 'tool_call' ||
      !isActiveToolCallStatus(candidate.content.status) ||
      (candidate.content.operation_id?.trim() || candidate.content.call_id?.trim()) !== operationId ||
      candidate.content.name?.trim() !== name ||
      !compatibleToolCallInput(candidate, incoming)
    ) {
      return [];
    }
    return [candidateIndex];
  });
  return candidates.length === 1 ? candidates[0] : undefined;
}

// 使用 WeakMap 缓存索引，当列表被 GC 时自动清理
// Use WeakMap to cache index, auto-cleanup when list is GC'd
const indexCache = new WeakMap<TMessage[], MessageIndex>();

export function logDroppedToolCallWithoutCallId(message: TMessage | undefined): boolean {
  if (!message) return false;
  if (message.type !== 'tool_call' || message.content?.call_id) return false;

  console.warn('[tool-call] dropped tool_call without call_id', {
    conversation_id: message.conversation_id,
    msg_id: message.msg_id,
    name: message.content?.name,
    status: message.content?.status,
  });
  return true;
}

// 构建消息索引
// Build message index
function buildMessageIndex(list: TMessage[]): MessageIndex {
  const msgIdIndex = new Map<string, number>();
  const call_idIndex = new Map<string, number>();
  const tool_call_idIndex = new Map<string, number>();
  const permission_call_idIndex = new Map<string, number>();

  for (let i = 0; i < list.length; i++) {
    const msg = list[i];
    const msgIndexKey = getMessageIndexKey(msg);
    if (msgIndexKey) {
      msgIdIndex.set(msgIndexKey, i);
    }
    if (msg.type === 'tool_call') {
      const identity = getToolCallIdentity(msg);
      if (identity) call_idIndex.set(identity, i);
    }
    if (msg.type === 'acp_tool_call' && msg.content?.update?.tool_call_id) {
      tool_call_idIndex.set(msg.content.update.tool_call_id, i);
    }
    if (msg.type === 'permission' && msg.content?.call_id) {
      permission_call_idIndex.set(msg.content.call_id, i);
    }
  }

  return {
    msgIdIndex,
    call_idIndex,
    tool_call_idIndex,
    permission_call_idIndex,
  };
}

// 获取或构建索引（带缓存）
// Get or build index with caching
function getOrBuildIndex(list: TMessage[]): MessageIndex {
  let cached = indexCache.get(list);
  if (!cached) {
    cached = buildMessageIndex(list);
    indexCache.set(list, cached);
  }
  return cached;
}

const sanitizeMessageForList = (message: TMessage): TMessage =>
  message.type === 'acp_tool_call'
    ? ({
        ...message,
        content: sanitizeAcpToolCallContent(message.content),
      } as TMessage)
    : message;

const withUniqueTextSegmentId = (message: TMessage, list: TMessage[]): TMessage => {
  if (message.type !== 'text' || !list.some((item) => item.id === message.id)) return message;
  const segmentNumber = list.filter((item) => item.type === 'text' && item.msg_id === message.msg_id).length + 1;
  return { ...message, id: `${message.id}:segment:${segmentNumber}` };
};

// 使用索引优化的消息合并函数
// Index-optimized message compose function
function composeMessageWithIndex(message: TMessage | undefined, list: TMessage[], index: MessageIndex): TMessage[] {
  if (!message) return list || [];

  if (logDroppedToolCallWithoutCallId(message)) {
    return list || [];
  }

  if (!list?.length) {
    const firstMessage = sanitizeMessageForList(message);
    // Update index when adding first message
    const msgIndexKey = getMessageIndexKey(firstMessage);
    if (msgIndexKey) {
      index.msgIdIndex.set(msgIndexKey, 0);
    }
    return [firstMessage];
  }

  const last = list[list.length - 1];

  // 对于 tool_group 类型，使用原始的 composeMessage（因为涉及内部数组匹配）
  // For tool_group type, use original composeMessage (involves inner array matching)
  // After composeMessage, the returned list may have different length/ordering,
  // so we must invalidate the index to prevent stale lookups in subsequent calls.
  if (message.type === 'tool_group') {
    const result = composeMessage(message, list);
    if (result !== list) {
      // Rebuild index maps from the new list to keep them in sync
      const rebuilt = buildMessageIndex(result);
      index.msgIdIndex = rebuilt.msgIdIndex;
      index.call_idIndex = rebuilt.call_idIndex;
      index.tool_call_idIndex = rebuilt.tool_call_idIndex;
      index.permission_call_idIndex = rebuilt.permission_call_idIndex;
    }
    return result;
  }

  // tool_call: 使用 call_idIndex 快速查找
  // tool_call: use call_idIndex for fast lookup
  if (message.type === 'tool_call' && message.content?.call_id) {
    const identity = getToolCallIdentity(message);
    if (!identity) return list;
    const exactExistingIdx = index.call_idIndex.get(identity);
    const existingIdx = exactExistingIdx ?? findCompatibleActiveToolIndex(list, message);
    if (existingIdx !== undefined && existingIdx < list.length) {
      const existingMsg = list[existingIdx];
      if (existingMsg.type === 'tool_call') {
        const newList = list.slice();
        const existingTerminal = isTerminalToolCallStatus(existingMsg.content.status);
        const incomingTerminal = isTerminalToolCallStatus(message.content.status);
        const merged = { ...existingMsg.content, ...message.content };
        if (exactExistingIdx === undefined) {
          merged.attempt = existingMsg.content.attempt;
          merged.call_id = existingMsg.content.call_id;
          merged.operation_id = existingMsg.content.operation_id;
        }
        if (existingTerminal) {
          merged.status = existingMsg.content.status;
          if (existingMsg.content.output !== undefined) merged.output = existingMsg.content.output;
          if (existingMsg.content.error !== undefined) merged.error = existingMsg.content.error;
        }
        if (existingTerminal || incomingTerminal) {
          delete merged.streaming;
          delete merged.streamingRecoveryError;
        }
        newList[existingIdx] = {
          ...existingMsg,
          ...message,
          id: existingMsg.id,
          msg_id: existingMsg.msg_id,
          created_at: existingMsg.created_at,
          hidden: existingTerminal || incomingTerminal ? false : message.hidden,
          content: merged,
        };
        index.call_idIndex.set(identity, existingIdx);
        return newList;
      }
    }
    // 未找到，添加新消息并更新索引
    const newIdx = list.length;
    index.call_idIndex.set(identity, newIdx);
    const msgIndexKey = getMessageIndexKey(message);
    if (msgIndexKey) index.msgIdIndex.set(msgIndexKey, newIdx);
    return list.concat(message);
  }

  // acp_tool_call: use tool_call_idIndex for fast lookup
  if (message.type === 'acp_tool_call' && message.content?.update?.tool_call_id) {
    const existingIdx = index.tool_call_idIndex.get(message.content.update.tool_call_id);
    if (existingIdx !== undefined && existingIdx < list.length) {
      const existingMsg = list[existingIdx];
      if (existingMsg.type === 'acp_tool_call') {
        const newList = list.slice();
        const merged = mergeAcpToolCallContent(existingMsg.content, message.content);
        newList[existingIdx] = { ...existingMsg, content: merged };
        return newList;
      }
    }
    // 未找到，添加新消息并更新索引
    const newIdx = list.length;
    index.tool_call_idIndex.set(message.content.update.tool_call_id, newIdx);
    const msgIndexKey = getMessageIndexKey(message);
    if (msgIndexKey) index.msgIdIndex.set(msgIndexKey, newIdx);
    return list.concat(sanitizeMessageForList(message));
  }

  // permission: use call_id for recovery/live stream dedupe.
  if (message.type === 'permission' && message.content?.call_id) {
    const existingIdx = index.permission_call_idIndex.get(message.content.call_id);
    if (existingIdx !== undefined && existingIdx < list.length) {
      const existingMsg = list[existingIdx];
      if (existingMsg.type === 'permission') {
        const newList = list.slice();
        newList[existingIdx] = {
          ...existingMsg,
          ...message,
          content: message.content,
        };
        return newList;
      }
    }
    const newIdx = list.length;
    index.permission_call_idIndex.set(message.content.call_id, newIdx);
    const msgIndexKey = getMessageIndexKey(message);
    if (msgIndexKey) index.msgIdIndex.set(msgIndexKey, newIdx);
    return list.concat(message);
  }

  // text message: merge only with the latest contiguous streaming chunk.
  // text 消息: 只与最后一条连续的流式片段合并，保留被工具/思考打断后的消息边界。
  if (message.type === 'text' && message.msg_id) {
    if (message.content.replaceScope === 'attempt') {
      const attemptId = message.content.assistantAttemptId;
      if (!attemptId) return list;
      const newList = list.filter((item) => item.type !== 'text' || item.content.assistantAttemptId !== attemptId);
      if (message.content.content !== '') newList.push(message);
      const rebuilt = buildMessageIndex(newList);
      index.msgIdIndex = rebuilt.msgIdIndex;
      index.call_idIndex = rebuilt.call_idIndex;
      index.tool_call_idIndex = rebuilt.tool_call_idIndex;
      index.permission_call_idIndex = rebuilt.permission_call_idIndex;
      return newList;
    }
    const messageKey = getMessageIndexKey(message)!;
    const existingIdx = index.msgIdIndex.get(messageKey);
    if (existingIdx !== undefined && existingIdx < list.length) {
      const existingMsg = list[existingIdx];
      if (existingMsg.type === 'text') {
        if (isTextPublicationCovered(existingMsg, message)) return list;
        if (message.content.replace === true) {
          const newList = list.filter((item) => item.type !== 'text' || item.msg_id !== message.msg_id);
          if (message.content.content !== '') {
            newList.push({
              ...existingMsg,
              ...message,
              ...mergeTextPublicationCoverage(existingMsg, message),
              content: mergeTextMessageContent(existingMsg.content, message.content),
            });
          }
          const rebuilt = buildMessageIndex(newList);
          index.msgIdIndex = rebuilt.msgIdIndex;
          index.call_idIndex = rebuilt.call_idIndex;
          index.tool_call_idIndex = rebuilt.tool_call_idIndex;
          index.permission_call_idIndex = rebuilt.permission_call_idIndex;
          return newList;
        }
        // User messages (right position) are complete — skip if already exists to prevent duplicates
        if (message.position === 'right') {
          if (message.artifact_refs === undefined) return list;
          const newList = list.slice();
          newList[existingIdx] = {
            ...existingMsg,
            artifact_refs: mergeArtifactReferences(existingMsg.artifact_refs, message.artifact_refs, 'union'),
          };
          return newList;
        }
        if (
          Number.isSafeInteger(message.source_publication_sequence) &&
          Number(message.source_publication_sequence) > 0
        ) {
          // A durable msg_id already identifies its segment. A delayed publication
          // enriches that row in place, even after a tool, rather than inventing a new segment.
          const newList = list.slice();
          newList[existingIdx] = {
            ...existingMsg,
            ...message,
            id: existingMsg.id,
            ...mergeTextPublicationCoverage(existingMsg, message),
            content: mergeTextMessageContent(existingMsg.content, message.content),
            artifact_refs: mergeArtifactReferences(existingMsg.artifact_refs, message.artifact_refs, 'union'),
          };
          return newList;
        }
      }
    }

    if (last.type === 'text' && last.msg_id === message.msg_id) {
      const newList = list.slice();
      newList[newList.length - 1] = {
        ...last,
        ...message,
        ...mergeTextPublicationCoverage(last, message),
        content: mergeTextMessageContent(last.content, message.content),
        artifact_refs: mergeArtifactReferences(
          last.artifact_refs,
          message.artifact_refs,
          message.content.replace === true ? 'replace' : 'union'
        ),
      };
      return newList;
    }

    const segment = withUniqueTextSegmentId(message, list);
    const newIdx = list.length;
    index.msgIdIndex.set(messageKey, newIdx);
    return list.concat(segment);
  }

  // thinking message: merge only with the latest contiguous thinking chunk.
  // Uses "thinking:${msg_id}" key to avoid collision with text messages sharing the same msg_id.
  if (message.type === 'thinking' && message.msg_id) {
    const thinkingKey = getMessageIndexKey(message)!;
    if (message.content.status === 'done') {
      const existingIdx = index.msgIdIndex.get(thinkingKey);
      if (existingIdx !== undefined && existingIdx < list.length) {
        const existingMsg = list[existingIdx];
        if (existingMsg.type === 'thinking') {
          const newList = list.slice();
          newList[existingIdx] = {
            ...existingMsg,
            content: {
              ...existingMsg.content,
              status: 'done' as const,
              duration: message.content.duration,
              subject: message.content.subject || existingMsg.content.subject,
            },
          };
          return newList;
        }
      }
    }

    if (last.type === 'thinking' && last.msg_id === message.msg_id) {
      const newList = list.slice();
      newList[newList.length - 1] = {
        ...last,
        content: {
          ...last.content,
          content:
            message.content.replace === true ? message.content.content : last.content.content + message.content.content,
          subject: message.content.subject || last.content.subject,
          replace: message.content.replace,
        },
      };
      return newList;
    }

    const newIdx = list.length;
    index.msgIdIndex.set(thinkingKey, newIdx);
    return list.concat(message);
  }

  // plan message: update content and move to end of list
  if (message.type === 'plan' && message.msg_id) {
    const messageKey = getMessageIndexKey(message)!;
    const existingIdx = index.msgIdIndex.get(messageKey);
    if (existingIdx !== undefined && existingIdx < list.length) {
      const existingMsg = list[existingIdx];
      const newList = list.slice();
      newList.splice(existingIdx, 1);
      const updated = {
        ...existingMsg,
        ...message,
        content: message.content,
      } as TMessage;
      newList.push(updated);
      // Rebuild index after splice
      const rebuilt = buildMessageIndex(newList);
      index.msgIdIndex = rebuilt.msgIdIndex;
      index.call_idIndex = rebuilt.call_idIndex;
      index.tool_call_idIndex = rebuilt.tool_call_idIndex;
      index.permission_call_idIndex = rebuilt.permission_call_idIndex;
      return newList;
    }
    const newIdx = list.length;
    index.msgIdIndex.set(messageKey, newIdx);
    return list.concat(message);
  }

  // agent_status / tips and other msg_id-based messages:
  // replace the existing item in place instead of appending duplicates.
  if (message.msg_id) {
    const messageKey = getMessageIndexKey(message)!;
    const existingIdx = index.msgIdIndex.get(messageKey);
    if (existingIdx !== undefined && existingIdx < list.length) {
      const existingMsg = list[existingIdx];
      const newList = list.slice();
      newList[existingIdx] = {
        ...existingMsg,
        ...message,
        content: message.content,
      } as TMessage;
      return newList;
    }
  }

  // Other types: fallback to last message check
  // 其他类型: 回退到检查最后一条消息
  if (last.msg_id !== message.msg_id || last.type !== message.type) {
    // Add new message and update index
    const newIdx = list.length;
    const msgIndexKey = getMessageIndexKey(message);
    if (msgIndexKey) index.msgIdIndex.set(msgIndexKey, newIdx);
    return list.concat(message);
  }

  // Merge other message types with same msg_id
  const newList = list.slice();
  const lastIdx = newList.length - 1;
  newList[lastIdx] = { ...last, ...message };
  return newList;
}

export const useMergeLiveMessage = () => {
  const update = useUpdateMessageList();
  const pendingRef = useRef<
    Array<{
      message?: TMessage;
      add: boolean;
      artifactUpdate?: {
        msgId: string;
        references: ArtifactReferenceWire[];
        mode: ArtifactReferenceMergeMode;
      };
    }>
  >([]);
  const frameRef = useRef<number | null>(null);
  const lastFlushAtRef = useRef(Number.NEGATIVE_INFINITY);

  const flush = useCallback(() => {
    frameRef.current = null;

    const pending = pendingRef.current;
    if (!pending.length) return;
    pendingRef.current = [];
    update((list) => {
      // 获取或构建索引用于快速查找 (O(1) instead of O(n))
      // Get or build index for fast lookup
      const index = getOrBuildIndex(list);
      let newList = list;

      for (const item of pending) {
        const messageWasDropped = item.message ? logDroppedToolCallWithoutCallId(item.message) : false;
        const publishedText =
          item.message?.type === 'text' &&
          Number.isSafeInteger(item.message.source_publication_sequence) &&
          Number(item.message.source_publication_sequence) > 0;
        if (item.message && !messageWasDropped && item.add && !publishedText) {
          // 新增消息，更新索引
          // New message, update index
          const msg = sanitizeMessageForList(item.message);
          const newIdx = newList.length;
          const msgIndexKey = getMessageIndexKey(msg);
          if (msgIndexKey) index.msgIdIndex.set(msgIndexKey, newIdx);
          if (msg.type === 'tool_call') {
            const identity = getToolCallIdentity(msg);
            if (identity) index.call_idIndex.set(identity, newIdx);
          }
          if (msg.type === 'acp_tool_call' && msg.content?.update?.tool_call_id) {
            index.tool_call_idIndex.set(msg.content.update.tool_call_id, newIdx);
          }
          if (msg.type === 'permission' && msg.content?.call_id) {
            index.permission_call_idIndex.set(msg.content.call_id, newIdx);
          }
          newList = newList.concat(msg);
        } else if (item.message && !messageWasDropped) {
          // 使用索引优化的消息合并
          // Use index-optimized message compose
          newList = composeMessageWithIndex(item.message, newList, index);
        }
        if (item.artifactUpdate) {
          newList = patchTextMessageArtifactReferences(
            newList,
            item.artifactUpdate.msgId,
            item.artifactUpdate.references,
            item.artifactUpdate.mode
          );
        }

        while (beforeUpdateMessageListStack.length) {
          newList = beforeUpdateMessageListStack.shift()!(newList);
        }
      }
      return newList;
    });
    lastFlushAtRef.current = performance.now();
  }, [update]);

  useEffect(() => {
    return () => {
      if (frameRef.current !== null) {
        cancelAnimationFrame(frameRef.current);
      }
    };
  }, []);

  return useCallback(
    (
      message: TMessage | undefined,
      add = false,
      artifactUpdate?: {
        msgId: string;
        references: ArtifactReferenceWire[];
        mode: ArtifactReferenceMergeMode;
      }
    ) => {
      if (!message && !artifactUpdate) {
        return;
      }
      pendingRef.current.push({ message, add, artifactUpdate });
      if (frameRef.current === null) {
        // Keep user-visible insertions responsive, but never let a streaming
        // delta trigger its own render. The v1.1 workbench coalesces the live
        // tail before React paints; doing the same here prevents markdown,
        // grouping, and Virtuoso measurement from competing with every event.
        if (add && performance.now() - lastFlushAtRef.current >= 16) {
          flush();
        } else {
          frameRef.current = requestAnimationFrame(flush);
        }
      }
    },
    [flush]
  );
};

export const useAddOrUpdateMessage = useMergeLiveMessage;

export const useRemoveMessageByMsgId = () => {
  const update = useUpdateMessageList();

  return useCallback(
    (msgId: string) => {
      update((list) => list.filter((message) => message.msg_id !== msgId));
    },
    [update]
  );
};

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const parseJsonRecord = (value: unknown): Record<string, unknown> | undefined => {
  if (isRecord(value)) return value;
  if (typeof value !== 'string') return undefined;
  try {
    const parsed = JSON.parse(value) as unknown;
    return isRecord(parsed) ? parsed : undefined;
  } catch {
    return undefined;
  }
};

const normalizeTipType = (value: unknown, fallback: IMessageTips['content']['type']) =>
  value === 'success' || value === 'warning' || value === 'error' || value === 'info' ? value : fallback;

const normalizePersistedWorkspaceRuntimeError = (
  parsed: Record<string, unknown>,
  message: string
): AgentStreamErrorInfo | undefined => {
  if (
    parsed.code !== 'WORKSPACE_PATH_RUNTIME_UNAVAILABLE' &&
    parsed.code !== 'WORKSPACE_PATH_CONTAINS_WHITESPACE_RUNTIME_UNSUPPORTED'
  ) {
    return undefined;
  }

  const details = isRecord(parsed.details) ? parsed.details : undefined;
  const workspacePath = typeof details?.workspace_path === 'string' ? details.workspace_path : undefined;
  if (!workspacePath) {
    return undefined;
  }

  const persistedError = isRecord(parsed.error) ? parsed.error : undefined;
  const detail = typeof persistedError?.detail === 'string' ? persistedError.detail : message;

  return {
    message,
    code: 'WORKSPACE_PATH_RUNTIME_UNAVAILABLE',
    ownership: 'synon-ai',
    detail,
    workspacePath,
    retryable: false,
    feedback_recommended: false,
  };
};

const classifyPersistedSendFailure = (
  parsed: Record<string, unknown>,
  message: string
): AgentStreamErrorInfo | undefined => {
  if (typeof parsed.source !== 'string' && typeof parsed.code !== 'string') {
    return undefined;
  }

  const persistedCode = typeof parsed.code === 'string' ? parsed.code : undefined;
  const structuredContent = isRecord(parsed.structuredContent) ? parsed.structuredContent : undefined;
  const domainCode =
    typeof structuredContent?.domainCode === 'string'
      ? structuredContent.domainCode
      : typeof parsed.domainCode === 'string'
        ? parsed.domainCode
        : undefined;
  const effectiveCode = domainCode || persistedCode;

  if (
    effectiveCode === 'MCP_HTTP_RESPONSE_READ_FAILED' ||
    effectiveCode === 'MCP_TOOL_REMOTE_ERROR' ||
    effectiveCode === 'MCP_TOOL_RESPONSE_UNEXPECTED' ||
    effectiveCode === 'MCP_TCP_READ_FAILED'
  ) {
    return {
      message,
      code: effectiveCode,
      ownership: 'synon-ai',
      detail: message,
      retryable: true,
      feedback_recommended: true,
    };
  }

  if (persistedCode === 'BAD_GATEWAY') {
    return {
      message,
      code: 'UNKNOWN_UPSTREAM_ERROR',
      ownership: 'unknown_upstream',
      detail: message,
      retryable: true,
      feedback_recommended: true,
    };
  }

  if (persistedCode === 'INTERNAL_ERROR') {
    return {
      message,
      code: 'SYNON_AI_INTERNAL_ERROR',
      ownership: 'synon-ai',
      detail: message,
      retryable: true,
      feedback_recommended: true,
    };
  }

  if (persistedCode?.startsWith('SYNON_AI_')) {
    return {
      message,
      code: persistedCode,
      ownership: 'synon-ai',
      detail: message,
      retryable: true,
    };
  }
  if (persistedCode?.startsWith('USER_AGENT_')) {
    return {
      message,
      code: persistedCode,
      ownership: 'user_agent',
      detail: message,
      retryable: true,
    };
  }
  if (persistedCode?.startsWith('USER_LLM_PROVIDER_')) {
    return {
      message,
      code: persistedCode,
      ownership: 'user_llm_provider',
      detail: message,
      retryable: false,
      feedback_recommended: false,
    };
  }
  if (persistedCode === 'UNKNOWN_UPSTREAM_ERROR') {
    return {
      message,
      code: persistedCode,
      ownership: 'unknown_upstream',
      detail: message,
      retryable: true,
      feedback_recommended: true,
    };
  }

  if (parsed.source === 'send_failed') {
    return {
      message,
      code: 'SYNON_AI_INTERNAL_ERROR',
      ownership: 'synon-ai',
      detail: message,
      retryable: true,
      feedback_recommended: true,
    };
  }

  return undefined;
};

const normalizeDbTipsMessage = (msg: TMessage): TMessage => {
  if (msg.type !== 'tips') return msg;
  const parsed = parseJsonRecord(msg.content);
  if (!parsed || typeof parsed.content !== 'string') return msg;

  const existingContent = isRecord(msg.content) ? msg.content : undefined;
  const fallbackType =
    existingContent?.type === 'success' ||
    existingContent?.type === 'warning' ||
    existingContent?.type === 'error' ||
    existingContent?.type === 'info'
      ? existingContent.type
      : 'error';
  const tipType = normalizeTipType(parsed.type, fallbackType);
  const code =
    typeof parsed.code === 'string'
      ? parsed.code
      : typeof existingContent?.code === 'string'
        ? existingContent.code
        : undefined;
  const params = isRecord(parsed.params)
    ? parsed.params
    : isRecord(existingContent?.params)
      ? existingContent.params
      : undefined;
  const structuredError =
    tipType === 'error'
      ? (normalizePersistedWorkspaceRuntimeError(parsed, parsed.content) ??
        normalizeAgentStreamError(parsed.error) ??
        classifyPersistedSendFailure(parsed, parsed.content) ??
        normalizeAgentStreamError({ ...parsed, message: parsed.content }))
      : undefined;

  return {
    ...msg,
    content: {
      content: parsed.content,
      type: tipType,
      ...(tipType !== 'error' && code ? { code } : {}),
      ...(tipType !== 'error' && params ? { params } : {}),
      ...(structuredError ? { error: structuredError } : {}),
    },
  } as IMessageTips;
};

/**
 * Normalize a message loaded from backend DB into renderer runtime shape.
 */
export function normalizeDbMessage(msg: TMessage): TMessage {
  const hasArtifactReferences = Object.hasOwn(msg, 'artifact_refs');
  const artifactReferences = hasArtifactReferences ? decodeArtifactReferences(msg.artifact_refs) : undefined;
  if (hasArtifactReferences && artifactReferences === undefined) {
    throw new Error('conversation_artifact_refs_invalid');
  }
  const normalized = hasArtifactReferences ? ({ ...msg, artifact_refs: artifactReferences! } as TMessage) : msg;
  if (normalized.type === 'tips') return normalizeDbTipsMessage(normalized);
  if (normalized.type !== 'text') return normalized;

  return {
    ...normalized,
    content: normalizeTextMessageContent((normalized as IMessageText).content),
  };
}

export function normalizeDbMessagePage(items: TMessage[], through?: number): TMessage[] {
  const coverage = messagePageHistoryRevision(through);
  return items.map((item) => {
    const message = normalizeDbMessage(item);
    return message.type === 'text' && coverage !== undefined
      ? { ...message, history_coverage_through: coverage }
      : message;
  });
}

export const usePrependHistoryPage = () => {
  const update = useUpdateMessageList();
  return useCallback(
    (messages: TMessage[]) => {
      update((list) => prependHistoryMessages(list, messages));
    },
    [update]
  );
};

export const useReplaceWithAnchorWindow = () => {
  const update = useUpdateMessageList();
  return useCallback(
    (conversationId: string, messages: TMessage[]) => {
      update((list) => mergeLoadedPageWithCurrent(conversationId, messages, list));
    },
    [update]
  );
};

export const useLoadPreviousMessagePage = (conversationId?: string) => {
  const currentList = useMessageList();
  const currentWindowFirstMessageId = currentList[0]?.id;
  const pagination = useMessagePaginationState();
  const setPagination = useUpdateMessagePaginationState();
  const updateMessageWindow = useUpdateMessageList();

  useEffect(
    () => () => {
      if (conversationId) invalidateMessageHistoryRequest(conversationId);
    },
    [conversationId]
  );

  return useCallback(async () => {
    if (!conversationId || !pagination.oldestCursor || !pagination.hasMoreBefore || pagination.isLoadingBefore) {
      return false;
    }

    const request = beginAbortableMessageHistoryRequest(conversationId);
    const requestEpoch = request.epoch;
    const branchRevision = getSynonBiomedBranchSelectionRevision(conversationId);
    setPagination((current) => ({
      ...current,
      isLoadingBefore: true,
      isLoadingAnchor: false,
    }));
    try {
      const page = await loadConversationMessagePage(conversationId, {
        limit: INTERACTIVE_MESSAGE_PAGE_LIMIT,
        before: pagination.oldestCursor,
        contentMode: 'compact',
        branchId: pagination.branchId,
        signal: request.controller.signal,
      });
      if (!isCurrentMessageHistoryRequest(conversationId, requestEpoch, branchRevision)) return false;
      const messages = normalizeDbMessagePage(page.items, page.through_publication_sequence);
      const existingWindowFirstMessageId = currentWindowFirstMessageId;
      updateMessageWindow((list) => prependHistoryMessages(list, messages));
      setPagination((current) => ({
        ...current,
        conversationId,
        oldestCursor: page.oldest_cursor ?? current.oldestCursor,
        newestCursor: current.newestCursor ?? page.newest_cursor ?? undefined,
        hasMoreBefore: page.has_more_before,
        hasMoreAfter: current.hasMoreAfter || page.has_more_after,
        isLoadingBefore: false,
        branchId: page.branch_id ?? current.branchId,
        branchGeneration: page.branch_generation ?? current.branchGeneration,
        historyRevision: messagePageHistoryRevision(page.through_publication_sequence) ?? current.historyRevision,
        groupBoundaryMessageIds: existingWindowFirstMessageId
          ? Array.from(new Set([...(current.groupBoundaryMessageIds ?? []), existingWindowFirstMessageId]))
          : current.groupBoundaryMessageIds,
      }));
      return true;
    } catch (error) {
      if (!isCurrentMessageHistoryRequest(conversationId, requestEpoch, branchRevision)) return false;
      setPagination((current) => ({ ...current, isLoadingBefore: false }));
      if (isMessageRequestAbort(error)) return false;
      if (isBackendHttpError(error) && error.status === 409) {
        selectSynonBiomedBranch(conversationId, pagination.branchId ?? null);
        return false;
      }
      console.error('[useLoadPreviousMessagePage] Failed to load previous messages:', error);
      return false;
    } finally {
      releaseAbortableMessageHistoryRequest(conversationId, request);
    }
  }, [
    conversationId,
    currentWindowFirstMessageId,
    pagination.hasMoreBefore,
    pagination.isLoadingBefore,
    pagination.oldestCursor,
    pagination.branchId,
    setPagination,
    updateMessageWindow,
  ]);
};

export const useLoadNextMessagePage = (conversationId?: string) => {
  const pagination = useMessagePaginationState();
  const setPagination = useUpdateMessagePaginationState();
  const update = useUpdateMessageList();

  useEffect(
    () => () => {
      if (conversationId) invalidateMessageHistoryRequest(conversationId);
    },
    [conversationId]
  );

  return useCallback(async () => {
    if (!conversationId || !pagination.newestCursor || !pagination.hasMoreAfter || pagination.isLoadingAfter) {
      return false;
    }
    const request = beginAbortableMessageHistoryRequest(conversationId);
    const requestEpoch = request.epoch;
    const branchRevision = getSynonBiomedBranchSelectionRevision(conversationId);
    setPagination((current) => ({
      ...current,
      isLoadingAfter: true,
      isLoadingAnchor: false,
    }));
    try {
      const page = await loadConversationMessagePage(conversationId, {
        limit: INTERACTIVE_MESSAGE_PAGE_LIMIT,
        after: pagination.newestCursor,
        contentMode: 'compact',
        branchId: pagination.branchId,
        signal: request.controller.signal,
      });
      if (!isCurrentMessageHistoryRequest(conversationId, requestEpoch, branchRevision)) return false;
      const messages = normalizeDbMessagePage(page.items, page.through_publication_sequence);
      update((list) => appendHistoryMessages(list, messages));
      setPagination((current) => ({
        ...current,
        conversationId,
        oldestCursor: current.oldestCursor ?? page.oldest_cursor ?? undefined,
        newestCursor: page.newest_cursor ?? current.newestCursor,
        hasMoreBefore: current.hasMoreBefore || page.has_more_before,
        hasMoreAfter: page.has_more_after,
        isLoadingAfter: false,
        branchId: page.branch_id ?? current.branchId,
        branchGeneration: page.branch_generation ?? current.branchGeneration,
        historyRevision: messagePageHistoryRevision(page.through_publication_sequence) ?? current.historyRevision,
        groupBoundaryMessageIds: [],
      }));
      return true;
    } catch (error) {
      if (!isCurrentMessageHistoryRequest(conversationId, requestEpoch, branchRevision)) return false;
      setPagination((current) => ({ ...current, isLoadingAfter: false }));
      if (isMessageRequestAbort(error)) return false;
      if (isBackendHttpError(error) && error.status === 409) {
        selectSynonBiomedBranch(conversationId, pagination.branchId ?? null);
        return false;
      }
      console.error('[useLoadNextMessagePage] Failed to load newer messages:', error);
      return false;
    } finally {
      releaseAbortableMessageHistoryRequest(conversationId, request);
    }
  }, [
    conversationId,
    pagination.branchId,
    pagination.hasMoreAfter,
    pagination.isLoadingAfter,
    pagination.newestCursor,
    setPagination,
    update,
  ]);
};

export const useLoadAnchorMessageWindow = (conversationId?: string) => {
  const pagination = useMessagePaginationState();
  const setPagination = useUpdateMessagePaginationState();
  const replaceWithAnchorWindow = useReplaceWithAnchorWindow();

  useEffect(
    () => () => {
      if (conversationId) invalidateMessageHistoryRequest(conversationId);
    },
    [conversationId]
  );

  return useCallback(
    async (messageId: string) => {
      if (!conversationId || !messageId) return false;

      const request = beginAbortableMessageHistoryRequest(conversationId);
      const requestEpoch = request.epoch;
      const branchRevision = getSynonBiomedBranchSelectionRevision(conversationId);
      setPagination((current) => ({
        ...current,
        isLoadingAnchor: true,
        isLoadingBefore: false,
        isLoadingAfter: false,
      }));
      try {
        const page = await loadConversationAnchorWindow(conversationId, messageId, {
          limit: INTERACTIVE_MESSAGE_PAGE_LIMIT,
          contentMode: 'compact',
          branchId: pagination.branchId,
          signal: request.controller.signal,
        });
        if (!isCurrentMessageHistoryRequest(conversationId, requestEpoch, branchRevision)) return false;
        replaceWithAnchorWindow(conversationId, normalizeDbMessagePage(page.items, page.through_publication_sequence));
        setPagination({
          conversationId,
          oldestCursor: page.oldest_cursor ?? undefined,
          newestCursor: page.newest_cursor ?? undefined,
          hasMoreBefore: page.has_more_before,
          hasMoreAfter: page.has_more_after,
          isLoadingBefore: false,
          isLoadingAfter: false,
          isLoadingAnchor: false,
          branchId: page.branch_id,
          branchGeneration: page.branch_generation,
          historyRevision: messagePageHistoryRevision(page.through_publication_sequence),
          groupBoundaryMessageIds: [],
        });
        return true;
      } catch (error) {
        if (!isCurrentMessageHistoryRequest(conversationId, requestEpoch, branchRevision)) return false;
        setPagination((current) => ({ ...current, isLoadingAnchor: false }));
        if (isMessageRequestAbort(error)) return false;
        if (isBackendHttpError(error) && error.status === 409) {
          selectSynonBiomedBranch(conversationId, pagination.branchId ?? null);
          return false;
        }
        console.error('[useLoadAnchorMessageWindow] Failed to load anchor messages:', error);
        return false;
      } finally {
        releaseAbortableMessageHistoryRequest(conversationId, request);
      }
    },
    [conversationId, pagination.branchId, replaceWithAnchorWindow, setPagination]
  );
};

export const useMessageLstCache = (key: string, ownerId = '') => {
  const currentList = useMessageList();
  const update = useUpdateMessageList();
  const setLoading = useUpdateMessageListLoading();
  const setLoadError = useUpdateMessageListLoadError();
  const pagination = useMessagePaginationState();
  const setPagination = useUpdateMessagePaginationState();
  const activeKeyRef = useRef(key);
  const initialHistoryBaselineRef = useRef<{
    conversationId: string;
    messages: Set<TMessage>;
  } | null>(null);
  const initialHistoryRetryRef = useRef<{
    conversationId: string;
    attempt: number;
    timer: ReturnType<typeof setTimeout> | null;
  } | null>(null);
  const loadMessagesRef = useRef<
    (replace?: boolean, source?: MessageListLoadSource, blockCachedWindow?: boolean) => Promise<TMessage[]>
  >(async () => []);
  activeKeyRef.current = key;
  const clearInitialHistoryRetry = useCallback((conversationId?: string) => {
    const retry = initialHistoryRetryRef.current;
    if (!retry || (conversationId && retry.conversationId !== conversationId)) return;
    if (retry.timer !== null) clearTimeout(retry.timer);
    initialHistoryRetryRef.current = null;
  }, []);
  const loadMessages = useCallback(
    async (
      replace = false,
      source: MessageListLoadSource = 'refresh',
      blockCachedWindow = true
    ): Promise<TMessage[]> => {
      if (!key) return [];
      if (source !== 'initial') clearInitialHistoryRetry(key);
      const requestEpoch = beginMessageHistoryRequest(key);
      const branchRevision = getSynonBiomedBranchSelectionRevision(key);
      const blocksVisibleWindow = blockCachedWindow && (source === 'initial' || source === 'branch');
      if (blocksVisibleWindow) setLoading(true);
      setPagination((current) => ({
        ...(current.conversationId === key ? current : EMPTY_MESSAGE_PAGINATION_STATE),
        conversationId: key,
        isLoadingBefore: false,
        isLoadingAfter: false,
        isLoadingAnchor: false,
      }));
      try {
        const result = await loadLatestConversationMessagesShared({
          ownerId,
          conversationId: key,
          branchRevision,
          limit: INTERACTIVE_MESSAGE_PAGE_LIMIT,
          contentMode: 'compact',
          force: source !== 'initial' && source !== 'terminal-mount',
          dedupeInFlight: source === 'live',
          retryTransient: source !== 'initial' && source !== 'terminal-mount' && source !== 'live',
        });
        if (
          !isCurrentMessageHistoryRequest(key, requestEpoch, branchRevision) ||
          activeKeyRef.current !== key ||
          branchRevision !== getSynonBiomedBranchSelectionRevision(key)
        )
          return [];

        const messages = result?.items
          ? normalizeDbMessagePage(result.items, result.through_publication_sequence)
          : undefined;
        if (messages && Array.isArray(messages)) {
          clearInitialHistoryRetry(key);
          const nextPagination: MessagePaginationState = {
            conversationId: key,
            oldestCursor: result.oldest_cursor ?? undefined,
            newestCursor: result.newest_cursor ?? undefined,
            hasMoreBefore: result.has_more_before,
            hasMoreAfter: result.has_more_after,
            isLoadingBefore: false,
            isLoadingAfter: false,
            isLoadingAnchor: false,
            branchId: result.branch_id,
            branchGeneration: result.branch_generation,
            historyRevision: messagePageHistoryRevision(result.through_publication_sequence),
            groupBoundaryMessageIds: [],
          };
          update((existingList) => {
            const initialBaseline =
              source === 'initial' && initialHistoryBaselineRef.current?.conversationId === key
                ? initialHistoryBaselineRef.current.messages
                : null;
            const liveWindow = initialBaseline
              ? existingList.filter((message) => !initialBaseline.has(message))
              : existingList;
            const nextList = replace
              ? messages
              : mergeLoadedPageWithCurrent(
                  key,
                  messages,
                  withoutOptimisticUserMessages(liveWindow),
                  true,
                  source === 'cursor-reset'
                );
            if (source === 'initial') initialHistoryBaselineRef.current = null;
            writeCachedMessageWindow(key, ownerId, branchRevision, nextList, nextPagination);
            return nextList;
          });
          setPagination(nextPagination);
          setLoadError(null);
          if (messages.length > 0) scheduleConversationRoutePerformanceStage('message-first-paint', 2);
          scheduleConversationRoutePerformanceStage('settled', 2);
          return messages;
        }
        scheduleConversationRoutePerformanceStage('settled', 2);
        return [];
      } catch (error) {
        if (isMessageRequestAbort(error)) throw error;
        if (isConversationHistoryNotReady(error) && (source === 'initial' || source === 'live')) {
          if (isCurrentMessageHistoryRequest(key, requestEpoch, branchRevision) && activeKeyRef.current === key) {
            setLoadError(null);
            scheduleConversationRoutePerformanceStage('settled', 2);
            if (source === 'initial') {
              // Keep converging for as long as this route is mounted. The
              // delay is capped, but the logical transcript has no retry-count
              // or wall-clock limit; navigation cleanup cancels the timer.
              const previous = initialHistoryRetryRef.current;
              const attempt = previous?.conversationId === key ? previous.attempt : 0;
              if (previous?.timer !== null && previous?.timer !== undefined) clearTimeout(previous.timer);
              const delayMs =
                TRANSIENT_HISTORY_RETRY_DELAYS_MS[Math.min(attempt, TRANSIENT_HISTORY_RETRY_DELAYS_MS.length - 1)];
              const retry = {
                conversationId: key,
                attempt: attempt + 1,
                timer: null as ReturnType<typeof setTimeout> | null,
              };
              retry.timer = setTimeout(() => {
                retry.timer = null;
                if (activeKeyRef.current !== key || initialHistoryRetryRef.current !== retry) return;
                void loadMessagesRef.current(false, 'initial', false).catch((retryError) => {
                  if (isMessageRequestAbort(retryError)) return;
                  console.error('[useMessageLstCache] Failed to reconcile conversation history:', retryError);
                });
              }, delayMs);
              initialHistoryRetryRef.current = retry;
            }
          }
          return [];
        }
        clearInitialHistoryRetry(key);
        if (isCurrentMessageHistoryRequest(key, requestEpoch, branchRevision) && activeKeyRef.current === key) {
          setLoadError({ source });
          scheduleConversationRoutePerformanceStage('settled', 2);
        }
        throw error;
      } finally {
        const currentRequest = isCurrentMessageHistoryRequest(key, requestEpoch, branchRevision);
        if (blocksVisibleWindow && currentRequest && activeKeyRef.current === key) {
          setLoading(false);
        }
        releaseMessageHistoryRequest(key, requestEpoch);
      }
    },
    [clearInitialHistoryRetry, key, ownerId, setLoadError, setLoading, setPagination, update]
  );
  loadMessagesRef.current = loadMessages;

  useLayoutEffect(() => {
    clearInitialHistoryRetry();
    const branchRevision = key ? getSynonBiomedBranchSelectionRevision(key) : 0;
    if (key) invalidateMessageHistoryRequest(key);
    const cached = key ? readCachedMessageWindow(key, ownerId, branchRevision) : null;
    const initialWindow = cached?.messages ?? [];
    initialHistoryBaselineRef.current = key ? { conversationId: key, messages: new Set(initialWindow) } : null;
    update(initialWindow);
    const cancelCachedPaint = cached?.messages.length
      ? scheduleConversationRoutePerformanceStage('message-first-paint')
      : () => {};
    setLoadError(null);
    setPagination(
      cached?.pagination ?? {
        ...EMPTY_MESSAGE_PAGINATION_STATE,
        conversationId: key || undefined,
      }
    );
    if (!key) {
      setLoading(false);
      return;
    }
    // A revisited conversation already has an owner/branch-fenced authoritative
    // window. Keep it interactive while the latest compact page revalidates in
    // the background; only a true cache miss renders the blocking skeleton.
    // The route transition already cleared the previous conversation window.
    // Merge the initial durable page with any direct stream publications that
    // arrived while this request was in flight; replacing here causes the
    // first visible tokens and tool rows to flash out of the viewport.
    void loadMessages(false, 'initial', !cached).catch((error) => {
      if (isMessageRequestAbort(error)) return;
      console.error('[useMessageLstCache] Failed to load messages from database:', error);
    });
    return () => {
      if (initialHistoryBaselineRef.current?.conversationId === key) initialHistoryBaselineRef.current = null;
      clearInitialHistoryRetry(key);
      cancelCachedPaint();
      invalidateMessageHistoryRequest(key);
      cancelConversationMessageRequests(ownerId, key);
    };
  }, [clearInitialHistoryRetry, key, loadMessages, ownerId, setLoadError, setLoading, setPagination, update]);

  useEffect(() => {
    if (!key || currentList.length === 0) return;
    const messages = currentList.filter((message) => message.conversation_id === key);
    if (messages.length !== currentList.length) return;
    writeCachedMessageWindow(key, ownerId, getSynonBiomedBranchSelectionRevision(key), messages, pagination);
  }, [currentList, key, ownerId, pagination]);

  useEffect(() => {
    if (!key || typeof window === 'undefined') return;
    const handleBranchSelection = (event: Event) => {
      const detail = (event as CustomEvent<SynonBiomedBranchSelectionEventDetail>).detail;
      if (detail?.conversationId !== key) return;
      if (detail.rollback) return;
      const displayedBranchId = pagination.branchId ?? detail.previousBranchId;
      setLoadError(null);
      void loadMessages(true, 'branch').catch((error) => {
        if (isMessageRequestAbort(error)) return;
        rollbackSynonBiomedBranchSelection(key, detail.branchId, displayedBranchId, detail.revision);
        console.error('[useMessageLstCache] Failed to switch branch:', error);
      });
    };
    window.addEventListener(SYNON_BIOMED_BRANCH_SELECTION_EVENT, handleBranchSelection);
    return () => window.removeEventListener(SYNON_BIOMED_BRANCH_SELECTION_EVENT, handleBranchSelection);
  }, [key, loadMessages, pagination.branchId, setLoadError]);

  useEffect(() => {
    if (!key) return;
    const reloadCanonicalHistory = (clearVisibleWindow: boolean, source: 'rebase' | 'cursor-reset') => {
      invalidateMessageHistoryRequest(key);
      resetSynonBiomedBranchSelection(key);
      if (clearVisibleWindow) update([]);
      setLoadError(null);
      if (clearVisibleWindow) {
        setPagination({ ...EMPTY_MESSAGE_PAGINATION_STATE, conversationId: key });
      }
      void loadMessages(false, source).catch((error) => {
        if (isMessageRequestAbort(error)) return;
        console.error('[useMessageLstCache] Failed to rebase canonical history:', error);
      });
    };
    const unsubscribeRebase = ipcBridge.conversation.historyRebased.on((payload) => {
      if (payload.conversation_id === key && payload.root_frame_id === key) reloadCanonicalHistory(true, 'rebase');
    });
    // A realtime cursor reset only invalidates the delivery cursor. It does
    // not invalidate the conversation itself. Keep the mounted transcript
    // visible while the bounded tail is refreshed; clearing it here made a
    // long-running task look as if its original conversation had vanished.
    const unsubscribeCursorReset = ipcBridge.realtime.cursorReset.on(() =>
      reloadCanonicalHistory(false, 'cursor-reset')
    );
    return () => {
      unsubscribeRebase();
      unsubscribeCursorReset();
    };
  }, [key, loadMessages, setLoadError, setPagination, update]);

  useEffect(() => {
    if (!key) {
      return;
    }

    return ipcBridge.conversation.userCreated.on((payload) => {
      if (payload.conversation_id !== key) {
        return;
      }

      update((list) => {
        const index = getOrBuildIndex(list);
        return composeMessageWithIndex(
          {
            id: payload.msg_id,
            msg_id: payload.msg_id,
            conversation_id: payload.conversation_id,
            type: 'text',
            position: payload.position,
            status: payload.status,
            hidden: payload.hidden,
            created_at: payload.created_at,
            content: {
              content: payload.content,
            },
          },
          list,
          index
        );
      });
    });
  }, [key, update]);

  return loadMessages;
};

export const beforeUpdateMessageList = (fn: (list: TMessage[]) => TMessage[]) => {
  beforeUpdateMessageListStack.push(fn);
  return () => {
    beforeUpdateMessageListStack.splice(beforeUpdateMessageListStack.indexOf(fn), 1);
  };
};
export {
  ChatKeyProvider,
  MessagePaginationProvider,
  MessageListLoadErrorProvider,
  MessageListLoadingProvider,
  MessageListProvider,
  useChatKey,
  useMessagePaginationState,
  useMessageList,
  useMessageListLoadError,
  useMessageListLoading,
  useUpdateMessagePaginationState,
  useUpdateMessageList,
};
