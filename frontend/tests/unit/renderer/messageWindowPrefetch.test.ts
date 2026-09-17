import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { loadLatestConversationMessagesMock } = vi.hoisted(() => ({
  loadLatestConversationMessagesMock: vi.fn(),
}));

vi.mock('@/renderer/utils/chat/messagePagination', () => ({
  INTERACTIVE_MESSAGE_PAGE_LIMIT: 80,
  loadLatestConversationMessages: loadLatestConversationMessagesMock,
}));

import {
  cancelConversationMessageRequests,
  clearMessageWindowRequests,
  loadLatestConversationMessagesShared,
  MAX_MESSAGE_WINDOW_REQUESTS,
  MESSAGE_WINDOW_DEDUPE_MS,
  MESSAGE_WINDOW_SETTLED_REUSE_MS,
  prefetchConversationMessages,
} from '@/renderer/pages/conversation/Messages/messageWindowPrefetch';

const page = {
  items: [],
  has_more_before: false,
  has_more_after: false,
};

describe('messageWindowPrefetch', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-07-31T00:00:00.000Z'));
    loadLatestConversationMessagesMock.mockReset().mockResolvedValue(page);
    clearMessageWindowRequests();
  });

  afterEach(() => {
    clearMessageWindowRequests();
    vi.useRealTimers();
  });

  it('reuses one owner-scoped settled page only during the short mount grace window', async () => {
    const request = {
      ownerId: 'owner-a',
      conversationId: 'conversation-a',
      branchRevision: 3,
    };

    const first = loadLatestConversationMessagesShared(request);
    const second = loadLatestConversationMessagesShared(request);

    expect(second).toBe(first);
    await expect(first).resolves.toBe(page);
    expect(loadLatestConversationMessagesMock).toHaveBeenCalledTimes(1);

    await loadLatestConversationMessagesShared(request);
    expect(loadLatestConversationMessagesMock).toHaveBeenCalledTimes(1);

    vi.advanceTimersByTime(MESSAGE_WINDOW_SETTLED_REUSE_MS);
    await loadLatestConversationMessagesShared(request);
    expect(loadLatestConversationMessagesMock).toHaveBeenCalledTimes(2);
  });

  it('keeps force refresh semantics after a request has settled', async () => {
    const request = {
      ownerId: 'owner-force',
      conversationId: 'conversation-force',
      branchRevision: 0,
    };

    await loadLatestConversationMessagesShared(request);
    await loadLatestConversationMessagesShared({ ...request, force: true, dedupeInFlight: true });

    expect(loadLatestConversationMessagesMock).toHaveBeenCalledTimes(2);
  });

  it('consumes a completed click prefetch without issuing a duplicate mounted read', async () => {
    const request = {
      ownerId: 'owner-prefetch',
      conversationId: 'conversation-prefetch',
      branchRevision: 0,
    };

    await prefetchConversationMessages(request);
    expect(loadLatestConversationMessagesMock).toHaveBeenCalledTimes(1);
    await expect(loadLatestConversationMessagesShared(request)).resolves.toBe(page);
    expect(loadLatestConversationMessagesMock).toHaveBeenCalledTimes(1);

    vi.advanceTimersByTime(MESSAGE_WINDOW_SETTLED_REUSE_MS);
    await loadLatestConversationMessagesShared(request);
    expect(loadLatestConversationMessagesMock).toHaveBeenCalledTimes(2);
  });

  it('lets a live refresh share an initial request that is still converging', async () => {
    let resolveFirst!: (value: typeof page) => void;
    loadLatestConversationMessagesMock.mockImplementationOnce(
      () =>
        new Promise<typeof page>((resolve) => {
          resolveFirst = resolve;
        })
    );
    const request = {
      ownerId: 'owner-live',
      conversationId: 'conversation-live',
      branchRevision: 0,
    };

    const initial = loadLatestConversationMessagesShared(request);
    const live = loadLatestConversationMessagesShared({ ...request, force: true, dedupeInFlight: true });

    expect(live).toBe(initial);
    expect(loadLatestConversationMessagesMock).toHaveBeenCalledTimes(1);
    resolveFirst(page);
    await expect(live).resolves.toBe(page);
  });

  it('never shares a prefetched page across owners', async () => {
    await Promise.all([
      loadLatestConversationMessagesShared({
        ownerId: 'owner-b',
        conversationId: 'conversation-shared',
        branchRevision: 0,
      }),
      loadLatestConversationMessagesShared({
        ownerId: 'owner-c',
        conversationId: 'conversation-shared',
        branchRevision: 0,
      }),
    ]);

    expect(loadLatestConversationMessagesMock).toHaveBeenCalledTimes(2);
  });

  it('evicts a failed request so an immediate retry can recover', async () => {
    loadLatestConversationMessagesMock
      .mockRejectedValueOnce(new Error('temporary failure'))
      .mockResolvedValueOnce(page);
    const request = {
      ownerId: 'owner-d',
      conversationId: 'conversation-retry',
      branchRevision: 0,
    };

    await expect(loadLatestConversationMessagesShared(request)).rejects.toThrow('temporary failure');
    await expect(loadLatestConversationMessagesShared(request)).resolves.toBe(page);
    expect(loadLatestConversationMessagesMock).toHaveBeenCalledTimes(2);
  });

  it('expires and aborts a stalled in-flight request after the bounded dedupe window', async () => {
    let firstSignal: AbortSignal | undefined;
    loadLatestConversationMessagesMock
      .mockImplementationOnce((_conversationId, options) => {
        firstSignal = options.signal;
        return new Promise((_resolve, reject) => {
          firstSignal?.addEventListener('abort', () => reject(firstSignal.reason), {
            once: true,
          });
        });
      })
      .mockResolvedValueOnce(page);
    const request = {
      ownerId: 'owner-expiry',
      conversationId: 'conversation-expiry',
      branchRevision: 0,
    };

    const stalled = loadLatestConversationMessagesShared(request);
    vi.advanceTimersByTime(MESSAGE_WINDOW_DEDUPE_MS);
    await expect(stalled).rejects.toMatchObject({ name: 'AbortError' });
    await expect(loadLatestConversationMessagesShared(request)).resolves.toBe(page);

    expect(firstSignal?.aborted).toBe(true);
    expect(loadLatestConversationMessagesMock).toHaveBeenCalledTimes(2);
  });

  it('bounds retained windows and evicts the oldest request', async () => {
    const pending: Array<() => void> = [];
    loadLatestConversationMessagesMock.mockImplementation(
      () =>
        new Promise((resolve) => {
          pending.push(() => resolve(page));
        })
    );
    for (let index = 0; index < MAX_MESSAGE_WINDOW_REQUESTS + 1; index += 1) {
      void loadLatestConversationMessagesShared({
        ownerId: 'owner-capacity',
        conversationId: `conversation-${index}`,
        branchRevision: 0,
      });
      vi.advanceTimersByTime(1);
    }

    void loadLatestConversationMessagesShared({
      ownerId: 'owner-capacity',
      conversationId: 'conversation-0',
      branchRevision: 0,
    });

    expect(loadLatestConversationMessagesMock).toHaveBeenCalledTimes(MAX_MESSAGE_WINDOW_REQUESTS + 2);
    pending.forEach((resolve) => resolve());
    await vi.runAllTimersAsync();
  });

  it('aborts the superseded fetch instead of merely ignoring its response', async () => {
    let firstSignal: AbortSignal | undefined;
    loadLatestConversationMessagesMock
      .mockImplementationOnce((_conversationId, options) => {
        firstSignal = options.signal;
        return new Promise((_resolve, reject) => {
          firstSignal?.addEventListener('abort', () => reject(firstSignal.reason), {
            once: true,
          });
        });
      })
      .mockResolvedValueOnce(page);
    const request = {
      ownerId: 'owner-abort',
      conversationId: 'conversation-abort',
      branchRevision: 0,
    };

    const superseded = loadLatestConversationMessagesShared(request);
    const replacement = loadLatestConversationMessagesShared({ ...request, force: true });

    expect(firstSignal?.aborted).toBe(true);
    await expect(superseded).rejects.toMatchObject({
      name: 'AbortError',
      message: 'message_window_replaced',
    });
    await expect(replacement).resolves.toBe(page);
  });

  it('cancels only the matching owner and conversation request on route teardown', async () => {
    const signals: AbortSignal[] = [];
    loadLatestConversationMessagesMock.mockImplementation((_conversationId, options) => {
      signals.push(options.signal);
      return new Promise(() => {});
    });

    void loadLatestConversationMessagesShared({
      ownerId: 'owner-a',
      conversationId: 'conversation-a',
      branchRevision: 0,
    });
    void loadLatestConversationMessagesShared({
      ownerId: 'owner-b',
      conversationId: 'conversation-b',
      branchRevision: 0,
    });
    cancelConversationMessageRequests('owner-a', 'conversation-a');

    expect(signals[0]?.aborted).toBe(true);
    expect(signals[1]?.aborted).toBe(false);
  });

  it('does not discard a settled mount-grace response during route cleanup', async () => {
    const request = {
      ownerId: 'owner-remount',
      conversationId: 'conversation-remount',
      branchRevision: 0,
    };

    await loadLatestConversationMessagesShared(request);
    cancelConversationMessageRequests(request.ownerId, request.conversationId);
    await loadLatestConversationMessagesShared(request);

    expect(loadLatestConversationMessagesMock).toHaveBeenCalledTimes(1);
  });
});
