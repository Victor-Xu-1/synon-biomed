import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => {
  const listeners = new Map<string, (event?: never) => void>();
  return {
    listeners,
    channel: (name: string) => ({
      on: vi.fn((listener: (event?: never) => void) => {
        listeners.set(name, listener);
        return () => listeners.delete(name);
      }),
    }),
  };
});

vi.mock('@/common', () => ({
  ipcBridge: {
    acpConversation: { responseStream: mocks.channel('stream') },
    conversation: {
      userCreated: mocks.channel('user'),
      historyRebased: mocks.channel('rebase'),
      turnCompleted: mocks.channel('turn'),
      listChanged: mocks.channel('list'),
    },
    runtime: { statusChanged: mocks.channel('runtime') },
    kernel: { executionCellUpdate: mocks.channel('kernel') },
    realtime: { reconnected: mocks.channel('reconnected') },
  },
}));

import { subscribeSynonBiomedConversationWake } from '@/renderer/services/synonBiomedConversationWake';

describe('subscribeSynonBiomedConversationWake', () => {
  beforeEach(() => mocks.listeners.clear());

  it('coalesces publication bursts and promotes terminal signals over stream-only wakes', async () => {
    const wake = vi.fn();
    const release = subscribeSynonBiomedConversationWake('conversation-a', wake);

    mocks.listeners.get('stream')?.({
      conversation_id: 'conversation-a',
      type: 'text',
    } as never);
    mocks.listeners.get('stream')?.({
      conversation_id: 'conversation-b',
      type: 'finish',
    } as never);
    await Promise.resolve();
    expect(wake).not.toHaveBeenCalled();

    mocks.listeners.get('stream')?.({
      conversation_id: 'conversation-a',
      type: 'start',
    } as never);
    mocks.listeners.get('stream')?.({
      conversation_id: 'conversation-a',
      type: 'content',
    } as never);
    mocks.listeners.get('stream')?.({
      conversation_id: 'conversation-a',
      type: 'finish',
    } as never);
    mocks.listeners.get('stream')?.({
      conversation_id: 'conversation-a',
      type: 'error',
    } as never);
    mocks.listeners.get('turn')?.({ session_id: 'conversation-a' } as never);
    await Promise.resolve();
    expect(wake).toHaveBeenCalledOnce();
    expect(wake).toHaveBeenCalledWith({
      kind: 'reconcile',
      terminalBoundary: true,
    });

    release();
    expect(mocks.listeners.size).toBe(0);
  });

  it('allows one publication to promote from response-close to terminal reconcile', async () => {
    const wake = vi.fn();
    const release = subscribeSynonBiomedConversationWake('conversation-a', wake);
    const boundary = {
      source_publication_sequence: 42,
      publication_boundary_id: 'transcript-web:stream-a:42',
    };

    mocks.listeners.get('stream')?.({
      conversation_id: 'conversation-a',
      type: 'finish',
      ...boundary,
    } as never);
    await Promise.resolve();
    expect(wake).toHaveBeenNthCalledWith(1, { kind: 'reconcile' });

    mocks.listeners.get('runtime')?.({
      scope: { kind: 'conversation', id: 'conversation-a' },
      ...boundary,
    } as never);
    await Promise.resolve();
    expect(wake).toHaveBeenCalledOnce();

    mocks.listeners.get('turn')?.({
      session_id: 'conversation-a',
      ...boundary,
    } as never);
    await Promise.resolve();
    expect(wake).toHaveBeenCalledTimes(2);
    expect(wake).toHaveBeenNthCalledWith(2, {
      kind: 'reconcile',
      terminalBoundary: true,
    });

    mocks.listeners.get('turn')?.({
      session_id: 'conversation-a',
      source_publication_sequence: 43,
      publication_boundary_id: 'transcript-web:stream-a:43',
    } as never);
    await Promise.resolve();
    expect(wake).toHaveBeenCalledTimes(3);
    release();
  });

  it('keeps ordinary non-terminal runtime status on the lightweight stream path', async () => {
    const wake = vi.fn();
    const release = subscribeSynonBiomedConversationWake('conversation-a', wake);

    mocks.listeners.get('runtime')?.({
      scope: { kind: 'conversation', id: 'conversation-a' },
      phase: 'downloading',
    } as never);
    await Promise.resolve();

    expect(wake).toHaveBeenCalledOnce();
    expect(wake).toHaveBeenCalledWith({ kind: 'stream' });
    release();
  });

  it('keeps an explicit durable tool boundary on the direct stream path', async () => {
    const wake = vi.fn();
    const release = subscribeSynonBiomedConversationWake('conversation-a', wake);

    mocks.listeners.get('runtime')?.({
      scope: { kind: 'conversation', id: 'conversation-a' },
      phase: 'waiting_for_lock',
      boundary_kind: 'tool',
      tool_call_id: 'call-a',
      tool_name: 'Python',
      source_publication_sequence: 17,
      publication_boundary_id: 'transcript-web:stream-a:17',
    } as never);
    await Promise.resolve();

    expect(wake).toHaveBeenCalledOnce();
    expect(wake).toHaveBeenCalledWith({ kind: 'stream' });
    release();
  });

  it('recovers durable history only after an explicit rebase or reconnect', async () => {
    const wake = vi.fn();
    const release = subscribeSynonBiomedConversationWake('conversation-a', wake);

    mocks.listeners.get('rebase')?.({
      conversation_id: 'conversation-a',
    } as never);
    await Promise.resolve();
    expect(wake).toHaveBeenNthCalledWith(1, {
      kind: 'reconcile',
      refreshHistory: true,
    });

    mocks.listeners.get('reconnected')?.({} as never);
    await Promise.resolve();
    expect(wake).toHaveBeenNthCalledWith(2, {
      kind: 'reconcile',
      refreshHistory: true,
    });
    release();
  });

  it('reconciles terminal runtime status wakes', async () => {
    const wake = vi.fn();
    const release = subscribeSynonBiomedConversationWake('conversation-a', wake);

    mocks.listeners.get('runtime')?.({
      scope: { kind: 'conversation', id: 'conversation-a' },
      phase: 'ready',
      terminal_status: 'completed',
    } as never);
    await Promise.resolve();

    expect(wake).toHaveBeenCalledOnce();
    expect(wake).toHaveBeenCalledWith({
      kind: 'reconcile',
      terminalBoundary: true,
    });
    release();
  });

  it('keeps conversation metadata updates on the streaming read path', async () => {
    const wake = vi.fn();
    const release = subscribeSynonBiomedConversationWake('conversation-a', wake);

    mocks.listeners.get('list')?.({
      conversation_id: 'conversation-a',
      action: 'updated',
    } as never);
    await Promise.resolve();

    expect(wake).toHaveBeenCalledOnce();
    expect(wake).toHaveBeenCalledWith({ kind: 'stream' });
    release();
  });

  it('keeps conversation list mutations on the lightweight stream path', async () => {
    const wake = vi.fn();
    const release = subscribeSynonBiomedConversationWake('conversation-a', wake);

    mocks.listeners.get('list')?.({
      conversation_id: 'conversation-a',
      action: 'created',
    } as never);
    await Promise.resolve();
    mocks.listeners.get('list')?.({
      conversation_id: 'conversation-a',
      action: 'deleted',
    } as never);
    await Promise.resolve();

    expect(wake).toHaveBeenCalledTimes(2);
    expect(wake).toHaveBeenNthCalledWith(1, { kind: 'stream' });
    expect(wake).toHaveBeenNthCalledWith(2, { kind: 'stream' });
    release();
  });

  it('only wakes related frames when the realtime envelope names the root or child frame', async () => {
    const wake = vi.fn();
    const release = subscribeSynonBiomedConversationWake('root-frame', wake, {
      includeRelatedConversations: true,
    });

    mocks.listeners.get('turn')?.({
      session_id: 'unrelated-frame',
      root_frame_id: 'unrelated-root',
      frame_id: 'unrelated-frame',
    } as never);
    mocks.listeners.get('list')?.({
      conversation_id: 'unrelated-frame',
      root_frame_id: 'unrelated-root',
      frame_id: 'unrelated-frame',
      action: 'updated',
    } as never);
    mocks.listeners.get('runtime')?.({
      scope: { kind: 'conversation', id: 'unrelated-frame' },
      root_frame_id: 'unrelated-root',
      frame_id: 'unrelated-frame',
      phase: 'ready',
    } as never);
    await Promise.resolve();
    expect(wake).not.toHaveBeenCalled();

    mocks.listeners.get('turn')?.({
      session_id: 'child-frame',
      root_frame_id: 'root-frame',
      frame_id: 'child-frame',
    } as never);
    await Promise.resolve();
    expect(wake).toHaveBeenCalledOnce();
    expect(wake).toHaveBeenCalledWith({
      kind: 'reconcile',
      terminalBoundary: true,
    });
    release();
  });
});
