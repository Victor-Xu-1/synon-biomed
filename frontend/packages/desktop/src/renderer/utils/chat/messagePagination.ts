/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { requestConversationMessages, type MessageCursorPage } from '@/common/adapter/ipcBridge';
import type { TMessage } from '@/common/chat/chatLib';
import {
  getSelectedSynonBiomedBranch,
  getSynonBiomedBranchSelectionRevision,
} from '@/renderer/services/synonBiomedConversationBranches';
import { withTransientHistoryRetry } from '@/renderer/services/synonBiomedHistoryRetry';

export type MessageContentMode = 'compact' | 'full';

export type LoadConversationMessagePageOptions = {
  limit?: number;
  before?: string;
  after?: string;
  anchorMessageId?: string;
  contentMode?: MessageContentMode;
  branchId?: string;
  signal?: AbortSignal;
  retryTransient?: boolean;
};

// The reference 0.1.25 runtime uses a 200-message tail/older-page window and keeps a
// separate 100,000-item server range ceiling. The latter is not a total
// history cap; ordinary UI reads stay at the 200-message window.
export const DEFAULT_MESSAGE_PAGE_LIMIT = 200;
// The interactive transcript only needs a bounded tail for first paint. Older
// messages remain available through cursor pagination, while keeping large
// long-task conversations from parsing and projecting hundreds of rich tool
// payloads during every route change.
export const INTERACTIVE_MESSAGE_PAGE_LIMIT = 80;
export const MAX_MESSAGE_PAGE_LIMIT = 100_000;

export async function loadConversationMessagePage(
  conversationId: string,
  options: LoadConversationMessagePageOptions = {}
): Promise<MessageCursorPage<TMessage>> {
  const load = () => loadConversationMessagePageOnce(conversationId, options);
  return options.retryTransient === false ? load() : withTransientHistoryRetry(load, options.signal);
}

async function loadConversationMessagePageOnce(
  conversationId: string,
  options: LoadConversationMessagePageOptions = {}
): Promise<MessageCursorPage<TMessage>> {
  const branchId = options.branchId ?? getSelectedSynonBiomedBranch(conversationId);
  const page = await requestConversationMessages(
    {
      conversation_id: conversationId,
      limit: options.limit ?? DEFAULT_MESSAGE_PAGE_LIMIT,
      ...(options.before ? { before: options.before } : {}),
      ...(options.after ? { after: options.after } : {}),
      ...(options.anchorMessageId ? { anchor_message_id: options.anchorMessageId } : {}),
      content_mode: options.contentMode ?? 'compact',
      ...(branchId ? { branch_id: branchId } : {}),
    },
    options.signal
  );
  if (branchId && page.branch_id !== branchId) throw new Error('conversation_branch_response_mismatch');
  if (
    page.branch_id !== undefined &&
    (!/^br_[0-9a-f]{8}$/.test(page.branch_id) ||
      !Number.isSafeInteger(page.branch_generation) ||
      Number(page.branch_generation) <= 0)
  ) {
    throw new Error('conversation_branch_response_invalid');
  }
  return page;
}

export function loadLatestConversationMessages(
  conversationId: string,
  options: Pick<
    LoadConversationMessagePageOptions,
    'limit' | 'contentMode' | 'branchId' | 'signal' | 'retryTransient'
  > = {}
): Promise<MessageCursorPage<TMessage>> {
  return loadConversationMessagePage(conversationId, options);
}

export function loadConversationAnchorWindow(
  conversationId: string,
  messageId: string,
  options: Pick<
    LoadConversationMessagePageOptions,
    'limit' | 'contentMode' | 'branchId' | 'signal' | 'retryTransient'
  > = {}
): Promise<MessageCursorPage<TMessage>> {
  return loadConversationMessagePage(conversationId, {
    ...options,
    anchorMessageId: messageId,
  });
}

export async function loadAllConversationMessagesPaged(
  conversationId: string,
  options: Pick<LoadConversationMessagePageOptions, 'limit' | 'contentMode' | 'branchId' | 'signal'> = {}
): Promise<TMessage[]> {
  const limit = options.limit ?? MAX_MESSAGE_PAGE_LIMIT;
  const contentMode = options.contentMode ?? 'full';
  const selectionRevision = getSynonBiomedBranchSelectionRevision(conversationId);
  const latest = await loadConversationMessagePage(conversationId, {
    limit,
    contentMode,
    signal: options.signal,
    ...(options.branchId ? { branchId: options.branchId } : {}),
  });
  if (selectionRevision !== getSynonBiomedBranchSelectionRevision(conversationId)) {
    throw new Error('conversation_branch_selection_changed');
  }
  const branchId = latest.branch_id;
  const loadOlderPages = async (before: string): Promise<TMessage[][]> => {
    const page = await loadConversationMessagePage(conversationId, {
      limit,
      before,
      contentMode,
      signal: options.signal,
      ...(branchId ? { branchId } : {}),
    });
    if (selectionRevision !== getSynonBiomedBranchSelectionRevision(conversationId)) {
      throw new Error('conversation_branch_selection_changed');
    }
    const nextCursor = page.oldest_cursor ?? undefined;
    const olderPages = page.has_more_before && nextCursor ? await loadOlderPages(nextCursor) : [];
    return [...olderPages, page.items];
  };

  const before = latest.oldest_cursor ?? undefined;
  const olderPages = latest.has_more_before && before ? await loadOlderPages(before) : [];

  return [...olderPages, latest.items].flat();
}
