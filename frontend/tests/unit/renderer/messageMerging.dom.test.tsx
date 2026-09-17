/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React, { type PropsWithChildren } from 'react';
import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ipcBridge } from '@/common';
import { BackendHttpError } from '@/common/adapter/httpBridge';
import type { MessageCursorPage } from '@/common/adapter/ipcBridge';
import type { IMessageAcpToolCall, IMessageText, IMessageThinking, TMessage } from '@/common/chat/chatLib';
import {
  MessageListLoadErrorProvider,
  MessageListLoadingProvider,
  MessageListProvider,
  MessagePaginationProvider,
  useAddOrUpdateMessage,
  useMessageLstCache,
  useMessageList,
  useMessageListLoading,
  useMessageListLoadError,
  useMessagePaginationState,
  useLoadAnchorMessageWindow,
  useLoadPreviousMessagePage,
  useReplaceWithAnchorWindow,
} from '@/renderer/pages/conversation/Messages/hooks';
import { mergeLoadedPageWithCurrent } from '@/renderer/pages/conversation/Messages/messageWindowReconciliation';
import {
  getSelectedSynonBiomedBranch,
  selectSynonBiomedBranch,
} from '@/renderer/services/synonBiomedConversationBranches';
import { TRANSIENT_HISTORY_RETRY_DELAYS_MS } from '@/renderer/services/synonBiomedHistoryRetry';
import { INTERACTIVE_MESSAGE_PAGE_LIMIT } from '@/renderer/utils/chat/messagePagination';

const { requestConversationMessagesMock } = vi.hoisted(() => ({
  requestConversationMessagesMock: vi.fn(),
}));

vi.mock('@/common/adapter/ipcBridge', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/common/adapter/ipcBridge')>()),
  requestConversationMessages: requestConversationMessagesMock,
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    conversation: {
      userCreated: {
        on: vi.fn().mockReturnValue(() => {}),
      },
      historyRebased: {
        on: vi.fn().mockReturnValue(() => {}),
      },
    },
    realtime: {
      cursorReset: {
        on: vi.fn().mockReturnValue(() => {}),
      },
    },
    database: {
      getConversationMessages: {
        invoke: vi.fn(),
      },
    },
  },
}));

const CONVERSATION_ID = 'conversation-1';

function createTextMessage(msgId: string, content: string, conversationId = CONVERSATION_ID): IMessageText {
  return {
    id: `text-${msgId}-${content}`,
    type: 'text',
    msg_id: msgId,
    conversation_id: conversationId,
    position: 'left',
    content: {
      content,
    },
  };
}

function createThinkingMessage(msgId: string, content: string): IMessageThinking {
  return {
    id: `thinking-${msgId}-${content}`,
    type: 'thinking',
    msg_id: msgId,
    conversation_id: CONVERSATION_ID,
    position: 'left',
    content: {
      content,
      status: 'thinking',
    },
  };
}

function createThinkingDoneMessage(msgId: string, duration: number): IMessageThinking {
  return {
    id: `thinking-done-${msgId}`,
    type: 'thinking',
    msg_id: msgId,
    conversation_id: CONVERSATION_ID,
    position: 'left',
    content: {
      content: '',
      duration,
      status: 'done',
    },
  };
}

function createToolCallMessage(toolCallId: string): IMessageAcpToolCall {
  return {
    id: toolCallId,
    type: 'acp_tool_call',
    msg_id: toolCallId,
    conversation_id: CONVERSATION_ID,
    position: 'left',
    content: {
      session_id: 'session-1',
      update: {
        sessionUpdate: 'tool_call',
        tool_call_id: toolCallId,
        status: 'completed',
        title: 'Read file',
        kind: 'read',
      },
    },
  };
}

function TestWrapper({ children }: PropsWithChildren): JSX.Element {
  return <MessageListProvider value={[]}>{children}</MessageListProvider>;
}

function CacheWrapper({ children }: PropsWithChildren): JSX.Element {
  return (
    <MessageListLoadErrorProvider value={null}>
      <MessageListLoadingProvider value={false}>
        <MessagePaginationProvider
          value={{
            hasMoreBefore: false,
            hasMoreAfter: false,
            isLoadingBefore: false,
            isLoadingAnchor: false,
          }}
        >
          <MessageListProvider value={[]}>{children}</MessageListProvider>
        </MessagePaginationProvider>
      </MessageListLoadingProvider>
    </MessageListLoadErrorProvider>
  );
}

function createMessagePage(items: TMessage[]): MessageCursorPage<TMessage> {
  return {
    items,
    oldest_cursor: null,
    newest_cursor: null,
    has_more_before: false,
    has_more_after: false,
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function useMessageHarness() {
  return {
    addOrUpdateMessage: useAddOrUpdateMessage(),
    messages: useMessageList(),
  };
}

function useAnchorMessageHarness() {
  return {
    addOrUpdateMessage: useAddOrUpdateMessage(),
    replaceWithAnchorWindow: useReplaceWithAnchorWindow(),
    messages: useMessageList(),
  };
}

async function flushMessageQueue(): Promise<void> {
  await act(async () => {
    vi.runAllTimers();
  });
}

describe('message merging', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.mocked(ipcBridge.conversation.historyRebased.on).mockClear();
    requestConversationMessagesMock.mockImplementation((params) =>
      vi.mocked(ipcBridge.database.getConversationMessages.invoke)(params)
    );
  });

  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
  });

  it('never retracts a richer rendered answer during terminal durable reconciliation', () => {
    const live = createTextMessage('answer-terminal', '完整结论已经显示，且包含全部文件说明。');
    const durable = {
      ...live,
      content: { ...live.content, content: '完整结论已经显示' },
    };

    const reconciled = mergeLoadedPageWithCurrent(CONVERSATION_ID, [durable], [live], true);

    expect(reconciled).toBeDefined();
    expect(reconciled[0]).toBe(live);
    expect((reconciled[0] as IMessageText).content.content).toBe('完整结论已经显示，且包含全部文件说明。');
  });

  it('keeps a durable terminal answer when a stale live attempt reset is empty', () => {
    const durable = {
      ...createTextMessage('answer-terminal', '完整结论已经持久化，并绑定最终产物。'),
      status: 'finish' as const,
      terminal_status: 'completed' as const,
      artifact_refs: [{ artifact_id: 'artifact-1', version_id: 'version-1', relation: 'produced' as const }],
    };
    const staleLiveReset = {
      ...durable,
      id: 'live-empty-reset',
      status: 'work' as const,
      terminal_status: undefined,
      artifact_refs: undefined,
      content: {
        content: '',
        replace: true,
        replaceScope: 'attempt' as const,
        assistantAttemptId: 'attempt-1',
      },
    };

    const reconciled = mergeLoadedPageWithCurrent(CONVERSATION_ID, [durable], [staleLiveReset], true);

    expect(reconciled).toHaveLength(1);
    expect((reconciled[0] as IMessageText).content.content).toBe('完整结论已经持久化，并绑定最终产物。');
    expect((reconciled[0] as IMessageText).terminal_status).toBe('completed');
    expect((reconciled[0] as IMessageText).artifact_refs).toEqual(durable.artifact_refs);
  });

  it('keeps text segments split when tool calls interrupt the same msg_id stream', async () => {
    const { result } = renderHook(() => useMessageHarness(), {
      wrapper: TestWrapper,
    });

    act(() => {
      result.current.addOrUpdateMessage(createTextMessage('msg-1', 'hello'));
      result.current.addOrUpdateMessage(createTextMessage('msg-1', ' world'));
    });
    await flushMessageQueue();

    expect(result.current.messages).toHaveLength(1);
    expect(result.current.messages[0].type).toBe('text');
    expect((result.current.messages[0] as IMessageText).content.content).toBe('hello world');

    const repeatedSegment = createTextMessage('msg-1', 'again');
    // Production streaming can reuse the same durable message id after a tool
    // interruption. The presentation layer must still give each rendered row
    // a stable unique key.
    repeatedSegment.id = result.current.messages[0].id;

    act(() => {
      result.current.addOrUpdateMessage(createToolCallMessage('tool-1'));
      result.current.addOrUpdateMessage(repeatedSegment);
    });
    await flushMessageQueue();

    expect(result.current.messages.map((message) => message.type)).toEqual(['text', 'acp_tool_call', 'text']);
    expect(new Set(result.current.messages.map((message) => message.id)).size).toBe(result.current.messages.length);
    expect((result.current.messages[0] as IMessageText).content.content).toBe('hello world');
    expect((result.current.messages[2] as IMessageText).content.content).toBe('again');
  });

  it('replaces every exact segment of one assistant attempt without parsing message id prefixes', async () => {
    const { result } = renderHook(() => useMessageHarness(), {
      wrapper: TestWrapper,
    });
    const attemptId = 'assistant-conversation-1-7';
    const first = createTextMessage('unrelated-id-a', 'draft before tool');
    first.content.assistantAttemptId = attemptId;
    const second = createTextMessage('opaque-id-b', 'draft after tool');
    second.content.assistantAttemptId = attemptId;
    const other = createTextMessage('assistant-conversation-1-8', 'other attempt');
    other.content.assistantAttemptId = 'assistant-conversation-1-8';

    act(() => {
      result.current.addOrUpdateMessage(first);
      result.current.addOrUpdateMessage(createToolCallMessage('tool-between'));
      result.current.addOrUpdateMessage(second);
      result.current.addOrUpdateMessage(other);
    });
    await flushMessageQueue();

    const replacement = createTextMessage('new-opaque-segment', 'corrected');
    replacement.content = {
      content: 'corrected',
      replace: true,
      replaceScope: 'attempt',
      assistantAttemptId: attemptId,
    };
    act(() => result.current.addOrUpdateMessage(replacement));
    await flushMessageQueue();

    expect(
      result.current.messages
        .filter((message): message is IMessageText => message.type === 'text')
        .map((message) => [message.msg_id, message.content.content])
    ).toEqual([
      ['assistant-conversation-1-8', 'other attempt'],
      ['new-opaque-segment', 'corrected'],
    ]);
  });

  it('keeps thinking segments split when tool calls interrupt the same msg_id stream', async () => {
    const { result } = renderHook(() => useMessageHarness(), {
      wrapper: TestWrapper,
    });

    act(() => {
      result.current.addOrUpdateMessage(createThinkingMessage('msg-1', 'alpha'));
      result.current.addOrUpdateMessage(createThinkingMessage('msg-1', 'beta'));
    });
    await flushMessageQueue();

    expect(result.current.messages).toHaveLength(1);
    expect(result.current.messages[0].type).toBe('thinking');
    expect((result.current.messages[0] as IMessageThinking).content.content).toBe('alphabeta');

    act(() => {
      result.current.addOrUpdateMessage(createToolCallMessage('tool-1'));
      result.current.addOrUpdateMessage(createThinkingMessage('msg-1', 'gamma'));
    });
    await flushMessageQueue();

    expect(result.current.messages.map((message) => message.type)).toEqual(['thinking', 'acp_tool_call', 'thinking']);
    expect((result.current.messages[0] as IMessageThinking).content.content).toBe('alphabeta');
    expect((result.current.messages[2] as IMessageThinking).content.content).toBe('gamma');
  });

  it('replaces a thinking segment when the durable stream publishes a reset', async () => {
    const { result } = renderHook(() => useMessageHarness(), { wrapper: TestWrapper });
    act(() => {
      result.current.addOrUpdateMessage(createThinkingMessage('msg-reset', 'candidate reasoning'));
      result.current.addOrUpdateMessage({
        ...createThinkingMessage('msg-reset', 'corrected reasoning'),
        content: { content: 'corrected reasoning', status: 'thinking', replace: true },
      });
    });
    await flushMessageQueue();
    expect(result.current.messages).toHaveLength(1);
    expect((result.current.messages[0] as IMessageThinking).content.content).toBe('corrected reasoning');
  });

  it('preserves a final text report when a terminal tip reuses its msg_id', async () => {
    const { result } = renderHook(() => useMessageHarness(), {
      wrapper: TestWrapper,
    });

    act(() => {
      result.current.addOrUpdateMessage(createTextMessage('turn-1', 'complete scientific report'));
      result.current.addOrUpdateMessage({
        id: 'tip-turn-1',
        type: 'tips',
        msg_id: 'turn-1',
        conversation_id: CONVERSATION_ID,
        position: 'left',
        content: {
          type: 'error',
          content: 'provider response too large',
        },
      } as TMessage);
    });
    await flushMessageQueue();

    expect(result.current.messages.map((message) => message.type)).toEqual(['text', 'tips']);
    expect((result.current.messages[0] as IMessageText).content.content).toBe('complete scientific report');
  });

  it('merges thinking done updates into the existing thinking message instead of appending a completion message', async () => {
    const { result } = renderHook(() => useMessageHarness(), {
      wrapper: TestWrapper,
    });

    act(() => {
      result.current.addOrUpdateMessage(createThinkingMessage('msg-1', 'alpha'));
      result.current.addOrUpdateMessage(createToolCallMessage('tool-1'));
      result.current.addOrUpdateMessage(createThinkingDoneMessage('msg-1', 4200));
    });
    await flushMessageQueue();

    expect(result.current.messages.map((message) => message.type)).toEqual(['thinking', 'acp_tool_call']);
    expect((result.current.messages[0] as IMessageThinking).content.status).toBe('done');
    expect((result.current.messages[0] as IMessageThinking).content.duration).toBe(4200);
  });

  it('ignores non-renderable transformed stream messages', async () => {
    const { result } = renderHook(() => useMessageHarness(), {
      wrapper: TestWrapper,
    });

    act(() => {
      result.current.addOrUpdateMessage(undefined);
    });
    await flushMessageQueue();

    expect(result.current.messages).toEqual([]);
  });

  it('applies terminal artifact references in the same queue as the final text chunk', async () => {
    const { result } = renderHook(() => useMessageHarness(), {
      wrapper: TestWrapper,
    });
    act(() => {
      result.current.addOrUpdateMessage(createTextMessage('answer-1', 'final answer'));
      result.current.addOrUpdateMessage(undefined, false, {
        msgId: 'answer-1',
        references: [
          {
            artifact_id: 'artifact-1',
            version_id: 'version-1',
            relation: 'produced',
            availability: 'available',
          },
        ],
        mode: 'replace',
      });
    });
    await flushMessageQueue();
    expect(result.current.messages).toHaveLength(1);
    expect(result.current.messages[0].artifact_refs).toEqual([
      {
        artifact_id: 'artifact-1',
        version_id: 'version-1',
        relation: 'produced',
        availability: 'available',
      },
    ]);
    act(() => {
      result.current.addOrUpdateMessage(undefined, false, {
        msgId: 'answer-1',
        references: [],
        mode: 'replace',
      });
    });
    await flushMessageQueue();
    expect(result.current.messages[0].artifact_refs).toEqual([]);
  });

  it('keeps live-only and richer streaming messages when replacing with an anchor window', async () => {
    const { result } = renderHook(() => useAnchorMessageHarness(), {
      wrapper: TestWrapper,
    });

    act(() => {
      result.current.addOrUpdateMessage(createTextMessage('agent-1', 'partial streaming response'));
      result.current.addOrUpdateMessage(createTextMessage('agent-2', 'live tail'));
    });
    await flushMessageQueue();

    act(() => {
      result.current.replaceWithAnchorWindow(CONVERSATION_ID, [
        createTextMessage('user-anchor', 'anchor'),
        createTextMessage('agent-1', 'partial'),
      ]);
    });

    expect(result.current.messages.map((message) => message.msg_id)).toEqual(['user-anchor', 'agent-1', 'agent-2']);
    expect((result.current.messages[1] as IMessageText).content.content).toBe('partial streaming response');
    expect((result.current.messages[2] as IMessageText).content.content).toBe('live tail');
  });

  it('does not reuse an attempt-level live text for a later durable segment', async () => {
    const { result } = renderHook(() => useAnchorMessageHarness(), {
      wrapper: TestWrapper,
    });
    const attemptId = 'assistant-conversation-1-1';
    const live = createTextMessage(attemptId, 'complete live preview');
    live.content.assistantAttemptId = attemptId;

    act(() => result.current.addOrUpdateMessage(live));
    await flushMessageQueue();

    const persistedFirst = createTextMessage(attemptId, 'initial assistant text');
    persistedFirst.content.assistantAttemptId = attemptId;
    const persistedSecond = createTextMessage(`${attemptId}-segment-2`, 'durable final segment');
    persistedSecond.content.assistantAttemptId = attemptId;

    act(() => result.current.replaceWithAnchorWindow(CONVERSATION_ID, [persistedFirst, persistedSecond]));

    expect(result.current.messages.map((message) => message.id)).toEqual([persistedFirst.id, persistedSecond.id]);
    expect(result.current.messages).not.toContainEqual(expect.objectContaining({ id: live.id }));
  });

  it('reconciles a durable prefix with its richer live preview into one row during an incremental refresh', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    invoke.mockReset();
    const attemptId = 'assistant-running-attempt';
    const live = createTextMessage(attemptId, 'complete live preview');
    live.content.assistantAttemptId = attemptId;
    const persisted = createTextMessage('durable-segment-1', 'complete live');
    persisted.msg_id = attemptId;
    persisted.content.assistantAttemptId = attemptId;
    invoke.mockResolvedValueOnce(createMessagePage([live])).mockResolvedValueOnce(createMessagePage([persisted]));

    const { result } = renderHook(
      () => ({
        refresh: useMessageLstCache(CONVERSATION_ID),
        messages: useMessageList(),
      }),
      { wrapper: CacheWrapper }
    );
    await act(async () => Promise.resolve());
    await act(async () => result.current.refresh(false));

    expect(result.current.messages).toHaveLength(1);
    expect(result.current.messages[0]).toEqual(
      expect.objectContaining({
        content: expect.objectContaining({ content: 'complete live preview' }),
      })
    );
  });

  it('requests compact tool content when hydrating historical messages', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    invoke.mockClear();
    invoke.mockResolvedValue({
      items: [],
      oldest_cursor: null,
      newest_cursor: null,
      has_more_before: false,
      has_more_after: false,
    });

    renderHook(() => useMessageLstCache(CONVERSATION_ID), {
      wrapper: CacheWrapper,
    });

    await act(async () => {
      await Promise.resolve();
    });

    expect(invoke).toHaveBeenCalledWith({
      conversation_id: CONVERSATION_ID,
      limit: INTERACTIVE_MESSAGE_PAGE_LIMIT,
      content_mode: 'compact',
    });
  });
  it('returns a stable refresh function for persisted Synon Biomed message polling', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    invoke.mockClear();
    invoke.mockResolvedValue({
      items: [],
      oldest_cursor: null,
      newest_cursor: null,
      has_more_before: false,
      has_more_after: false,
    });

    const { result } = renderHook(() => useMessageLstCache(CONVERSATION_ID), {
      wrapper: CacheWrapper,
    });

    await act(async () => {
      await Promise.resolve();
    });
    invoke.mockClear();

    expect(typeof result.current).toBe('function');
    await act(async () => {
      await result.current();
    });

    expect(invoke).toHaveBeenCalledWith({
      conversation_id: CONVERSATION_ID,
      limit: INTERACTIVE_MESSAGE_PAGE_LIMIT,
      content_mode: 'compact',
    });
  });

  it('replaces a superseded attempt window when canonical history advances', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    invoke.mockReset();
    invoke
      .mockResolvedValueOnce({
        ...createMessagePage([createTextMessage('attempt-4', 'superseded attempt')]),
        through_publication_sequence: 4000,
      })
      .mockResolvedValueOnce({
        ...createMessagePage([createTextMessage('attempt-5', 'current canonical attempt')]),
        through_publication_sequence: 4018,
      });
    const { result } = renderHook(
      () => ({
        messages: useMessageList(),
        refresh: useMessageLstCache(CONVERSATION_ID),
      }),
      { wrapper: CacheWrapper }
    );
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(result.current.messages.map((message) => message.msg_id)).toEqual(['attempt-4']);

    await act(async () => {
      await result.current.refresh(true);
    });
    expect(result.current.messages.map((message) => message.msg_id)).toEqual(['attempt-5']);
  });

  it('restores a bounded message window after a long idle while revalidating a revisited conversation', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    const conversationId = 'conversation-cache-revisit';
    invoke
      .mockReset()
      .mockResolvedValueOnce(createMessagePage([createTextMessage('cached', 'cached transcript', conversationId)]));

    const first = renderHook(
      () => ({
        messages: useMessageList(),
        refresh: useMessageLstCache(conversationId),
      }),
      {
        wrapper: CacheWrapper,
      }
    );
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(first.result.current.messages.map((message) => message.msg_id)).toEqual(['cached']);
    first.unmount();
    vi.advanceTimersByTime(12 * 60 * 60 * 1000);

    const refresh = deferred<MessageCursorPage<TMessage>>();
    invoke.mockReturnValueOnce(refresh.promise);
    const second = renderHook(
      () => ({
        messages: useMessageList(),
        loading: useMessageListLoading(),
        refresh: useMessageLstCache(conversationId),
      }),
      {
        wrapper: CacheWrapper,
      }
    );

    expect(second.result.current.messages.map((message) => message.msg_id)).toEqual(['cached']);
    expect(second.result.current.loading).toBe(false);
    expect(invoke).toHaveBeenCalledTimes(2);

    await act(async () => {
      refresh.resolve(createMessagePage([createTextMessage('fresh', 'fresh transcript', conversationId)]));
      await refresh.promise;
    });
    expect(second.result.current.messages.map((message) => message.msg_id)).toEqual(['fresh']);
    expect(second.result.current.loading).toBe(false);
    second.unmount();
  });

  it('never restores another authenticated owner message window', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    const conversationId = 'conversation-owner-isolation';
    invoke
      .mockReset()
      .mockResolvedValueOnce(
        createMessagePage([createTextMessage('owner-a-message', 'owner A transcript', conversationId)])
      );
    const first = renderHook(
      () => ({
        messages: useMessageList(),
        refresh: useMessageLstCache(conversationId, 'owner-a'),
      }),
      { wrapper: CacheWrapper }
    );
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(first.result.current.messages.map((message) => message.msg_id)).toEqual(['owner-a-message']);
    first.unmount();

    const ownerB = deferred<MessageCursorPage<TMessage>>();
    invoke.mockReturnValueOnce(ownerB.promise);
    const second = renderHook(
      () => ({
        messages: useMessageList(),
        refresh: useMessageLstCache(conversationId, 'owner-b'),
      }),
      { wrapper: CacheWrapper }
    );
    expect(second.result.current.messages).toEqual([]);

    await act(async () => {
      ownerB.resolve(createMessagePage([createTextMessage('owner-b-message', 'owner B transcript', conversationId)]));
      await ownerB.promise;
    });
    expect(second.result.current.messages.map((message) => message.msg_id)).toEqual(['owner-b-message']);
    second.unmount();
  });

  it('retains at most eight root-frame message windows', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    invoke
      .mockReset()
      .mockImplementation(({ conversation_id }) =>
        Promise.resolve(
          createMessagePage([createTextMessage(`message-${conversation_id}`, 'cached transcript', conversation_id)])
        )
      );

    for (let index = 0; index < 9; index += 1) {
      const conversationId = `conversation-cache-capacity-${index}`;
      const view = renderHook(
        () => ({
          messages: useMessageList(),
          refresh: useMessageLstCache(conversationId, 'owner-capacity'),
        }),
        { wrapper: CacheWrapper }
      );
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(view.result.current.messages).toHaveLength(1);
      view.unmount();
    }

    const revalidation = deferred<MessageCursorPage<TMessage>>();
    invoke.mockReturnValueOnce(revalidation.promise);
    const revisited = renderHook(
      () => ({
        messages: useMessageList(),
        refresh: useMessageLstCache('conversation-cache-capacity-0', 'owner-capacity'),
      }),
      { wrapper: CacheWrapper }
    );

    expect(revisited.result.current.messages).toEqual([]);
    revisited.unmount();
  });

  it('preserves direct stream messages that arrive before initial history resolves', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    const initialHistory = deferred<MessageCursorPage<TMessage>>();
    invoke.mockReset().mockReturnValueOnce(initialHistory.promise);
    const conversationId = 'conversation-live-before-history';
    const { result } = renderHook(
      () => ({
        messages: useMessageList(),
        add: useAddOrUpdateMessage(),
        refresh: useMessageLstCache(conversationId),
      }),
      { wrapper: CacheWrapper }
    );

    act(() => {
      result.current.add(createTextMessage('live-first-token', 'first visible token', conversationId));
    });
    await flushMessageQueue();
    expect(result.current.messages.map((message) => message.msg_id)).toEqual(['live-first-token']);

    await act(async () => {
      initialHistory.resolve(createMessagePage([]));
      await initialHistory.promise;
    });
    expect(result.current.messages.map((message) => message.msg_id)).toEqual(['live-first-token']);
  });

  it('keeps reconciling a temporarily unavailable history window without requiring a reload', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    const initialHistory = deferred<MessageCursorPage<TMessage>>();
    invoke.mockReset().mockReturnValueOnce(initialHistory.promise);
    const conversationId = 'conversation-history-preparing';
    const { result } = renderHook(
      () => ({
        messages: useMessageList(),
        loading: useMessageListLoading(),
        loadError: useMessageListLoadError(),
        add: useAddOrUpdateMessage(),
        refresh: useMessageLstCache(conversationId, 'owner-history-preparing'),
      }),
      { wrapper: CacheWrapper }
    );

    expect(result.current.loading).toBe(true);
    act(() => {
      result.current.add(createTextMessage('live-progress', 'visible live progress', conversationId));
    });
    await flushMessageQueue();

    const historyRetry = deferred<MessageCursorPage<TMessage>>();
    invoke.mockReturnValueOnce(historyRetry.promise);
    await act(async () => {
      initialHistory.reject(
        new BackendHttpError({
          method: 'GET',
          path: `/api/conversations/${conversationId}/messages`,
          status: 503,
          body: { code: 'HISTORY_NOT_READY', message: 'transcript history is still preparing' },
        })
      );
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(invoke).toHaveBeenCalledTimes(1);
    expect(result.current.loading).toBe(false);
    expect(result.current.loadError).toBeNull();
    expect(result.current.messages.map((message) => message.msg_id)).toEqual(['live-progress']);

    await act(async () => {
      vi.advanceTimersByTime(TRANSIENT_HISTORY_RETRY_DELAYS_MS[0]);
      await Promise.resolve();
    });
    expect(invoke).toHaveBeenCalledTimes(2);
    expect(result.current.loading).toBe(false);

    await act(async () => {
      historyRetry.resolve(createMessagePage([createTextMessage('durable', 'durable history', conversationId)]));
      await historyRetry.promise;
    });
    expect(result.current.messages.map((message) => message.msg_id)).toEqual(['durable', 'live-progress']);
  });

  it('ignores a late response from the previous conversation after navigation', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    const first = deferred<MessageCursorPage<TMessage>>();
    const second = deferred<MessageCursorPage<TMessage>>();
    invoke.mockImplementation(({ conversation_id }) =>
      conversation_id === 'conversation-a' ? first.promise : second.promise
    );

    const { result, rerender } = renderHook(
      ({ conversationId }) => {
        const refresh = useMessageLstCache(conversationId);
        return {
          refresh,
          messages: useMessageList(),
          loadError: useMessageListLoadError(),
          pagination: useMessagePaginationState(),
        };
      },
      {
        wrapper: CacheWrapper,
        initialProps: { conversationId: 'conversation-a' },
      }
    );

    rerender({ conversationId: 'conversation-b' });
    expect(result.current.pagination.conversationId).toBe('conversation-b');
    expect(result.current.pagination.historyRevision).toBeUndefined();
    await act(async () => {
      second.resolve({
        ...createMessagePage([createTextMessage('b-1', 'conversation B', 'conversation-b')]),
        through_publication_sequence: 3,
      });
      await second.promise;
    });
    expect(result.current.messages.map((message) => message.conversation_id)).toEqual(['conversation-b']);
    expect(result.current.pagination).toMatchObject({
      conversationId: 'conversation-b',
      historyRevision: 3,
    });

    await act(async () => {
      first.resolve({
        ...createMessagePage([createTextMessage('a-1', 'late conversation A', 'conversation-a')]),
        through_publication_sequence: 99,
      });
      await first.promise;
    });
    expect(result.current.messages.map((message) => message.conversation_id)).toEqual(['conversation-b']);
    expect(result.current.loadError).toBeNull();
    expect(result.current.pagination).toMatchObject({
      conversationId: 'conversation-b',
      historyRevision: 3,
    });
  });

  it('does not let a late previous-page response pollute a newly selected branch', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    const previous = deferred<MessageCursorPage<TMessage>>();
    invoke
      .mockResolvedValueOnce({
        items: [createTextMessage('a-tail', 'branch A tail')],
        oldest_cursor: 'cursor-a',
        newest_cursor: 'cursor-a',
        has_more_before: true,
        has_more_after: false,
        branch_id: 'br_00000001',
        branch_generation: 2,
      })
      .mockReturnValueOnce(previous.promise)
      .mockResolvedValueOnce({
        items: [createTextMessage('b-tail', 'branch B tail')],
        oldest_cursor: 'cursor-b',
        newest_cursor: 'cursor-b',
        has_more_before: false,
        has_more_after: false,
        branch_id: 'br_00000002',
        branch_generation: 3,
      });

    const { result, unmount } = renderHook(
      () => ({
        refresh: useMessageLstCache(CONVERSATION_ID),
        loadPrevious: useLoadPreviousMessagePage(CONVERSATION_ID),
        messages: useMessageList(),
        pagination: useMessagePaginationState(),
      }),
      { wrapper: CacheWrapper }
    );
    await act(async () => {
      await Promise.resolve();
    });

    let previousRequest!: Promise<boolean>;
    await act(async () => {
      previousRequest = result.current.loadPrevious();
      await Promise.resolve();
    });
    await act(async () => {
      selectSynonBiomedBranch(CONVERSATION_ID, 'br_00000002');
      await Promise.resolve();
    });
    expect(result.current.messages.map((message) => message.msg_id)).toEqual(['b-tail']);

    await act(async () => {
      previous.resolve({
        items: [createTextMessage('a-old', 'late branch A history')],
        oldest_cursor: 'cursor-a-old',
        newest_cursor: 'cursor-a-old',
        has_more_before: false,
        has_more_after: true,
        branch_id: 'br_00000001',
        branch_generation: 2,
      });
      await previousRequest;
    });
    expect(result.current.messages.map((message) => message.msg_id)).toEqual(['b-tail']);
    expect(result.current.pagination).toMatchObject({
      branchId: 'br_00000002',
      branchGeneration: 3,
      isLoadingBefore: false,
    });
    unmount();
    selectSynonBiomedBranch(CONVERSATION_ID, null);
  });

  it('records the previous window boundary when an older cursor page is prepended', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    const tail = createTextMessage('tail', 'latest window');
    invoke
      .mockReset()
      .mockResolvedValueOnce({
        items: [tail],
        oldest_cursor: 'cursor-tail',
        newest_cursor: 'cursor-tail',
        has_more_before: true,
        has_more_after: false,
      })
      .mockResolvedValueOnce({
        items: [createTextMessage('older', 'older window')],
        oldest_cursor: 'cursor-older',
        newest_cursor: 'cursor-older',
        has_more_before: false,
        has_more_after: true,
      });

    const { result, unmount } = renderHook(
      () => ({
        refresh: useMessageLstCache(CONVERSATION_ID),
        loadPrevious: useLoadPreviousMessagePage(CONVERSATION_ID),
        messages: useMessageList(),
        pagination: useMessagePaginationState(),
      }),
      { wrapper: CacheWrapper }
    );
    await act(async () => Promise.resolve());

    await act(async () => {
      expect(await result.current.loadPrevious()).toBe(true);
    });

    expect(result.current.messages.map((message) => message.msg_id)).toEqual(['older', 'tail']);
    expect(result.current.pagination.groupBoundaryMessageIds).toEqual([tail.id]);
    unmount();
  });

  it('keeps the displayed transcript and rolls back selection when a branch load fails', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    invoke.mockReset();
    invoke
      .mockResolvedValueOnce({
        ...createMessagePage([createTextMessage('base', 'stable branch')]),
        branch_id: 'br_00000001',
        branch_generation: 2,
      })
      .mockRejectedValueOnce(new Error('branch unavailable'));

    const { result, unmount } = renderHook(
      () => ({
        refresh: useMessageLstCache(CONVERSATION_ID),
        messages: useMessageList(),
        loadError: useMessageListLoadError(),
        pagination: useMessagePaginationState(),
      }),
      { wrapper: CacheWrapper }
    );
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(result.current.messages.map((item) => item.msg_id)).toEqual(['base']);

    await act(async () => {
      selectSynonBiomedBranch(CONVERSATION_ID, 'br_00000002');
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(result.current.loadError).toEqual({ source: 'branch' });
    expect(result.current.messages.map((item) => item.msg_id)).toEqual(['base']);
    expect(result.current.pagination.branchId).toBe('br_00000001');
    expect(getSelectedSynonBiomedBranch(CONVERSATION_ID)).toBe('br_00000001');
    unmount();
    selectSynonBiomedBranch(CONVERSATION_ID, null);
  });

  it('replaces history once per activation and rejects an older in-flight refresh', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    invoke.mockReset();
    const staleRefresh = deferred<MessageCursorPage<TMessage>>();
    const canonicalReload = deferred<MessageCursorPage<TMessage>>();
    invoke
      .mockResolvedValueOnce({
        ...createMessagePage([createTextMessage('legacy', 'legacy history')]),
        branch_id: 'br_00000001',
        branch_generation: 1,
      })
      .mockReturnValueOnce(staleRefresh.promise)
      .mockReturnValueOnce(canonicalReload.promise);
    const canonicalPage = {
      ...createMessagePage([createTextMessage('canonical', 'canonical history')]),
      branch_id: 'br_00000002',
      branch_generation: 2,
    };
    const { result, unmount } = renderHook(
      () => ({
        refresh: useMessageLstCache(CONVERSATION_ID),
        messages: useMessageList(),
      }),
      { wrapper: CacheWrapper }
    );
    await act(async () => {
      await Promise.resolve();
    });
    expect(result.current.messages.map((message) => message.msg_id)).toEqual(['legacy']);

    let stalePromise!: Promise<TMessage[]>;
    await act(async () => {
      stalePromise = result.current.refresh();
      await Promise.resolve();
    });
    const listener = vi.mocked(ipcBridge.conversation.historyRebased.on).mock.calls.at(-1)?.[0];
    expect(listener).toBeTypeOf('function');
    await act(async () => {
      listener?.({
        conversation_id: CONVERSATION_ID,
        project_id: 'project-a',
        root_frame_id: CONVERSATION_ID,
        activation_id: 'a'.repeat(64),
        authority_generation: 2,
        target_epoch: 2,
        active_branch_id: 'br_00000002',
        branch_generation: 2,
        realtime_high_water: 7,
      });
      await Promise.resolve();
    });
    const liveListener = vi.mocked(ipcBridge.conversation.userCreated.on).mock.calls.at(-1)?.[0];
    await act(async () => {
      liveListener?.({
        conversation_id: CONVERSATION_ID,
        msg_id: 'live',
        content: 'arrived during rebase',
        position: 'right',
        status: 'finish',
        hidden: false,
        created_at: 3,
      });
      canonicalReload.resolve(canonicalPage);
      await canonicalReload.promise;
    });
    expect(result.current.messages.map((message) => message.msg_id)).toEqual(['canonical', 'live']);
    expect(invoke).toHaveBeenCalledTimes(3);

    await act(async () => {
      staleRefresh.resolve(createMessagePage([createTextMessage('stale', 'late legacy history')]));
      await stalePromise;
    });
    expect(result.current.messages.map((message) => message.msg_id)).toEqual(['canonical', 'live']);

    unmount();
    act(() => selectSynonBiomedBranch(CONVERSATION_ID, null));
  });

  it('reloads canonical history after the server rejects a future realtime cursor', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    invoke.mockReset();
    invoke
      .mockResolvedValueOnce(createMessagePage([createTextMessage('before-reset', 'stale window')]))
      .mockResolvedValueOnce(createMessagePage([createTextMessage('after-reset', 'canonical window')]));
    const { result, unmount } = renderHook(
      () => ({
        refresh: useMessageLstCache(CONVERSATION_ID),
        messages: useMessageList(),
      }),
      { wrapper: CacheWrapper }
    );
    await act(async () => Promise.resolve());
    expect(result.current.messages.map((message) => message.msg_id)).toEqual(['before-reset']);

    const listener = vi.mocked(ipcBridge.realtime.cursorReset.on).mock.calls.at(-1)?.[0];
    expect(listener).toBeTypeOf('function');
    await act(async () => {
      listener?.({ cursor: 0 });
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(result.current.messages.map((message) => message.msg_id)).toEqual(['after-reset']);
    expect(invoke).toHaveBeenCalledTimes(2);
    unmount();
  });

  it('releases every pagination lane when a newer anchor request fails before an older page returns', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    const previous = deferred<MessageCursorPage<TMessage>>();
    const anchor = deferred<MessageCursorPage<TMessage>>();
    invoke
      .mockResolvedValueOnce({
        items: [createTextMessage('tail', 'latest history')],
        oldest_cursor: 'cursor-latest',
        newest_cursor: 'cursor-latest',
        has_more_before: true,
        has_more_after: false,
        branch_id: 'br_00000001',
        branch_generation: 2,
      })
      .mockReturnValueOnce(previous.promise)
      .mockReturnValueOnce(anchor.promise);

    const { result, unmount } = renderHook(
      () => ({
        refresh: useMessageLstCache(CONVERSATION_ID),
        loadPrevious: useLoadPreviousMessagePage(CONVERSATION_ID),
        loadAnchor: useLoadAnchorMessageWindow(CONVERSATION_ID),
        pagination: useMessagePaginationState(),
      }),
      { wrapper: CacheWrapper }
    );
    await act(async () => {
      await Promise.resolve();
    });

    let previousRequest!: Promise<boolean>;
    await act(async () => {
      previousRequest = result.current.loadPrevious();
      await Promise.resolve();
    });
    const previousSignal = requestConversationMessagesMock.mock.calls.at(-1)?.[1] as AbortSignal | undefined;
    expect(previousSignal).toBeInstanceOf(AbortSignal);
    expect(previousSignal?.aborted).toBe(false);
    expect(result.current.pagination).toMatchObject({
      isLoadingBefore: true,
      isLoadingAnchor: false,
    });

    let anchorRequest!: Promise<boolean>;
    await act(async () => {
      anchorRequest = result.current.loadAnchor('message-anchor');
      await Promise.resolve();
    });
    const anchorSignal = requestConversationMessagesMock.mock.calls.at(-1)?.[1] as AbortSignal | undefined;
    expect(previousSignal?.aborted).toBe(true);
    expect(anchorSignal).toBeInstanceOf(AbortSignal);
    expect(anchorSignal?.aborted).toBe(false);
    expect(result.current.pagination).toMatchObject({
      isLoadingBefore: false,
      isLoadingAnchor: true,
    });

    await act(async () => {
      anchor.reject(new Error('anchor unavailable'));
      expect(await anchorRequest).toBe(false);
    });
    expect(result.current.pagination).toMatchObject({
      isLoadingBefore: false,
      isLoadingAnchor: false,
    });

    await act(async () => {
      previous.resolve({
        items: [createTextMessage('old', 'stale older history')],
        oldest_cursor: 'cursor-old',
        newest_cursor: 'cursor-old',
        has_more_before: false,
        has_more_after: true,
        branch_id: 'br_00000001',
        branch_generation: 2,
      });
      expect(await previousRequest).toBe(false);
    });
    expect(result.current.pagination).toMatchObject({
      isLoadingBefore: false,
      isLoadingAnchor: false,
    });
    unmount();
  });

  it('aborts an in-flight anchor page when the message window unmounts', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    const anchor = deferred<MessageCursorPage<TMessage>>();
    invoke.mockReset().mockResolvedValueOnce(createMessagePage([])).mockReturnValueOnce(anchor.promise);

    const { result, unmount } = renderHook(
      () => ({
        refresh: useMessageLstCache(CONVERSATION_ID),
        loadAnchor: useLoadAnchorMessageWindow(CONVERSATION_ID),
      }),
      { wrapper: CacheWrapper }
    );
    await act(async () => {
      await Promise.resolve();
    });

    act(() => {
      void result.current.loadAnchor('message-anchor');
    });
    await act(async () => Promise.resolve());
    const anchorSignal = requestConversationMessagesMock.mock.calls.at(-1)?.[1] as AbortSignal | undefined;
    expect(anchorSignal?.aborted).toBe(false);

    unmount();
    expect(anchorSignal?.aborted).toBe(true);
  });

  it('discards a stale branch cursor and reloads one fresh canonical snapshot', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessages.invoke);
    invoke.mockReset();
    invoke
      .mockResolvedValueOnce({
        items: [createTextMessage('old-tail', 'old snapshot')],
        oldest_cursor: 'stale-cursor',
        newest_cursor: 'stale-cursor',
        has_more_before: true,
        has_more_after: false,
        branch_id: 'br_00000005',
        branch_generation: 4,
      })
      .mockRejectedValueOnce(
        new BackendHttpError({
          method: 'GET',
          path: '/api/conversations/conversation-1/messages',
          status: 409,
          body: {},
        })
      )
      .mockResolvedValueOnce({
        items: [createTextMessage('fresh-tail', 'fresh snapshot')],
        oldest_cursor: 'fresh-cursor',
        newest_cursor: 'fresh-cursor',
        has_more_before: false,
        has_more_after: false,
        branch_id: 'br_00000005',
        branch_generation: 5,
      });

    const { result, unmount } = renderHook(
      () => ({
        refresh: useMessageLstCache(CONVERSATION_ID),
        loadPrevious: useLoadPreviousMessagePage(CONVERSATION_ID),
        messages: useMessageList(),
        pagination: useMessagePaginationState(),
      }),
      { wrapper: CacheWrapper }
    );
    await act(async () => {
      await Promise.resolve();
    });
    await act(async () => {
      expect(await result.current.loadPrevious()).toBe(false);
      await Promise.resolve();
    });

    expect(result.current.messages.map((message) => message.msg_id)).toEqual(['fresh-tail']);
    expect(result.current.pagination).toMatchObject({
      oldestCursor: 'fresh-cursor',
      branchId: 'br_00000005',
      branchGeneration: 5,
      isLoadingBefore: false,
    });
    expect(invoke).toHaveBeenCalledTimes(3);
    unmount();
    selectSynonBiomedBranch(CONVERSATION_ID, null);
  });
});
