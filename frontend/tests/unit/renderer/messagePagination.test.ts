/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { BackendHttpError } from '@/common/adapter/httpBridge';
import * as branches from '@/renderer/services/synonBiomedConversationBranches';
import * as ipcBridge from '@/common/adapter/ipcBridge';
import {
  loadAllConversationMessagesPaged,
  loadConversationAnchorWindow,
  loadLatestConversationMessages,
} from '@/renderer/utils/chat/messagePagination';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/common/adapter/ipcBridge', async () => {
  const actual = await vi.importActual<typeof import('@/common/adapter/ipcBridge')>('@/common/adapter/ipcBridge');
  return {
    ...actual,
    requestConversationMessages: vi.fn(),
  };
});

vi.mock('@/renderer/services/synonBiomedConversationBranches', async () => {
  const actual = await vi.importActual<typeof import('@/renderer/services/synonBiomedConversationBranches')>(
    '@/renderer/services/synonBiomedConversationBranches'
  );
  return {
    ...actual,
    getSelectedSynonBiomedBranch: vi.fn(),
    getSynonBiomedBranchSelectionRevision: vi.fn(),
  };
});

const requestConversationMessages = vi.mocked(ipcBridge.requestConversationMessages);
const getSelectedSynonBiomedBranch = vi.mocked(branches.getSelectedSynonBiomedBranch);
const getSynonBiomedBranchSelectionRevision = vi.mocked(branches.getSynonBiomedBranchSelectionRevision);

describe('message pagination helpers', () => {
  beforeEach(() => {
    requestConversationMessages.mockReset();
    getSelectedSynonBiomedBranch.mockReturnValue(null);
    getSynonBiomedBranchSelectionRevision.mockReturnValue(0);
  });

  it('loads the latest compact page with cursor params', async () => {
    requestConversationMessages.mockResolvedValue({
      items: [],
      oldest_cursor: null,
      newest_cursor: null,
      has_more_before: false,
      has_more_after: false,
    });

    await loadLatestConversationMessages('conversation-1', { limit: 25, contentMode: 'compact' });

    expect(requestConversationMessages).toHaveBeenCalledWith(
      {
        conversation_id: 'conversation-1',
        limit: 25,
        content_mode: 'compact',
      },
      undefined
    );
  });

  it('walks older pages and returns all messages in display order', async () => {
    requestConversationMessages
      .mockResolvedValueOnce({
        items: [{ id: 'm3' }, { id: 'm4' }],
        oldest_cursor: 'c3',
        newest_cursor: 'c4',
        has_more_before: true,
        has_more_after: false,
        branch_id: 'br_00000001',
        branch_generation: 3,
      })
      .mockResolvedValueOnce({
        items: [{ id: 'm1' }, { id: 'm2' }],
        oldest_cursor: 'c1',
        newest_cursor: 'c2',
        has_more_before: false,
        has_more_after: true,
        branch_id: 'br_00000001',
        branch_generation: 3,
      });

    const messages = await loadAllConversationMessagesPaged('conversation-1', { limit: 2 });

    expect(messages.map((message) => message.id)).toEqual(['m1', 'm2', 'm3', 'm4']);
    expect(requestConversationMessages).toHaveBeenNthCalledWith(
      1,
      {
        conversation_id: 'conversation-1',
        limit: 2,
        content_mode: 'full',
      },
      undefined
    );
    expect(requestConversationMessages).toHaveBeenNthCalledWith(
      2,
      {
        conversation_id: 'conversation-1',
        limit: 2,
        before: 'c3',
        content_mode: 'full',
        branch_id: 'br_00000001',
      },
      undefined
    );
  });

  it('pins every page to the canonical branch returned by the first snapshot', async () => {
    requestConversationMessages
      .mockResolvedValueOnce({
        items: [{ id: 'm2' }],
        oldest_cursor: 'branch-cursor',
        newest_cursor: 'branch-cursor',
        has_more_before: true,
        has_more_after: false,
        branch_id: 'br_00000002',
        branch_generation: 4,
      })
      .mockResolvedValueOnce({
        items: [{ id: 'm1' }],
        oldest_cursor: 'branch-start',
        newest_cursor: 'branch-start',
        has_more_before: false,
        has_more_after: true,
        branch_id: 'br_00000002',
        branch_generation: 4,
      });

    const messages = await loadAllConversationMessagesPaged('conversation-branch', { limit: 1 });

    expect(messages.map((message) => message.id)).toEqual(['m1', 'm2']);
    expect(requestConversationMessages).toHaveBeenNthCalledWith(
      2,
      {
        conversation_id: 'conversation-branch',
        limit: 1,
        before: 'branch-cursor',
        content_mode: 'full',
        branch_id: 'br_00000002',
      },
      undefined
    );
  });

  it('rejects a response from a different branch instead of mixing histories', async () => {
    requestConversationMessages.mockResolvedValue({
      items: [],
      oldest_cursor: null,
      newest_cursor: null,
      has_more_before: false,
      has_more_after: false,
      branch_id: 'br_00000003',
      branch_generation: 2,
    });

    await expect(
      loadLatestConversationMessages('conversation-branch', {
        branchId: 'br_00000004',
      })
    ).rejects.toThrow('conversation_branch_response_mismatch');
  });

  it('loads an anchor window by message id', async () => {
    requestConversationMessages.mockResolvedValue({
      items: [{ id: 'target' }],
      oldest_cursor: 'c1',
      newest_cursor: 'c1',
      has_more_before: true,
      has_more_after: true,
    });

    const page = await loadConversationAnchorWindow('conversation-1', 'target', { limit: 31 });

    expect(page.items).toEqual([{ id: 'target' }]);
    expect(requestConversationMessages).toHaveBeenCalledWith(
      {
        conversation_id: 'conversation-1',
        limit: 31,
        anchor_message_id: 'target',
        content_mode: 'compact',
      },
      undefined
    );
  });

  it('retries on transient 503 not-ready text errors', async () => {
    vi.useFakeTimers();
    try {
      const notReady = new BackendHttpError({
        method: 'GET',
        path: '/api/conversations/conversation-1/messages',
        status: 503,
        body: { message: 'transcript runtime is not available' },
      });
      const successPage = {
        items: [{ id: 'm1' }],
        oldest_cursor: null,
        newest_cursor: null,
        has_more_before: false,
        has_more_after: false,
      };

      requestConversationMessages
        .mockRejectedValueOnce(notReady)
        .mockRejectedValueOnce(notReady)
        .mockResolvedValueOnce(successPage);

      const resultPromise = loadLatestConversationMessages('conversation-1', { limit: 4, contentMode: 'compact' });
      await vi.runAllTimersAsync();
      const result = await resultPromise;

      expect(requestConversationMessages).toHaveBeenCalledTimes(3);
      expect(result).toEqual(successPage);
    } finally {
      vi.useRealTimers();
    }
  });

  it('returns interactive history-not-ready responses without entering the bounded retry window', async () => {
    const notReady = new BackendHttpError({
      method: 'GET',
      path: '/api/conversations/conversation-1/messages',
      status: 503,
      body: { code: 'HISTORY_NOT_READY' },
    });
    requestConversationMessages.mockRejectedValue(notReady);

    await expect(
      loadLatestConversationMessages('conversation-1', {
        contentMode: 'compact',
        retryTransient: false,
      })
    ).rejects.toBe(notReady);
    expect(requestConversationMessages).toHaveBeenCalledTimes(1);
  });

  it('waits through the full bounded projection window before failing history', async () => {
    vi.useFakeTimers();
    try {
      const notReady = new BackendHttpError({
        method: 'GET',
        path: '/api/conversations/conversation-1/messages',
        status: 503,
        body: { code: 'HISTORY_NOT_READY' },
      });
      const successPage = {
        items: [{ id: 'm-after-projection' }],
        oldest_cursor: null,
        newest_cursor: null,
        has_more_before: false,
        has_more_after: false,
      };
      requestConversationMessages.mockRejectedValueOnce(notReady);
      for (let index = 0; index < 7; index += 1) {
        requestConversationMessages.mockRejectedValueOnce(notReady);
      }
      requestConversationMessages.mockResolvedValueOnce(successPage);

      const resultPromise = loadLatestConversationMessages('conversation-1');
      await vi.runAllTimersAsync();

      await expect(resultPromise).resolves.toEqual(successPage);
      expect(requestConversationMessages).toHaveBeenCalledTimes(9);
    } finally {
      vi.useRealTimers();
    }
  });
});
