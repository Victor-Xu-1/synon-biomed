import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { useWorkspaceEvents } from '@/renderer/pages/conversation/Workspace/hooks/useWorkspaceEvents';

const realtime = vi.hoisted(() => ({
  responseListener: null as ((event: unknown) => void) | null,
  turnListener: null as ((event: { session_id: string }) => void) | null,
  runtimeListener: null as
    | ((event: { scope: { kind: string; id: string }; terminal_status?: 'completed' | 'failed' | 'cancelled' }) => void)
    | null,
  responseUnsubscribe: vi.fn(),
  turnUnsubscribe: vi.fn(),
  runtimeUnsubscribe: vi.fn(),
}));
const emitterMocks = vi.hoisted(() => ({ emit: vi.fn(), useAddEventListener: vi.fn() }));

vi.mock('@/common', () => ({
  ipcBridge: {
    acpConversation: {
      responseStream: {
        on: (listener: (event: unknown) => void) => {
          realtime.responseListener = listener;
          return realtime.responseUnsubscribe;
        },
      },
    },
    conversation: {
      turnCompleted: {
        on: (listener: (event: { session_id: string }) => void) => {
          realtime.turnListener = listener;
          return realtime.turnUnsubscribe;
        },
      },
      responseSearchWorkSpace: { provider: () => () => {} },
    },
    runtime: {
      statusChanged: {
        on: (listener: typeof realtime.runtimeListener) => {
          realtime.runtimeListener = listener;
          return realtime.runtimeUnsubscribe;
        },
      },
    },
  },
}));

vi.mock('@/renderer/utils/emitter', () => ({
  emitter: { emit: emitterMocks.emit },
  useAddEventListener: emitterMocks.useAddEventListener,
}));

describe('useWorkspaceEvents', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    realtime.responseListener = null;
    realtime.turnListener = null;
    realtime.runtimeListener = null;
    realtime.responseUnsubscribe.mockReset();
    realtime.turnUnsubscribe.mockReset();
    realtime.runtimeUnsubscribe.mockReset();
    emitterMocks.emit.mockReset();
    emitterMocks.useAddEventListener.mockReset();
  });

  afterEach(() => vi.useRealTimers());

  const options = (
    conversation_id: string,
    refreshWorkspace: () => void,
    ensureWorkspace: () => Promise<unknown> = vi.fn().mockResolvedValue([])
  ) => ({
    conversation_id,
    eventPrefix: 'acp' as const,
    ensureWorkspace,
    refreshWorkspace,
    clearSelection: vi.fn(),
    setFiles: vi.fn(),
    setSelected: vi.fn(),
    setExpandedKeys: vi.fn(),
    setTreeKey: vi.fn(),
    selectedNodeRef: { current: null },
    selectedKeysRef: { current: [] },
    closeContextMenu: vi.fn(),
    setContextMenu: vi.fn(),
    closeRenameModal: vi.fn(),
    closeDeleteModal: vi.fn(),
  });

  it('does not force-refresh project artifacts when the conversation changes', () => {
    const refreshWorkspace = vi.fn();
    const ensureWorkspace = vi.fn().mockResolvedValue([]);
    const rendered = renderHook(
      ({ conversation }) => useWorkspaceEvents(options(conversation, refreshWorkspace, ensureWorkspace)),
      {
        initialProps: { conversation: 'conversation-a' },
      }
    );

    expect(ensureWorkspace).toHaveBeenCalledTimes(1);
    expect(refreshWorkspace).not.toHaveBeenCalled();
    rendered.rerender({ conversation: 'conversation-b' });
    expect(ensureWorkspace).toHaveBeenCalledTimes(2);
    expect(refreshWorkspace).not.toHaveBeenCalled();
    rendered.unmount();
  });

  it('refreshes the artifact workspace at the authoritative completed-turn boundary only for this conversation', () => {
    const refreshWorkspace = vi.fn();
    const { unmount } = renderHook(() => useWorkspaceEvents(options('conversation-a', refreshWorkspace)));

    expect(realtime.turnListener).not.toBeNull();
    expect(realtime.runtimeListener).not.toBeNull();
    refreshWorkspace.mockClear();

    act(() => realtime.turnListener?.({ session_id: 'conversation-b' }));
    expect(refreshWorkspace).not.toHaveBeenCalled();

    act(() => realtime.turnListener?.({ session_id: 'conversation-a' }));
    expect(refreshWorkspace).toHaveBeenCalledTimes(1);

    act(() =>
      realtime.runtimeListener?.({
        scope: { kind: 'conversation', id: 'conversation-a' },
        terminal_status: 'completed',
      })
    );
    expect(refreshWorkspace).toHaveBeenCalledTimes(1);

    unmount();
    expect(realtime.responseUnsubscribe).toHaveBeenCalledTimes(1);
    expect(realtime.turnUnsubscribe).toHaveBeenCalledTimes(1);
    expect(realtime.runtimeUnsubscribe).toHaveBeenCalledTimes(1);
  });

  it('refreshes after a cancellation that has no turn-completed event', () => {
    const refreshWorkspace = vi.fn();
    const { unmount } = renderHook(() => useWorkspaceEvents(options('conversation-a', refreshWorkspace)));
    refreshWorkspace.mockClear();

    act(() =>
      realtime.runtimeListener?.({
        scope: { kind: 'conversation', id: 'conversation-a' },
        terminal_status: 'cancelled',
      })
    );
    expect(refreshWorkspace).toHaveBeenCalledTimes(1);

    act(() =>
      realtime.runtimeListener?.({
        scope: { kind: 'conversation', id: 'conversation-b' },
        terminal_status: 'failed',
      })
    );
    expect(refreshWorkspace).toHaveBeenCalledTimes(1);
    unmount();
  });

  it('cancels a trailing refresh when the active conversation changes', () => {
    const refreshA = vi.fn();
    const refreshB = vi.fn();
    const { rerender, unmount } = renderHook(
      ({ conversation, refresh }) => useWorkspaceEvents(options(conversation, refresh)),
      { initialProps: { conversation: 'conversation-a', refresh: refreshA } }
    );
    refreshA.mockClear();

    act(() => {
      realtime.responseListener?.({
        conversation_id: 'conversation-a',
        type: 'tool_call',
        data: { status: 'completed' },
      });
      realtime.responseListener?.({
        conversation_id: 'conversation-a',
        type: 'tool_call',
        data: { status: 'completed' },
      });
    });
    expect(refreshA).toHaveBeenCalledTimes(1);

    rerender({ conversation: 'conversation-b', refresh: refreshB });
    refreshB.mockClear();
    act(() => vi.advanceTimersByTime(2100));
    expect(refreshA).toHaveBeenCalledTimes(1);
    expect(refreshB).not.toHaveBeenCalled();

    act(() =>
      realtime.runtimeListener?.({
        scope: { kind: 'conversation', id: 'conversation-b' },
        terminal_status: 'failed',
      })
    );
    expect(refreshB).toHaveBeenCalledTimes(1);
    unmount();
  });
});
