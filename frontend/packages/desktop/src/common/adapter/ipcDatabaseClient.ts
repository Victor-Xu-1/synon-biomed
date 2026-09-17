/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * IPC Bridge → HTTP/WS adapter.
 *
 * This file replaces the original IPC bridge calls with HTTP REST and WebSocket
 * calls routed through the Synon Biomed WebHost gateway. Electron-native
 * operations (window controls, native dialogs, auto-update, devtools, zoom,
 * CDP, deep links) remain as IPC.
 */

import { fromApiPaginatedConversations } from './apiModelMapper';
import { httpGet, httpRequest, withResponseMap } from './httpBridge';
import { fromApiSearchResult, type ApiMessageSearchItem } from './searchMapper';

export type PaginatedResult<T> = {
  items: T[];
  total: number;
  has_more: boolean;
  next_cursor?: string | null;
};

export type MessageCursorPage<T> = {
  items: T[];
  oldest_cursor: string | null;
  newest_cursor: string | null;
  has_more_before: boolean;
  has_more_after: boolean;
  branch_id?: string;
  branch_generation?: number;
  through_publication_sequence?: number;
};

export type GetConversationMessagesParams = {
  conversation_id: string;
  limit?: number;
  before?: string;
  after?: string;
  anchor_message_id?: string;
  content_mode?: 'compact' | 'full';
  branch_id?: string;
};

export function conversationMessagesPath(p: GetConversationMessagesParams): string {
  const params = new URLSearchParams();
  if (p.limit !== undefined) params.set('limit', String(p.limit));
  if (p.before) params.set('before', p.before);
  if (p.after) params.set('after', p.after);
  if (p.anchor_message_id) params.set('anchor_message_id', p.anchor_message_id);
  if (p.content_mode) params.set('content_mode', p.content_mode);
  if (p.branch_id) params.set('branch_id', p.branch_id);
  const qs = params.toString();
  return `/api/conversations/${encodeURIComponent(p.conversation_id)}/messages${qs ? `?${qs}` : ''}`;
}

/**
 * Abortable message-history read used by route prefetch and the active
 * conversation store. The legacy bridge invoke delegates here so both paths
 * have identical URL, timeout, auth, and response-unwrapping behavior.
 */
export function requestConversationMessages(
  params: GetConversationMessagesParams,
  signal?: AbortSignal
): Promise<MessageCursorPage<import('@/common/chat/chatLib').TMessage>> {
  return httpRequest('GET', conversationMessagesPath(params), undefined, {
    timeoutMs: 15_000,
    signal,
    silentErrorCodes: ['HISTORY_NOT_READY'],
  });
}

export type ConversationReadCursor = {
  root_frame_id: string;
  message_uuid: string | null;
  message_index: number;
  updated_at: string;
};

export type PutConversationReadCursorParams = {
  conversation_id: string;
  message_uuid: string;
  message_index: number;
  observed_message_uuid?: string;
  observed_message_index?: number;
  repair?: boolean;
  keepalive?: boolean;
};

export const database = {
  getConversationMessages: {
    provider: () => {},
    invoke: requestConversationMessages,
  },
  getConversationMessage: httpGet<
    import('@/common/chat/chatLib').TMessage,
    { conversation_id: string; message_id: string; branch_id?: string }
  >((p) => {
    const path = `/api/conversations/${encodeURIComponent(p.conversation_id)}/messages/${encodeURIComponent(p.message_id)}`;
    return p.branch_id ? `${path}?branch_id=${encodeURIComponent(p.branch_id)}` : path;
  }),
  getConversationReadCursor: httpGet<ConversationReadCursor | undefined, { conversation_id: string }>(
    (p) => `/api/frames/${encodeURIComponent(p.conversation_id)}/read-cursor`
  ),
  putConversationReadCursor: {
    provider: () => {},
    invoke: (async (p: PutConversationReadCursorParams) =>
      httpRequest<ConversationReadCursor>(
        'PUT',
        `/api/frames/${encodeURIComponent(p.conversation_id)}/read-cursor`,
        {
          message_uuid: p.message_uuid,
          message_index: p.message_index,
          observed_message_uuid: p.observed_message_uuid,
          observed_message_index: p.observed_message_index,
          repair: p.repair ?? false,
        },
        { keepalive: p.keepalive }
      )) as (p: PutConversationReadCursorParams) => Promise<ConversationReadCursor>,
  },
  getUserConversations: withResponseMap(
    httpGet<PaginatedResult<import('@/common/config/storage').TChatConversation>, { cursor?: string; limit?: number }>(
      (p) => {
        const params = new URLSearchParams();
        if (p.cursor) params.set('cursor', p.cursor);
        if (p.limit) params.set('limit', String(p.limit));
        const qs = params.toString();
        return `/api/conversations${qs ? `?${qs}` : ''}`;
      }
    ),
    fromApiPaginatedConversations
  ),
  searchConversationMessages: withResponseMap(
    httpGet<PaginatedResult<ApiMessageSearchItem>, { keyword: string; page?: number; page_size?: number }>(
      (p) =>
        `/api/conversations/search?q=${encodeURIComponent(p.keyword)}&page=${
          p.page ?? 0
        }&page_size=${p.page_size ?? 20}`
    ),
    fromApiSearchResult
  ),
};
