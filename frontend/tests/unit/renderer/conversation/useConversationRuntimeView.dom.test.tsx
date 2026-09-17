/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { resetConversationRuntimeViewStoreForTest } from '@/renderer/pages/conversation/runtime/conversationRuntimeViewStore';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { resetRendererAccountScopeForTest, setRendererAccountOwner } from '@/renderer/services/rendererAccountScope';

const mocks = vi.hoisted(() => ({
  getConversation: vi.fn(),
  writeLog: vi.fn().mockResolvedValue(undefined),
  runtimeStatusOn: vi.fn(),
  runtimeStatusListener: null as
    | null
    | ((event: { scope: { kind: string; id: string }; terminal_status?: string }) => void),
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    application: { writeRendererLog: { invoke: mocks.writeLog } },
    conversation: {
      listChanged: { on: () => () => {} },
      turnCompleted: { on: () => () => {} },
    },
    runtime: {
      statusChanged: {
        on: (listener: (event: { scope: { kind: string; id: string }; terminal_status?: string }) => void) => {
          mocks.runtimeStatusOn();
          mocks.runtimeStatusListener = listener;
          return () => {
            if (mocks.runtimeStatusListener === listener) mocks.runtimeStatusListener = null;
          };
        },
      },
    },
  },
}));

vi.mock('@/renderer/pages/conversation/utils/conversationCache', () => ({
  getConversationOrNull: mocks.getConversation,
}));

import { useConversationRuntimeView } from '@/renderer/pages/conversation/runtime/useConversationRuntimeView';

const Harness = ({
  initialConversation,
}: {
  initialConversation?: Parameters<typeof useConversationRuntimeView>[1];
}) => {
  const runtime = useConversationRuntimeView('conversation-1', initialConversation);
  return (
    <div>
      <output data-testid='hydrated'>{String(runtime.hydrated)}</output>
      <output data-testid='state'>{runtime.state}</output>
      <output data-testid='can-send'>{String(runtime.canSendMessage)}</output>
      <output data-testid='error'>{runtime.hydrationError ?? 'none'}</output>
      <button type='button' onClick={runtime.retryHydration}>
        retry
      </button>
      <button type='button' onClick={runtime.markSendStarted}>
        start send
      </button>
      <button
        type='button'
        onClick={() =>
          runtime.markSendAccepted('conversation-1', {
            state: 'running',
            can_send_message: false,
            has_task: true,
            task_status: 'running',
            is_processing: true,
            pending_confirmations: 0,
            turn_id: 'conversation-1',
          })
        }
      >
        accept resumed run
      </button>
    </div>
  );
};

describe('useConversationRuntimeView hydration authority', () => {
  beforeEach(() => {
    resetConversationRuntimeViewStoreForTest();
    resetRendererAccountScopeForTest();
    setRendererAccountOwner('owner-a');
    mocks.getConversation.mockReset();
    mocks.writeLog.mockClear();
    mocks.runtimeStatusOn.mockClear();
    mocks.runtimeStatusListener = null;
  });

  it('ignores an old owner hydration that resolves after the account changes', async () => {
    let resolveOwnerA!: (value: unknown) => void;
    mocks.getConversation.mockReturnValueOnce(
      new Promise((resolve) => {
        resolveOwnerA = resolve;
      })
    );
    render(<Harness />);
    await waitFor(() => expect(mocks.getConversation).toHaveBeenCalledTimes(1));

    act(() => {
      setRendererAccountOwner('owner-b');
      resolveOwnerA({
        runtime: {
          state: 'running',
          can_send_message: false,
          has_task: true,
          task_status: 'running',
          is_processing: true,
          pending_confirmations: 0,
          turn_id: 'owner-a-turn',
        },
      });
    });

    await waitFor(() => expect(screen.getByTestId('state')).toHaveTextContent('idle'));
    expect(screen.getByTestId('hydrated')).toHaveTextContent('false');
    expect(screen.getByTestId('state')).not.toHaveTextContent('running');
  });

  it('blocks on a redacted hydrate failure and retries into backend authority', async () => {
    mocks.getConversation
      .mockRejectedValueOnce(new Error('gateway rejected sk-ant-api03-shouldNotLeak123456 for alice@example.com'))
      .mockResolvedValueOnce({
        runtime: {
          state: 'idle',
          can_send_message: true,
          has_task: false,
          task_status: 'finished',
          is_processing: false,
          pending_confirmations: 0,
          turn_id: null,
        },
      });
    render(<Harness />);

    await waitFor(() => expect(screen.getByTestId('error')).toHaveTextContent('[REDACTED_KEY]'));
    expect(screen.getByTestId('error')).toHaveTextContent('[email]');
    expect(screen.getByTestId('error')).not.toHaveTextContent('shouldNotLeak123456');
    expect(screen.getByTestId('can-send')).toHaveTextContent('false');

    fireEvent.click(screen.getByRole('button', { name: 'retry' }));
    await waitFor(() => expect(mocks.getConversation).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByTestId('error')).toHaveTextContent('none'));
    expect(screen.getByTestId('hydrated')).toHaveTextContent('true');
    expect(screen.getByTestId('can-send')).toHaveTextContent('true');
    expect(JSON.stringify(mocks.writeLog.mock.calls)).not.toContain('shouldNotLeak123456');
    expect(JSON.stringify(mocks.writeLog.mock.calls)).not.toContain('alice@example.com');
  });

  it.each([
    ['a missing runtime', {}],
    [
      'a contradictory runtime',
      {
        runtime: {
          state: 'idle',
          can_send_message: true,
          has_task: true,
          task_status: 'finished',
          is_processing: false,
          pending_confirmations: 0,
          turn_id: null,
        },
      },
    ],
  ])('keeps sending blocked when hydration returns %s', async (_name, conversation) => {
    mocks.getConversation.mockResolvedValueOnce(conversation);
    render(<Harness />);

    await waitFor(() => expect(screen.getByTestId('hydrated')).toHaveTextContent('true'));
    expect(screen.getByTestId('can-send')).toHaveTextContent('false');
    expect(screen.getByTestId('error')).toHaveTextContent('conversation_runtime_invalid');
  });

  it('re-hydrates from backend authority when a durable runtime status invalidates the conversation', async () => {
    mocks.getConversation
      .mockResolvedValueOnce({
        runtime: {
          state: 'idle',
          can_send_message: true,
          has_task: false,
          task_status: 'finished',
          is_processing: false,
          pending_confirmations: 0,
          turn_id: null,
        },
      })
      .mockResolvedValueOnce({
        runtime: {
          state: 'idle',
          can_send_message: true,
          has_task: false,
          task_status: 'cancelled',
          is_processing: false,
          pending_confirmations: 0,
          turn_id: null,
        },
      });
    render(<Harness />);

    await waitFor(() => expect(screen.getByTestId('can-send')).toHaveTextContent('true'));
    fireEvent.click(screen.getByRole('button', { name: 'start send' }));
    await waitFor(() => expect(screen.getByTestId('can-send')).toHaveTextContent('false'));
    expect(mocks.runtimeStatusListener).not.toBeNull();
    await act(async () => {
      mocks.runtimeStatusListener?.({ scope: { kind: 'conversation', id: 'different-conversation' } });
    });
    expect(mocks.getConversation).toHaveBeenCalledTimes(1);

    await act(async () => {
      mocks.runtimeStatusListener?.({
        scope: { kind: 'conversation', id: 'conversation-1' },
        terminal_status: 'cancelled',
      });
    });

    await waitFor(() => expect(mocks.getConversation).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByTestId('can-send')).toHaveTextContent('true'));
  });

  it('reuses the route-owned runtime summary without another conversation request', async () => {
    render(
      <Harness
        initialConversation={
          {
            id: 'conversation-1',
            runtime: {
              state: 'running',
              can_send_message: false,
              has_task: true,
              task_status: 'running',
              is_processing: true,
              pending_confirmations: 0,
              turn_id: 'turn-1',
            },
          } as Parameters<typeof useConversationRuntimeView>[1]
        }
      />
    );

    await waitFor(() => expect(screen.getByTestId('hydrated')).toHaveTextContent('true'));
    expect(screen.getByTestId('can-send')).toHaveTextContent('false');
    expect(mocks.getConversation).not.toHaveBeenCalled();
  });

  it('does not let a stale route seed restore waiting input after Ask User resumes running', async () => {
    const waitingConversation = () =>
      ({
        id: 'conversation-1',
        runtime: {
          state: 'waiting_input',
          can_send_message: true,
          has_task: true,
          task_status: 'pending',
          is_processing: false,
          pending_confirmations: 0,
          turn_id: 'conversation-1',
        },
      }) as Parameters<typeof useConversationRuntimeView>[1];
    const view = render(<Harness initialConversation={waitingConversation()} />);

    await waitFor(() => expect(screen.getByTestId('state')).toHaveTextContent('waiting_input'));
    fireEvent.click(screen.getByRole('button', { name: 'accept resumed run' }));
    await waitFor(() => expect(screen.getByTestId('state')).toHaveTextContent('running'));

    view.rerender(<Harness initialConversation={waitingConversation()} />);

    expect(screen.getByTestId('state')).toHaveTextContent('running');
    expect(screen.getByTestId('can-send')).toHaveTextContent('false');
  });

  it('shares one durable binding and ignores non-terminal status chatter across consumers', async () => {
    const initialConversation = {
      id: 'conversation-1',
      runtime: {
        state: 'running',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        pending_confirmations: 0,
        turn_id: 'turn-1',
      },
    } as Parameters<typeof useConversationRuntimeView>[1];
    mocks.getConversation.mockResolvedValueOnce({
      id: 'conversation-1',
      runtime: {
        state: 'idle',
        can_send_message: true,
        has_task: false,
        task_status: 'completed',
        is_processing: false,
        pending_confirmations: 0,
        turn_id: null,
      },
    });

    render(
      <>
        <Harness initialConversation={initialConversation} />
        <Harness initialConversation={initialConversation} />
      </>
    );

    await waitFor(() => expect(mocks.runtimeStatusOn).toHaveBeenCalledTimes(1));
    expect(mocks.getConversation).not.toHaveBeenCalled();

    await act(async () => {
      mocks.runtimeStatusListener?.({
        scope: { kind: 'conversation', id: 'conversation-1' },
      });
    });
    expect(mocks.getConversation).not.toHaveBeenCalled();

    await act(async () => {
      mocks.runtimeStatusListener?.({
        scope: { kind: 'conversation', id: 'conversation-1' },
        terminal_status: 'completed',
      });
    });
    await waitFor(() => expect(mocks.getConversation).toHaveBeenCalledTimes(1));
  });

  it('reconciles a Synon Biomed terminal notification against current backend authority', async () => {
    const initialConversation = {
      id: 'conversation-1',
      extra: { backend: 'synonbiomed' },
      runtime: {
        state: 'running',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        pending_confirmations: 0,
        turn_id: 'turn-1',
      },
    } as Parameters<typeof useConversationRuntimeView>[1];

    mocks.getConversation.mockResolvedValueOnce({
      id: 'conversation-1',
      extra: { backend: 'synonbiomed' },
      runtime: {
        state: 'running',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        pending_confirmations: 0,
        turn_id: 'turn-1',
      },
    });

    render(<Harness initialConversation={initialConversation} />);

    await waitFor(() => expect(screen.getByTestId('hydrated')).toHaveTextContent('true'));
    expect(mocks.runtimeStatusListener).not.toBeNull();
    expect(mocks.getConversation).not.toHaveBeenCalled();

    await act(async () => {
      mocks.runtimeStatusListener?.({
        scope: { kind: 'conversation', id: 'conversation-1' },
        terminal_status: 'failed',
      });
    });

    await waitFor(() => expect(mocks.getConversation).toHaveBeenCalledTimes(1));
    expect(screen.getByTestId('state')).toHaveTextContent('running');
    expect(screen.getByTestId('can-send')).toHaveTextContent('false');
  });
});
