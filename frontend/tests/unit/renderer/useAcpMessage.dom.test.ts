/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { act, renderHook, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useAcpMessage } from '@/renderer/pages/conversation/platforms/acp/useAcpMessage';
import { getConversationOrNull } from '@/renderer/pages/conversation/utils/conversationCache';
import { resetEnsureConversationRuntimeStateForTests } from '@/renderer/pages/conversation/utils/ensureConversationRuntime';
import type { IResponseMessage } from '@/common/adapter/ipcBridge';

const {
  addOrUpdateMessageMock,
  updateMessageListMock,
  ensureRuntimeInvokeMock,
  getSlashCommandsInvokeMock,
  responseStreamOnMock,
  responseStreamHandlerRef,
  toolStdoutOnMock,
  toolStdoutHandlerRef,
} = vi.hoisted(() => ({
  addOrUpdateMessageMock: vi.fn(),
  updateMessageListMock: vi.fn(),
  ensureRuntimeInvokeMock: vi.fn(),
  getSlashCommandsInvokeMock: vi.fn(),
  responseStreamOnMock: vi.fn(),
  responseStreamHandlerRef: {
    current: undefined as ((message: IResponseMessage) => void) | undefined,
  },
  toolStdoutOnMock: vi.fn(),
  toolStdoutHandlerRef: {
    current: undefined as ((event: Record<string, unknown>) => void) | undefined,
  },
}));

vi.mock('@/renderer/pages/conversation/Messages/hooks', () => ({
  useAddOrUpdateMessage: () => addOrUpdateMessageMock,
  useMergeLiveMessage: () => addOrUpdateMessageMock,
  useUpdateMessageList: () => updateMessageListMock,
}));

vi.mock('@/renderer/pages/conversation/utils/conversationCache', () => ({
  getConversationOrNull: vi.fn(),
}));

vi.mock('react-i18next', () => {
  const t = (key: string) => key;
  return { useTranslation: () => ({ t }) };
});

vi.mock('@/common', () => ({
  ipcBridge: {
    acpConversation: {
      responseStream: {
        on: responseStreamOnMock.mockImplementation((handler: (message: IResponseMessage) => void) => {
          responseStreamHandlerRef.current = handler;
          return vi.fn();
        }),
      },
    },
    conversation: {
      ensureRuntime: {
        invoke: ensureRuntimeInvokeMock,
      },
      getSlashCommands: {
        invoke: getSlashCommandsInvokeMock,
      },
    },
    realtime: {
      toolStdoutChunk: {
        on: toolStdoutOnMock.mockImplementation((handler: (event: Record<string, unknown>) => void) => {
          toolStdoutHandlerRef.current = handler;
          return vi.fn();
        }),
      },
      reconnected: {
        on: vi.fn(() => vi.fn()),
      },
    },
  },
}));

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe('useAcpMessage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetEnsureConversationRuntimeStateForTests();
    ensureRuntimeInvokeMock.mockResolvedValue({
      recovered: false,
      config_options: [],
      runtime: null,
    });
    getSlashCommandsInvokeMock.mockResolvedValue([]);
    responseStreamHandlerRef.current = undefined;
    toolStdoutHandlerRef.current = undefined;
  });

  it('does not rerender for every text delta after the thought panel is cleared', async () => {
    vi.mocked(getConversationOrNull).mockResolvedValue(null);
    let renderCount = 0;
    const { result } = renderHook(() => {
      renderCount += 1;
      return useAcpMessage('conv-stream-render-budget');
    });
    await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));

    act(() => {
      result.current.setThought({ subject: '分析', description: '处理中' });
    });
    const renderCountBeforeStream = renderCount;
    const chunks = ['首', '个', '字', '符'];

    for (const data of chunks) {
      act(() => {
        responseStreamHandlerRef.current?.({
          type: 'text',
          data,
          msg_id: 'assistant-render-budget-1',
          conversation_id: 'conv-stream-render-budget',
          status: 'pending',
        });
      });
    }

    expect(result.current.thought).toEqual({ subject: '', description: '' });
    expect(renderCount - renderCountBeforeStream).toBeLessThan(chunks.length);
  });

  it('projects ordered durable kernel stdout into the existing live tool message', async () => {
    vi.mocked(getConversationOrNull).mockResolvedValue(null);
    updateMessageListMock.mockImplementation((update: (messages: never[]) => unknown) => update([]));
    const { result } = renderHook(() => useAcpMessage('conv-tool-stdout'));
    await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));

    act(() => {
      toolStdoutHandlerRef.current?.({
        frame_id: 'conv-tool-stdout',
        root_frame_id: 'conv-tool-stdout',
        tool_use_id: 'tool-python',
        tool_name: 'python',
        chunk: 'first\n',
        chunk_sequence: 1,
      });
      toolStdoutHandlerRef.current?.({
        frame_id: 'conv-tool-stdout',
        root_frame_id: 'conv-tool-stdout',
        tool_use_id: 'tool-python',
        tool_name: 'python',
        chunk: 'second\n',
        chunk_sequence: 2,
      });
      // Replayed durable sequence is ignored instead of duplicating output.
      toolStdoutHandlerRef.current?.({
        frame_id: 'conv-tool-stdout',
        root_frame_id: 'conv-tool-stdout',
        tool_use_id: 'tool-python',
        tool_name: 'python',
        chunk: 'second\n',
        chunk_sequence: 2,
      });
    });

    const existing = [
      {
        id: 'tool-python',
        msg_id: 'tool-python',
        conversation_id: 'conv-tool-stdout',
        type: 'tool_call',
        position: 'left',
        content: { call_id: 'tool-python', name: 'python', status: 'running' },
      },
    ] as never[];
    await waitFor(() => {
      const projected = updateMessageListMock.mock.calls
        .flatMap((call) =>
          (call[0] as (messages: never[]) => Array<{ type: string; content?: Record<string, unknown> }>)(existing)
        )
        .find((message) => message.type === 'tool_call' && message.content?.output === 'first\nsecond\n');
      expect(projected?.content).toMatchObject({
        name: 'python',
        status: 'running',
        output: 'first\nsecond\n',
      });
    });
  });

  it('attaches stdout received before the durable tool-start publication to that first visible row', async () => {
    vi.mocked(getConversationOrNull).mockResolvedValue(null);
    const { result } = renderHook(() => useAcpMessage('conv-tool-boundary'));
    await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));

    act(() => {
      toolStdoutHandlerRef.current?.({
        frame_id: 'conv-tool-boundary',
        root_frame_id: 'conv-tool-boundary',
        exec_id: 'exec-python',
        tool_use_id: 'tool-python',
        tool_name: 'python',
        started_at: '2026-09-11T08:00:00Z',
        background: false,
        status: 'running',
        chunk: 'already visible\n',
        chunk_sequence: 1,
        chunk_start_byte: 0,
        chunk_end_byte: 16,
      });
      responseStreamHandlerRef.current?.({
        type: 'tool_call',
        data: {
          call_id: 'tool-python',
          operation_id: 'tool-python',
          attempt: 1,
          name: 'python',
          status: 'running',
        },
        msg_id: 'transcript-tool:stream:1:tool-python',
        conversation_id: 'conv-tool-boundary',
        status: 'work',
      });
    });

    const liveTool = addOrUpdateMessageMock.mock.calls
      .map((call) => call[0] as { type?: string; content?: { output?: string } })
      .find((message) => message?.type === 'tool_call');
    expect(liveTool?.content?.output).toBe('already visible\n');
  });

  it('completes hydration when the conversation lookup fails', async () => {
    vi.mocked(getConversationOrNull).mockRejectedValue(new TypeError('Failed to fetch'));

    const { result } = renderHook(() => useAcpMessage('conv-1'));

    await waitFor(() => {
      expect(result.current.hasHydratedRunningState).toBe(true);
    });

    expect(result.current.running).toBe(false);
    expect(result.current.aiProcessing).toBe(false);
  });

  it('hydrates running state from the route-owned conversation without another request', async () => {
    const initialConversation = {
      id: 'conv-route-owned',
      type: 'acp',
      extra: {
        backend: 'synonbiomed',
        last_token_usage: { total_tokens: 321 },
        last_context_limit: 4096,
      },
      runtime: {
        state: 'running',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        pending_confirmations: 0,
        turn_id: 'turn-route-owned',
      },
    } as NonNullable<Parameters<typeof useAcpMessage>[1]>['initialConversation'];

    const { result } = renderHook(() =>
      useAcpMessage('conv-route-owned', {
        skipWarmup: true,
        initialConversation,
      })
    );

    await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));
    expect(result.current.running).toBe(true);
    expect(result.current.aiProcessing).toBe(true);
    expect(result.current.tokenUsage).toEqual({ total_tokens: 321 });
    expect(vi.mocked(getConversationOrNull)).not.toHaveBeenCalled();
  });

  it('redacts network failure diagnostics while completing hydration', async () => {
    const consoleWarn = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    const fakeKey = ['sk', 'live-secret-value'].join('-');
    vi.mocked(getConversationOrNull).mockRejectedValue(
      new TypeError(`Failed to fetch ${fakeKey} for alice@example.com token=private-token-value ${'x'.repeat(5_000)}`)
    );

    try {
      const { result } = renderHook(() => useAcpMessage('conv-redacted'));
      await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));

      const diagnostic = JSON.stringify(consoleWarn.mock.calls);
      expect(diagnostic).toContain('[REDACTED_KEY]');
      expect(diagnostic).toContain('[email]');
      expect(diagnostic).not.toContain('live-secret-value');
      expect(diagnostic).not.toContain('alice@example.com');
      expect(diagnostic).not.toContain('private-token-value');
      expect(diagnostic.length).toBeLessThan(700);
    } finally {
      consoleWarn.mockRestore();
    }
  });

  it('logs request trace lifecycle with fixed codes and closed non-identifying classifications', async () => {
    const consoleLog = vi.spyOn(console, 'log').mockImplementation(() => undefined);
    const backendCredential = ['sk', 'backend-private-value'].join('-');
    const modelCredential = ['AKIA', 'MODELPRIVATE123456'].join('');
    const sessionCredential = ['AIza', 'SessionPrivateValue123'].join('');
    vi.mocked(getConversationOrNull).mockResolvedValue(null);

    try {
      const { result } = renderHook(() => useAcpMessage('conv-trace'));
      await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));

      act(() => {
        responseStreamHandlerRef.current?.({
          type: 'request_trace',
          data: {
            timestamp: Date.now(),
            backend: backendCredential,
            model_id: modelCredential,
            session_mode: sessionCredential,
            owner: 'alice@example.com',
            path: '/home/alice/private-project',
            credential: backendCredential,
            prompt: 'sensitive '.repeat(2_000),
          },
          msg_id: 'trace-1',
          conversation_id: 'conv-trace',
        });
        responseStreamHandlerRef.current?.({
          type: 'finish',
          data: null,
          msg_id: 'trace-1',
          conversation_id: 'conv-trace',
        });
        responseStreamHandlerRef.current?.({
          type: 'start',
          data: null,
          msg_id: 'trace-2',
          conversation_id: 'conv-trace',
        });
        responseStreamHandlerRef.current?.({
          type: 'request_trace',
          data: {
            timestamp: Date.now(),
            backend: 'synonbiomed',
            model_id: 'deepseek-v4-flash',
            session_mode: 'research',
          },
          msg_id: 'trace-2',
          conversation_id: 'conv-trace',
        });
        responseStreamHandlerRef.current?.({
          type: 'error',
          data: {
            message: 'failed for alice@example.com at /home/alice/private-project',
            token: backendCredential,
          },
          msg_id: 'trace-2',
          conversation_id: 'conv-trace',
        });
      });

      const diagnostics = JSON.stringify(consoleLog.mock.calls);
      expect(diagnostics).toContain('REQUEST_TRACE_START');
      expect(diagnostics).toContain('REQUEST_TRACE_FINISH');
      expect(diagnostics).toContain('REQUEST_TRACE_ERROR');
      expect(diagnostics).toContain('backend_category');
      expect(diagnostics).toContain('model_family');
      expect(diagnostics).toContain('session_category');
      expect(diagnostics).not.toMatch(
        /alice@example\.com|private-project|private-value|MODELPRIVATE|SessionPrivate|deepseek-v4-flash|sensitive/
      );
      expect(diagnostics.length).toBeLessThan(1_000);
      expect(consoleLog.mock.calls.map((call) => (call[1] as { code?: string })?.code)).toEqual([
        'REQUEST_TRACE_START',
        'REQUEST_TRACE_FINISH',
        'REQUEST_TRACE_START',
        'REQUEST_TRACE_ERROR',
      ]);
    } finally {
      consoleLog.mockRestore();
    }
  });

  it('finalizes an error tip trace once and never reclassifies it as a later turn finish', async () => {
    const consoleLog = vi.spyOn(console, 'log').mockImplementation(() => undefined);
    vi.mocked(getConversationOrNull).mockResolvedValue(null);
    try {
      const { result } = renderHook(() => useAcpMessage('conv-trace-tip'));
      await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));

      act(() => {
        responseStreamHandlerRef.current?.({
          type: 'request_trace',
          data: {
            timestamp: Date.now(),
            backend: 'synonbiomed',
            model_id: 'deepseek-v4-flash',
          },
          msg_id: 'trace-tip-1',
          conversation_id: 'conv-trace-tip',
        });
        responseStreamHandlerRef.current?.({
          type: 'tips',
          data: { type: 'error', content: 'request failed' },
          msg_id: 'trace-tip-1',
          conversation_id: 'conv-trace-tip',
        });
        responseStreamHandlerRef.current?.({
          type: 'start',
          data: null,
          msg_id: 'trace-tip-2',
          conversation_id: 'conv-trace-tip',
        });
        responseStreamHandlerRef.current?.({
          type: 'finish',
          data: null,
          msg_id: 'trace-tip-2',
          conversation_id: 'conv-trace-tip',
        });
      });

      expect(consoleLog.mock.calls.map((call) => (call[1] as { code?: string })?.code)).toEqual([
        'REQUEST_TRACE_START',
        'REQUEST_TRACE_ERROR',
      ]);
    } finally {
      consoleLog.mockRestore();
    }
  });

  it('preserves multi-day task duration instead of truncating diagnostics at 24 hours', async () => {
    const consoleLog = vi.spyOn(console, 'log').mockImplementation(() => undefined);
    vi.mocked(getConversationOrNull).mockResolvedValue(null);
    try {
      const { result } = renderHook(() => useAcpMessage('conv-week-long-trace'));
      await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));
      const eightDaysMs = 8 * 24 * 60 * 60 * 1000;

      act(() => {
        responseStreamHandlerRef.current?.({
          type: 'request_trace',
          data: { timestamp: Date.now() - eightDaysMs, backend: 'synonbiomed' },
          msg_id: 'week-long-trace',
          conversation_id: 'conv-week-long-trace',
        });
        responseStreamHandlerRef.current?.({
          type: 'finish',
          data: null,
          msg_id: 'week-long-trace',
          conversation_id: 'conv-week-long-trace',
        });
      });

      const finish = consoleLog.mock.calls
        .map((call) => call[1] as { code?: string; duration_ms?: number })
        .find((entry) => entry.code === 'REQUEST_TRACE_FINISH');
      expect(finish?.duration_ms).toBeGreaterThanOrEqual(eightDaysMs);
    } finally {
      consoleLog.mockRestore();
    }
  });

  it('clears request traces across conversation changes and explicit reset boundaries', async () => {
    const consoleLog = vi.spyOn(console, 'log').mockImplementation(() => undefined);
    vi.mocked(getConversationOrNull).mockResolvedValue(null);
    try {
      const { result, rerender } = renderHook(({ id }) => useAcpMessage(id), {
        initialProps: { id: 'conv-trace-owner-a' },
      });
      await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));
      act(() => {
        responseStreamHandlerRef.current?.({
          type: 'request_trace',
          data: { timestamp: Date.now(), backend: 'synonbiomed' },
          msg_id: 'owner-a-trace',
          conversation_id: 'conv-trace-owner-a',
        });
      });

      rerender({ id: 'conv-trace-owner-b' });
      await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));
      act(() => {
        responseStreamHandlerRef.current?.({
          type: 'start',
          data: null,
          msg_id: 'owner-b-turn',
          conversation_id: 'conv-trace-owner-b',
        });
        responseStreamHandlerRef.current?.({
          type: 'finish',
          data: null,
          msg_id: 'owner-b-turn',
          conversation_id: 'conv-trace-owner-b',
        });
        responseStreamHandlerRef.current?.({
          type: 'start',
          data: null,
          msg_id: 'owner-b-reset-turn',
          conversation_id: 'conv-trace-owner-b',
        });
        responseStreamHandlerRef.current?.({
          type: 'request_trace',
          data: { timestamp: Date.now(), backend: 'synonbiomed' },
          msg_id: 'owner-b-reset-turn',
          conversation_id: 'conv-trace-owner-b',
        });
        result.current.resetState();
        responseStreamHandlerRef.current?.({
          type: 'start',
          data: null,
          msg_id: 'owner-b-after-reset',
          conversation_id: 'conv-trace-owner-b',
        });
        responseStreamHandlerRef.current?.({
          type: 'finish',
          data: null,
          msg_id: 'owner-b-after-reset',
          conversation_id: 'conv-trace-owner-b',
        });
      });

      expect(consoleLog.mock.calls.map((call) => (call[1] as { code?: string })?.code)).toEqual([
        'REQUEST_TRACE_START',
        'REQUEST_TRACE_START',
      ]);
    } finally {
      consoleLog.mockRestore();
    }
  });

  it('emits a synthetic thinking done update on finish when the stream never sends one', async () => {
    vi.mocked(getConversationOrNull).mockResolvedValue(null);

    const now = Date.now();
    const { result } = renderHook(() => useAcpMessage('conv-1'));
    await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));

    expect(responseStreamHandlerRef.current).toBeTypeOf('function');

    act(() => {
      responseStreamHandlerRef.current?.({
        type: 'request_trace',
        data: {
          timestamp: now - 4200,
          backend: 'claude',
          model_id: 'model-1',
        },
        msg_id: 'msg-1',
        conversation_id: 'conv-1',
      });
      responseStreamHandlerRef.current?.({
        type: 'thinking',
        data: {
          content: 'alpha',
          status: 'thinking',
        },
        msg_id: 'msg-1',
        conversation_id: 'conv-1',
      });
      responseStreamHandlerRef.current?.({
        type: 'finish',
        data: null,
        msg_id: 'msg-1',
        conversation_id: 'conv-1',
      });
    });

    expect(addOrUpdateMessageMock).toHaveBeenCalledWith(
      expect.objectContaining({
        type: 'thinking',
        msg_id: 'msg-1',
        conversation_id: 'conv-1',
        content: expect.objectContaining({
          status: 'done',
          duration: expect.any(Number),
        }),
      })
    );
  });

  it('normalizes a typed thinking stream delta and preserves reset authority', async () => {
    vi.mocked(getConversationOrNull).mockResolvedValue(null);
    const { result } = renderHook(() => useAcpMessage('conv-thinking-stream'));
    await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));

    act(() => {
      responseStreamHandlerRef.current?.({
        type: 'thinking',
        data: { content: 'corrected reasoning', status: 'thinking' },
        msg_id: 'thinking-stream-1',
        conversation_id: 'conv-thinking-stream',
        replace: true,
      });
    });

    expect(addOrUpdateMessageMock).toHaveBeenCalledWith(
      expect.objectContaining({
        type: 'thinking',
        msg_id: 'thinking-stream-1',
        content: expect.objectContaining({ content: 'corrected reasoning', replace: true, status: 'thinking' }),
      })
    );
  });

  it('routes legacy thought events through the existing thinking reducer and completes them at a tool boundary', async () => {
    vi.mocked(getConversationOrNull).mockResolvedValue(null);
    const { result } = renderHook(() => useAcpMessage('conv-thought-timeline'));
    await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));

    act(() => {
      responseStreamHandlerRef.current?.({
        type: 'thought',
        data: {
          subject: '分析路线',
          description: '先核对结构标识，再调用检索工具。',
        },
        msg_id: 'thought-turn-1',
        conversation_id: 'conv-thought-timeline',
        created_at: 1000,
      });
      responseStreamHandlerRef.current?.({
        type: 'tool_group',
        data: [],
        msg_id: 'tool-turn-1',
        conversation_id: 'conv-thought-timeline',
        created_at: 1600,
      });
    });

    expect(addOrUpdateMessageMock).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({
        type: 'thinking',
        msg_id: 'thought-turn-1',
        content: {
          content: '先核对结构标识，再调用检索工具。',
          subject: '分析路线',
          duration: undefined,
          status: 'thinking',
        },
      })
    );
    expect(addOrUpdateMessageMock).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({
        type: 'thinking',
        msg_id: 'thought-turn-1',
        content: expect.objectContaining({ status: 'done', duration: 600 }),
      })
    );
  });

  it('applies ordered text and reset updates, then rejects late deltas after the terminal event', async () => {
    vi.mocked(getConversationOrNull).mockResolvedValue(null);

    const { result } = renderHook(() => useAcpMessage('conv-1'));

    await waitFor(() => {
      expect(result.current.hasHydratedRunningState).toBe(true);
    });

    act(() => {
      responseStreamHandlerRef.current?.({
        type: 'start',
        data: {},
        msg_id: 'user-1',
        conversation_id: 'conv-1',
        turn_id: 'turn-1',
      });
      responseStreamHandlerRef.current?.({
        type: 'text',
        data: 'alpha',
        msg_id: 'assistant-1',
        conversation_id: 'conv-1',
        turn_id: 'turn-1',
        status: 'pending',
      });
      responseStreamHandlerRef.current?.({
        type: 'text',
        data: 'replacement',
        msg_id: 'assistant-1',
        conversation_id: 'conv-1',
        turn_id: 'turn-1',
        status: 'pending',
        replace: true,
      });
      responseStreamHandlerRef.current?.({
        type: 'finish',
        data: '',
        msg_id: 'assistant-1',
        conversation_id: 'conv-1',
        turn_id: 'turn-1',
        status: 'finish',
      });
    });

    expect(result.current.running).toBe(false);
    expect(addOrUpdateMessageMock).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({
        msg_id: 'assistant-1',
        content: { content: 'alpha' },
      })
    );
    expect(addOrUpdateMessageMock).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({
        msg_id: 'assistant-1',
        content: { content: 'replacement', replace: true },
      })
    );

    addOrUpdateMessageMock.mockClear();
    act(() => {
      responseStreamHandlerRef.current?.({
        type: 'text',
        data: 'late transport data',
        msg_id: 'assistant-1',
        conversation_id: 'conv-1',
        turn_id: 'turn-1',
        status: 'pending',
      });
    });

    expect(addOrUpdateMessageMock).not.toHaveBeenCalled();
    expect(result.current.running).toBe(false);

    act(() => {
      responseStreamHandlerRef.current?.({
        type: 'text',
        data: 'resumed attempt output',
        msg_id: 'assistant-2',
        conversation_id: 'conv-1',
        turn_id: 'turn-1',
        status: 'pending',
      });
    });

    expect(addOrUpdateMessageMock).toHaveBeenCalledWith(
      expect.objectContaining({
        msg_id: 'assistant-2',
        content: { content: 'resumed attempt output' },
      })
    );
    expect(result.current.running).toBe(true);

    addOrUpdateMessageMock.mockClear();
    act(() => {
      responseStreamHandlerRef.current?.({
        type: 'text',
        data: 'older attempt remains fenced',
        msg_id: 'assistant-1',
        conversation_id: 'conv-1',
        turn_id: 'turn-1',
        status: 'pending',
      });
    });
    expect(addOrUpdateMessageMock).not.toHaveBeenCalled();
  });

  it('retains publication deduplication across same-conversation status hydration', async () => {
    const initialConversation = { id: 'conv-hydration', type: 'acp', extra: {} } as NonNullable<
      Parameters<typeof useAcpMessage>[1]
    >['initialConversation'];
    const { result, rerender } = renderHook(
      ({ conversation }) => useAcpMessage('conv-hydration', { skipWarmup: true, initialConversation: conversation }),
      { initialProps: { conversation: initialConversation } }
    );
    await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));
    const message: IResponseMessage = {
      type: 'text',
      msg_id: 'assistant-segment-69',
      conversation_id: 'conv-hydration',
      data: 'Stage result ready.',
      source_publication_sequence: 1285,
      publication_boundary_id: 'publication:1285',
    };
    act(() => responseStreamHandlerRef.current?.(message));
    rerender({ conversation: { ...initialConversation } as typeof initialConversation });
    await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));
    act(() => responseStreamHandlerRef.current?.(message));
    expect(addOrUpdateMessageMock.mock.calls.filter(([item]) => item?.msg_id === message.msg_id)).toHaveLength(1);
  });

  it('keeps direct Transcript publications authoritative during history reconciliation', async () => {
    vi.mocked(getConversationOrNull).mockResolvedValue(null);

    const { result } = renderHook(() => useAcpMessage('conv-history-handoff', { skipWarmup: true }));
    await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));

    act(() => {
      responseStreamHandlerRef.current?.({
        type: 'text',
        data: '第一段',
        msg_id: 'assistant-history-handoff-1',
        conversation_id: 'conv-history-handoff',
        source_publication_sequence: 20,
        publication_boundary_id: 'transcript-web:stream-handoff:20',
      });
    });
    expect(addOrUpdateMessageMock).toHaveBeenCalledWith(expect.objectContaining({ content: { content: '第一段' } }));
    act(() => {
      responseStreamHandlerRef.current?.({
        type: 'text',
        data: '第一段',
        msg_id: 'assistant-history-handoff-1',
        conversation_id: 'conv-history-handoff',
        source_publication_sequence: 20,
        publication_boundary_id: 'transcript-web:stream-handoff:20',
      });
    });
    expect(addOrUpdateMessageMock).toHaveBeenCalledTimes(1);

    act(() => {
      responseStreamHandlerRef.current?.({
        type: 'text',
        data: '第二段',
        msg_id: 'assistant-history-handoff-1',
        conversation_id: 'conv-history-handoff',
        source_publication_sequence: 21,
        publication_boundary_id: 'transcript-web:stream-handoff:21',
      });
    });
    expect(addOrUpdateMessageMock).toHaveBeenCalledWith(expect.objectContaining({ content: { content: '第二段' } }));
    expect(addOrUpdateMessageMock).toHaveBeenCalledTimes(2);
  });

  it('completes thinking as soon as the first non-thinking message arrives', async () => {
    vi.mocked(getConversationOrNull).mockResolvedValue(null);

    const { result } = renderHook(() => useAcpMessage('conv-1'));
    await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));

    act(() => {
      responseStreamHandlerRef.current?.({
        type: 'thinking',
        data: {
          content: 'alpha',
          status: 'thinking',
        },
        msg_id: 'msg-1',
        conversation_id: 'conv-1',
        created_at: 1_000,
      });
      responseStreamHandlerRef.current?.({
        type: 'text',
        data: 'beta',
        msg_id: 'msg-1',
        conversation_id: 'conv-1',
        created_at: 4_200,
      });
    });

    expect(addOrUpdateMessageMock).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({
        type: 'thinking',
        msg_id: 'msg-1',
        content: expect.objectContaining({
          status: 'thinking',
        }),
      })
    );
    expect(addOrUpdateMessageMock).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({
        type: 'thinking',
        msg_id: 'msg-1',
        content: expect.objectContaining({
          status: 'done',
          duration: 3200,
        }),
      })
    );
    expect(addOrUpdateMessageMock).toHaveBeenNthCalledWith(
      3,
      expect.objectContaining({
        type: 'text',
        msg_id: 'msg-1',
      })
    );
  });

  it('preserves slash-command metadata from available_commands stream updates', async () => {
    vi.mocked(getConversationOrNull).mockResolvedValue(null);

    const { result } = renderHook(() => useAcpMessage('conv-1'));
    await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));

    act(() => {
      responseStreamHandlerRef.current?.({
        type: 'available_commands',
        data: {
          commands: [
            {
              name: 'review',
              description: 'Review the current diff',
              input: {
                hint: '⌘R',
              },
              _meta: {
                completion_behavior: 'neutral_tip_on_empty',
                empty_turn_tip_code: 'acp.empty_turn.choose_command',
                empty_turn_tip_params: {
                  command_count: 1,
                },
              },
            },
          ],
        },
        msg_id: 'cmd-1',
        conversation_id: 'conv-1',
      });
    });

    await waitFor(() => {
      expect(result.current.slashCommands).toEqual([
        {
          name: 'review',
          description: 'Review the current diff',
          hint: '⌘R',
          kind: 'template',
          source: 'acp',
          selectionBehavior: 'insert',
          completionBehavior: 'neutral_tip_on_empty',
          emptyTurnTipCode: 'acp.empty_turn.choose_command',
          emptyTurnTipParams: {
            command_count: 1,
          },
        },
      ]);
    });
  });

  it('loads initial slash commands after runtime ensure without legacy warmup', async () => {
    vi.mocked(getConversationOrNull).mockResolvedValue(null);
    getSlashCommandsInvokeMock.mockResolvedValue([
      {
        command: 'review',
        description: 'Review the current diff',
        completion_behavior: 'neutral_tip_on_empty',
      },
    ]);

    const { result } = renderHook(() => useAcpMessage('conv-1'));

    await waitFor(() => {
      expect(ensureRuntimeInvokeMock).toHaveBeenCalledWith({
        conversation_id: 'conv-1',
      });
      expect(getSlashCommandsInvokeMock).toHaveBeenCalledWith({
        conversation_id: 'conv-1',
      });
    });
    await waitFor(() => {
      expect(result.current.slashCommands).toEqual([
        {
          name: 'review',
          description: 'Review the current diff',
          kind: 'template',
          source: 'acp',
          selectionBehavior: 'insert',
          completionBehavior: 'neutral_tip_on_empty',
        },
      ]);
    });
  });

  it('deduplicates slash command fetches while a request is in flight', async () => {
    vi.mocked(getConversationOrNull).mockResolvedValue(null);
    const slashCommandsDeferred = deferred<
      Array<{
        command: string;
        description: string;
      }>
    >();
    getSlashCommandsInvokeMock.mockReturnValue(slashCommandsDeferred.promise);

    const { result } = renderHook(() => useAcpMessage('conv-1'));

    await waitFor(() => {
      expect(getSlashCommandsInvokeMock).toHaveBeenCalledTimes(1);
    });

    act(() => {
      result.current.fetchSlashCommands();
    });

    await waitFor(() => {
      expect(getSlashCommandsInvokeMock).toHaveBeenCalledTimes(1);
    });

    await act(async () => {
      slashCommandsDeferred.resolve([
        {
          command: 'review',
          description: 'Review the current diff',
        },
      ]);
      await slashCommandsDeferred.promise;
    });

    await waitFor(() => {
      expect(result.current.slashCommands).toEqual([
        {
          name: 'review',
          description: 'Review the current diff',
          kind: 'template',
          source: 'acp',
          selectionBehavior: 'insert',
        },
      ]);
    });
  });

  it('ignores unknown unsupported runtime events', async () => {
    vi.mocked(getConversationOrNull).mockResolvedValue(null);

    const { result } = renderHook(() => useAcpMessage('conversation-1'));
    await waitFor(() => expect(result.current.hasHydratedRunningState).toBe(true));

    act(() => {
      responseStreamHandlerRef.current?.({
        type: 'retired_runtime_message',
        data: {
          content: 'ignored',
        },
        msg_id: 'retired-message-1',
        conversation_id: 'conversation-1',
      } as unknown as IResponseMessage);
    });

    expect(addOrUpdateMessageMock).not.toHaveBeenCalled();
  });

  it('isolates the durable tool window while routing A to B and back to A', async () => {
    vi.mocked(getConversationOrNull).mockResolvedValue(null);
    const tool = (conversationId: string, callId: string) => ({
      id: `${conversationId}-${callId}`,
      conversation_id: conversationId,
      type: 'tool_call',
      position: 'left',
      content: { call_id: callId, name: 'python', args: {}, status: 'completed', output: callId },
    });
    const currentMessages: unknown[] = [tool('conversation-a', 'a-only'), tool('conversation-b', 'b-stale')];
    updateMessageListMock.mockImplementation((updater: (messages: unknown[]) => unknown[]) => {
      currentMessages.splice(0, currentMessages.length, ...updater(currentMessages));
    });

    const { rerender } = renderHook(({ id }) => useAcpMessage(id), {
      initialProps: { id: 'conversation-a' },
    });
    await waitFor(() =>
      expect(currentMessages.map((message) => (message as { conversation_id: string }).conversation_id)).toEqual([
        'conversation-a',
      ])
    );

    currentMessages.push(tool('conversation-b', 'b-only'));
    rerender({ id: 'conversation-b' });
    await waitFor(() =>
      expect(currentMessages.map((message) => (message as { conversation_id: string }).conversation_id)).toEqual([
        'conversation-b',
      ])
    );

    currentMessages.push(tool('conversation-a', 'a-returned'));
    rerender({ id: 'conversation-a' });
    await waitFor(() =>
      expect(currentMessages.map((message) => (message as { conversation_id: string }).conversation_id)).toEqual([
        'conversation-a',
      ])
    );
    expect(JSON.stringify(currentMessages)).not.toContain('b-only');
  });
});
