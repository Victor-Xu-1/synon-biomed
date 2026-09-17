import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { TChatConversation, TConversationRuntimeSummary } from '@/common/config/storage';
import {
  getSynonBiomedConversationRetryDelay,
  SYNON_BIOMED_CONVERSATION_LIVE_RECONCILE_INTERVAL_MS,
  SYNON_BIOMED_CONVERSATION_TERMINAL_FALLBACK_GRACE_MS,
  useSynonBiomedConversationSync,
  type SynonBiomedConversationSyncOptions,
} from '@/renderer/pages/conversation/platforms/acp/useSynonBiomedConversationSync';
import {
  getConversationRuntimeViewSnapshot,
  markTerminalProjectionReady,
  resetConversationRuntimeViewStoreForTest,
} from '@/renderer/pages/conversation/runtime/conversationRuntimeViewStore';

const runningRuntime: TConversationRuntimeSummary = {
  state: 'running',
  can_send_message: false,
  has_task: true,
  task_status: 'running',
  is_processing: true,
  pending_confirmations: 0,
  turn_id: 'frame-stat6',
};

const idleRuntime: TConversationRuntimeSummary = {
  state: 'idle',
  can_send_message: true,
  has_task: false,
  task_status: 'finished',
  is_processing: false,
  pending_confirmations: 0,
  turn_id: null,
};

const waitingInputRuntime: TConversationRuntimeSummary = {
  state: 'waiting_input',
  can_send_message: true,
  has_task: true,
  task_status: 'pending',
  is_processing: false,
  pending_confirmations: 0,
  turn_id: 'frame-stat6',
};

const conversationResponse = (runtime: TConversationRuntimeSummary, id = 'frame-stat6') =>
  new Response(JSON.stringify({ id, runtime }), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  });

const inertSubscribeWake = (): (() => void) => () => undefined;

type TestWakeSignal = {
  kind: 'stream' | 'reconcile';
  refreshHistory?: boolean;
  terminalBoundary?: boolean;
};

const requestPaths = (fetchMock: ReturnType<typeof vi.fn<typeof fetch>>): string[] =>
  fetchMock.mock.calls.map(([input]) => new URL(String(input), 'http://localhost').pathname);

const hasStreamingBatchRequest = (fetchMock: ReturnType<typeof vi.fn<typeof fetch>>): boolean =>
  requestPaths(fetchMock).some((path) => path.includes('/streaming-batch'));

describe('useSynonBiomedConversationSync', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    resetConversationRuntimeViewStoreForTest();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  const renderSync = (overrides: Partial<SynonBiomedConversationSyncOptions> = {}) => {
    const options: SynonBiomedConversationSyncOptions = {
      conversationId: 'frame-stat6',
      backend: 'synonbiomed',
      enabled: true,
      initialRuntime: runningRuntime,
      refreshMessages: vi.fn().mockResolvedValue([]),
      onSettled: vi.fn(),
      subscribeWake: inertSubscribeWake,
      pollIntervalMs: 100,
      ...overrides,
    };
    return {
      options,
      view: renderHook(() => useSynonBiomedConversationSync(options)),
    };
  };

  it('uses the direct Transcript authority without requesting streaming-batch', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(conversationResponse(runningRuntime));
    vi.stubGlobal('fetch', fetchMock);
    let wake = (_signal?: TestWakeSignal) => undefined;

    renderSync({
      subscribeWake: (_conversationId, callback) => {
        wake = callback;
        return vi.fn();
      },
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(fetchMock).not.toHaveBeenCalled();

    await act(async () => {
      wake({ kind: 'stream' });
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(fetchMock).not.toHaveBeenCalled();

    await act(async () => {
      wake({ kind: 'reconcile' });
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(requestPaths(fetchMock)).toEqual(['/api/conversations/frame-stat6']);
    expect(hasStreamingBatchRequest(fetchMock)).toBe(false);
  });

  it('reconciles durable cards only at a running publication boundary', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockImplementation(() => Promise.resolve(conversationResponse(runningRuntime)));
    vi.stubGlobal('fetch', fetchMock);
    const refreshMessages = vi.fn().mockImplementation(async () => {
      markTerminalProjectionReady('frame-stat6');
      return [];
    });
    let wake = (_signal?: TestWakeSignal) => undefined;

    renderSync({
      refreshMessages,
      subscribeWake: (_conversationId, callback) => {
        wake = callback;
        return vi.fn();
      },
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
      wake({ kind: 'stream' });
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(refreshMessages).not.toHaveBeenCalled();

    await act(async () => {
      wake({ kind: 'reconcile' });
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(refreshMessages).not.toHaveBeenCalled();

    await act(async () => {
      wake({ kind: 'reconcile', refreshHistory: true });
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(refreshMessages).toHaveBeenCalledOnce();
    expect(refreshMessages).toHaveBeenCalledWith(false, 'live');
    expect(hasStreamingBatchRequest(fetchMock)).toBe(false);
  });

  it('does not promote an ordinary stream wake to history refresh while the route is still enabling', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(conversationResponse(runningRuntime));
    vi.stubGlobal('fetch', fetchMock);
    const refreshMessages = vi.fn().mockResolvedValue([]);
    let wake = (_signal?: TestWakeSignal) => undefined;

    renderSync({
      enabled: false,
      refreshMessages,
      subscribeWake: (_conversationId, callback) => {
        wake = callback;
        return vi.fn();
      },
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(refreshMessages).not.toHaveBeenCalled();

    fetchMock.mockClear();
    refreshMessages.mockClear();
    await act(async () => {
      wake({ kind: 'stream' });
      await vi.advanceTimersByTimeAsync(0);
    });

    expect(fetchMock).not.toHaveBeenCalled();
    expect(refreshMessages).not.toHaveBeenCalled();
  });

  it('settles with one non-destructive terminal reconciliation', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockImplementation(() => Promise.resolve(conversationResponse(idleRuntime)));
    vi.stubGlobal('fetch', fetchMock);
    const refreshMessages = vi.fn().mockImplementation(async () => {
      markTerminalProjectionReady('frame-stat6');
      return [];
    });
    const onSettled = vi.fn();
    let wake = (_signal?: TestWakeSignal) => undefined;

    renderSync({
      refreshMessages,
      onSettled,
      subscribeWake: (_conversationId, callback) => {
        wake = callback;
        return vi.fn();
      },
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
      wake({ kind: 'reconcile' });
      await vi.advanceTimersByTimeAsync(0);
    });

    expect(refreshMessages).toHaveBeenCalledWith(false, 'refresh');
    expect(onSettled).toHaveBeenCalledOnce();
    expect(getConversationRuntimeViewSnapshot('frame-stat6')).toMatchObject({
      state: 'idle',
      isProcessing: false,
      canSendMessage: true,
      terminalProjectionPending: false,
    });
    expect(hasStreamingBatchRequest(fetchMock)).toBe(false);
  });

  it('keeps retrying terminal history hydration without publishing a premature completed capsule', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockImplementation(() => Promise.resolve(conversationResponse(idleRuntime)));
    vi.stubGlobal('fetch', fetchMock);
    const refreshMessages = vi
      .fn()
      .mockRejectedValueOnce(new Error('history temporarily unavailable'))
      .mockImplementationOnce(async () => {
        markTerminalProjectionReady('frame-stat6');
        return [];
      });
    const onSettled = vi.fn();
    let wake = (_signal?: TestWakeSignal) => undefined;

    renderSync({
      refreshMessages,
      onSettled,
      subscribeWake: (_conversationId, callback) => {
        wake = callback;
        return vi.fn();
      },
      pollIntervalMs: 100,
    });

    await act(async () => {
      wake({ kind: 'reconcile', terminalBoundary: true });
      await vi.advanceTimersByTimeAsync(0);
    });

    expect(refreshMessages).toHaveBeenCalledOnce();
    expect(onSettled).not.toHaveBeenCalled();
    expect(getConversationRuntimeViewSnapshot('frame-stat6')).toMatchObject({
      isProcessing: false,
      terminalProjectionPending: true,
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });

    expect(refreshMessages).toHaveBeenCalledTimes(2);
    expect(onSettled).toHaveBeenCalledOnce();
    expect(getConversationRuntimeViewSnapshot('frame-stat6').terminalProjectionPending).toBe(false);
  });

  it('keeps reconciling a terminal task until the final assistant projection is renderable', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockImplementation(() => Promise.resolve(conversationResponse(idleRuntime)));
    vi.stubGlobal('fetch', fetchMock);
    const refreshMessages = vi.fn().mockResolvedValue([]);
    const onSettled = vi.fn();
    let wake = (_signal?: TestWakeSignal) => undefined;

    renderSync({
      refreshMessages,
      onSettled,
      subscribeWake: (_conversationId, callback) => {
        wake = callback;
        return vi.fn();
      },
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
      wake({ kind: 'reconcile', terminalBoundary: true });
      await vi.advanceTimersByTimeAsync(0);
    });

    expect(refreshMessages).toHaveBeenCalledOnce();
    expect(onSettled).not.toHaveBeenCalled();
    expect(getConversationRuntimeViewSnapshot('frame-stat6').terminalProjectionPending).toBe(true);

    markTerminalProjectionReady('frame-stat6');
    await act(async () => {
      await vi.advanceTimersByTimeAsync(SYNON_BIOMED_CONVERSATION_TERMINAL_FALLBACK_GRACE_MS);
    });

    expect(refreshMessages).toHaveBeenCalledTimes(2);
    expect(onSettled).toHaveBeenCalledOnce();
    expect(getConversationRuntimeViewSnapshot('frame-stat6').terminalProjectionPending).toBe(false);
  });

  it('lets the terminal publication settle a turn before heartbeat fallback', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockImplementation(() => Promise.resolve(conversationResponse(idleRuntime)));
    vi.stubGlobal('fetch', fetchMock);
    const refreshMessages = vi.fn().mockImplementation(async () => {
      markTerminalProjectionReady('frame-stat6');
      return [];
    });
    const onSettled = vi.fn();

    renderSync({ refreshMessages, onSettled, pollIntervalMs: 100 });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(200);
    });

    expect(fetchMock).toHaveBeenCalledOnce();
    expect(refreshMessages).not.toHaveBeenCalled();
    expect(onSettled).not.toHaveBeenCalled();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(SYNON_BIOMED_CONVERSATION_TERMINAL_FALLBACK_GRACE_MS);
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(refreshMessages).toHaveBeenCalledOnce();
    expect(refreshMessages).toHaveBeenCalledWith(false, 'refresh');
    expect(onSettled).toHaveBeenCalledOnce();
  });

  it('uses an already loaded terminal route snapshot without extra HTTP', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(conversationResponse(idleRuntime));
    vi.stubGlobal('fetch', fetchMock);
    const initialConversation = {
      id: 'frame-stat6',
      runtime: idleRuntime,
    } as TChatConversation;

    renderSync({
      initialConversation,
      initialRuntime: idleRuntime,
      enabled: false,
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('reuses the initial history window for terminal mount reconciliation', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(conversationResponse(idleRuntime));
    vi.stubGlobal('fetch', fetchMock);
    const refreshMessages = vi.fn().mockResolvedValue([]);

    renderSync({
      enabled: false,
      initialRuntime: null,
      refreshMessages,
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    expect(refreshMessages).toHaveBeenCalledOnce();
    expect(refreshMessages).toHaveBeenCalledWith(false, 'terminal-mount');
  });

  it('reconciles an accepted local send before relying on direct stream events', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(conversationResponse(runningRuntime));
    vi.stubGlobal('fetch', fetchMock);
    const refreshMessages = vi.fn().mockResolvedValue([]);

    renderSync({
      initialRuntime: idleRuntime,
      pendingLocalSend: true,
      runtimeActive: true,
      refreshMessages,
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    expect(requestPaths(fetchMock)).toEqual(['/api/conversations/frame-stat6']);
    expect(refreshMessages).not.toHaveBeenCalled();
    expect(getConversationRuntimeViewSnapshot('frame-stat6')).toMatchObject({
      state: 'running',
      isProcessing: true,
    });
  });

  it('does not mistake an optimistic local send for observed backend processing', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockImplementation(() => Promise.resolve(conversationResponse(idleRuntime)));
    vi.stubGlobal('fetch', fetchMock);
    const refreshMessages = vi.fn().mockResolvedValue([]);
    const onSettled = vi.fn();

    renderSync({
      initialRuntime: idleRuntime,
      pendingLocalSend: true,
      runtimeActive: true,
      refreshMessages,
      onSettled,
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    expect(fetchMock).toHaveBeenCalledOnce();
    expect(refreshMessages).not.toHaveBeenCalled();
    expect(onSettled).not.toHaveBeenCalled();
  });

  it('keeps waiting-input tasks observable and settles after a later terminal wake', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(conversationResponse(waitingInputRuntime))
      .mockResolvedValueOnce(conversationResponse(idleRuntime));
    vi.stubGlobal('fetch', fetchMock);
    const onSettled = vi.fn();
    const refreshMessages = vi.fn().mockImplementation(async () => {
      markTerminalProjectionReady('frame-stat6');
      return [];
    });
    let wake = (_signal?: TestWakeSignal) => undefined;

    renderSync({
      initialRuntime: waitingInputRuntime,
      enabled: false,
      onSettled,
      refreshMessages,
      subscribeWake: (_conversationId, callback) => {
        wake = callback;
        return vi.fn();
      },
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(onSettled).not.toHaveBeenCalled();
    expect(refreshMessages).not.toHaveBeenCalled();

    await act(async () => {
      wake({ kind: 'reconcile' });
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(onSettled).toHaveBeenCalledOnce();
    expect(refreshMessages).toHaveBeenCalledOnce();
    expect(refreshMessages).toHaveBeenCalledWith(false, 'refresh');
    expect(hasStreamingBatchRequest(fetchMock)).toBe(false);
  });

  it('uses bounded conversation-only fallback and then a low-frequency heartbeat', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockImplementation(() => Promise.resolve(conversationResponse(runningRuntime)));
    vi.stubGlobal('fetch', fetchMock);
    const refreshMessages = vi.fn().mockResolvedValue([]);

    renderSync({ pollIntervalMs: 20, refreshMessages });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    const boundedCount = fetchMock.mock.calls.length;
    expect(boundedCount).toBeGreaterThan(0);
    expect(boundedCount).toBeLessThanOrEqual(10);
    expect(new Set(requestPaths(fetchMock))).toEqual(new Set(['/api/conversations/frame-stat6']));
    expect(hasStreamingBatchRequest(fetchMock)).toBe(false);
    expect(refreshMessages).not.toHaveBeenCalled();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(SYNON_BIOMED_CONVERSATION_LIVE_RECONCILE_INTERVAL_MS - 1_000);
    });
    expect(fetchMock).toHaveBeenCalledTimes(boundedCount);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(fetchMock).toHaveBeenCalledTimes(boundedCount + 1);
  });

  it('does not let ordinary stream wakes starve the terminal heartbeat', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockImplementation(() => Promise.resolve(conversationResponse(runningRuntime)));
    vi.stubGlobal('fetch', fetchMock);
    let wake = (_signal?: TestWakeSignal) => undefined;

    renderSync({
      pollIntervalMs: 20,
      subscribeWake: (_conversationId, callback) => {
        wake = callback;
        return vi.fn();
      },
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    const boundedCount = fetchMock.mock.calls.length;

    for (let elapsed = 1_000; elapsed < SYNON_BIOMED_CONVERSATION_LIVE_RECONCILE_INTERVAL_MS; elapsed += 1_000) {
      await act(async () => {
        wake({ kind: 'stream' });
        await vi.advanceTimersByTimeAsync(1_000);
      });
    }
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(fetchMock).toHaveBeenCalledTimes(boundedCount + 1);
  });

  it('stops on a deleted conversation and reports a recoverable missing state', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(null, { status: 404 }));
    vi.stubGlobal('fetch', fetchMock);
    const onIssue = vi.fn();

    renderSync({ initialRuntime: null, onIssue });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });

    expect(fetchMock).toHaveBeenCalledOnce();
    expect(onIssue).toHaveBeenLastCalledWith({
      kind: 'missing',
      retryDelayMs: null,
    });
    expect(hasStreamingBatchRequest(fetchMock)).toBe(false);
  });

  it('backs off transient reconciliation failures and redacts credentials', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockRejectedValueOnce(new Error('key sk-ant-api03-shouldNotLeak123456 is unavailable'))
      .mockResolvedValueOnce(conversationResponse(idleRuntime));
    vi.stubGlobal('fetch', fetchMock);
    const onIssue = vi.fn();

    renderSync({ initialRuntime: null, onIssue, pollIntervalMs: 100 });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(onIssue).toHaveBeenLastCalledWith({
      kind: 'unavailable',
      retryDelayMs: 100,
    });
    expect(JSON.stringify(warn.mock.calls)).toContain('[REDACTED_KEY]');
    expect(JSON.stringify(warn.mock.calls)).not.toContain('shouldNotLeak');

    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(onIssue).toHaveBeenLastCalledWith(null);
    expect(hasStreamingBatchRequest(fetchMock)).toBe(false);
  });

  it('does not activate for a non-Synon conversation', async () => {
    const fetchMock = vi.fn<typeof fetch>();
    vi.stubGlobal('fetch', fetchMock);
    const subscribeWake = vi.fn(inertSubscribeWake);

    renderSync({
      backend: 'acp',
      workspace: 'file:///tmp/project',
      subscribeWake,
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });

    expect(fetchMock).not.toHaveBeenCalled();
    expect(subscribeWake).not.toHaveBeenCalled();
  });

  it('does not restart the event authority when settlement callback identities change', async () => {
    const fetchMock = vi.fn<typeof fetch>();
    vi.stubGlobal('fetch', fetchMock);
    const subscribeWake = vi.fn(inertSubscribeWake);

    const view = renderHook(
      ({ onSettled }) =>
        useSynonBiomedConversationSync({
          conversationId: 'frame-stat6',
          backend: 'synonbiomed',
          enabled: true,
          initialRuntime: runningRuntime,
          refreshMessages: vi.fn().mockResolvedValue([]),
          onSettled,
          subscribeWake,
          pollIntervalMs: 100,
        }),
      { initialProps: { onSettled: vi.fn() } }
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    view.rerender({ onSettled: vi.fn() });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    expect(subscribeWake).toHaveBeenCalledOnce();
  });

  it('cancels fallback timers and releases the subscription on unmount', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(conversationResponse(runningRuntime));
    vi.stubGlobal('fetch', fetchMock);
    const unsubscribe = vi.fn();

    const { view } = renderSync({ subscribeWake: () => unsubscribe });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    view.unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });

    expect(unsubscribe).toHaveBeenCalledOnce();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('caps exponential retry delay at the declared maximum', () => {
    expect(getSynonBiomedConversationRetryDelay(1, 500)).toBe(500);
    expect(getSynonBiomedConversationRetryDelay(4, 500)).toBe(4_000);
    expect(getSynonBiomedConversationRetryDelay(50, 500)).toBe(10_000);
  });
});
