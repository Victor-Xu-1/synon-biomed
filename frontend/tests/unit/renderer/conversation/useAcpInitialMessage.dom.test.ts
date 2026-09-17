/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { act, renderHook } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const sendMessageInvokeMock = vi.fn();
const setComputeProviderMock = vi.fn();

vi.mock('@/common', () => ({
  ipcBridge: {
    acpConversation: {
      sendMessage: {
        invoke: (...args: unknown[]) => sendMessageInvokeMock(...args),
      },
    },
  },
}));

vi.mock('@/renderer/services/synonBiomedCompute', () => ({
  setSynonBiomedSessionComputeProvider: (...args: unknown[]) => setComputeProviderMock(...args),
}));

vi.mock('@/renderer/utils/file/messageFiles', () => ({
  buildDisplayMessage: (input: string) => input,
}));

vi.mock('@/renderer/utils/emitter', () => ({
  emitter: { emit: vi.fn() },
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => (key === 'common.unknownError' ? 'Unknown error' : key),
  }),
}));

import { useAcpInitialMessage } from '@/renderer/pages/conversation/platforms/acp/useAcpInitialMessage';

const createDeferred = <T>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve;
    reject = promiseReject;
  });
  return { promise, resolve, reject };
};

const createParams = (conversationId: string, streamReady = true) => ({
  conversation_id: conversationId,
  backend: 'synonbiomed',
  workspacePath: '',
  streamReady,
  setAiProcessing: vi.fn(),
  resetState: vi.fn(),
  markSendStarted: vi.fn(),
  markSendAccepted: vi.fn(),
  markSendFailed: vi.fn(),
  checkAndUpdateTitle: vi.fn(),
  addOrUpdateMessage: vi.fn(),
});

describe('useAcpInitialMessage', () => {
  beforeEach(() => {
    sessionStorage.clear();
    sendMessageInvokeMock.mockReset();
    setComputeProviderMock.mockReset();
    setComputeProviderMock.mockResolvedValue(undefined);
  });

  it('waits for realtime subscriptions before sending the routed initial message', async () => {
    const conversationId = 'initial-stream-gate';
    const storageKey = `acp_initial_message_${conversationId}`;
    const stored = JSON.stringify({ input: 'render this without refresh' });
    sessionStorage.setItem(storageKey, stored);
    sendMessageInvokeMock.mockResolvedValue({
      turn_id: 'turn-stream-gate',
      msg_id: 'msg-stream-gate',
      runtime: { backend: 'synonbiomed', status: 'running' },
    });

    const params = createParams(conversationId, false);
    const { rerender } = renderHook(({ streamReady }) => useAcpInitialMessage({ ...params, streamReady }), {
      initialProps: { streamReady: false },
    });

    await act(async () => Promise.resolve());
    expect(sendMessageInvokeMock).not.toHaveBeenCalled();
    expect(sessionStorage.getItem(storageKey)).toBe(stored);

    rerender({ streamReady: true });
    await vi.waitFor(() => expect(sendMessageInvokeMock).toHaveBeenCalledTimes(1));
    await vi.waitFor(() => expect(sessionStorage.getItem(storageKey)).toBeNull());

    rerender({ streamReady: true });
    await act(async () => Promise.resolve());
    expect(sendMessageInvokeMock).toHaveBeenCalledTimes(1);
  });

  it('keeps automatic review off when an initial-message payload does not explicitly enable it', async () => {
    const conversationId = 'initial-review-default-off';
    sessionStorage.setItem(
      `acp_initial_message_${conversationId}`,
      JSON.stringify({ input: 'start task', session_options: { ultra_mode: false } })
    );
    sendMessageInvokeMock.mockResolvedValue({
      turn_id: 'turn-review-default-off',
      msg_id: 'msg-review-default-off',
      runtime: { backend: 'synonbiomed', status: 'running' },
    });

    renderHook(() => useAcpInitialMessage(createParams(conversationId)));
    await vi.waitFor(() => expect(sendMessageInvokeMock).toHaveBeenCalledTimes(1));
    expect(sendMessageInvokeMock).toHaveBeenCalledWith(
      expect.objectContaining({
        session_options: expect.objectContaining({ verifier_mode: 'off' }),
      })
    );
  });

  it('forwards validated project artifact references with the first message', async () => {
    const conversationId = 'initial-artifact-refs';
    sessionStorage.setItem(
      `acp_initial_message_${conversationId}`,
      JSON.stringify({
        input: 'review the attached results',
        artifact_refs: [
          {
            artifact_id: 'artifact-1',
            version_id: 'version-1',
            relation: 'attached',
            availability: 'available',
            filename: 'results.csv',
            size_bytes: 128,
          },
          { artifact_id: 42, version_id: 'ignored' },
        ],
      })
    );
    sendMessageInvokeMock.mockResolvedValue({
      turn_id: 'turn-artifact-refs',
      msg_id: 'msg-artifact-refs',
      runtime: { backend: 'synonbiomed', status: 'running' },
    });

    renderHook(() => useAcpInitialMessage(createParams(conversationId)));
    await vi.waitFor(() => expect(sendMessageInvokeMock).toHaveBeenCalledTimes(1));

    expect(sendMessageInvokeMock).toHaveBeenCalledWith(
      expect.objectContaining({
        artifact_refs: [
          {
            artifact_id: 'artifact-1',
            version_id: 'version-1',
            relation: 'attached',
            availability: 'available',
            filename: 'results.csv',
            size_bytes: 128,
          },
        ],
      })
    );
  });

  it('forwards bounded fixed Skill and MCP selections with the first message', async () => {
    const conversationId = 'initial-fixed-capabilities';
    sessionStorage.setItem(
      `acp_initial_message_${conversationId}`,
      JSON.stringify({
        input: 'use the selected capabilities',
        inject_skills: [' autodock-vina ', 'autodock-vina', '', 42],
        inject_mcp_server_ids: [' bundled:pubmed ', 'bundled:pubmed', null],
      })
    );
    sendMessageInvokeMock.mockResolvedValue({
      turn_id: 'turn-fixed-capabilities',
      msg_id: 'msg-fixed-capabilities',
      runtime: { backend: 'synonbiomed', status: 'running' },
    });

    renderHook(() => useAcpInitialMessage(createParams(conversationId)));
    await vi.waitFor(() => expect(sendMessageInvokeMock).toHaveBeenCalledTimes(1));

    expect(sendMessageInvokeMock).toHaveBeenCalledWith(
      expect.objectContaining({
        inject_skills: ['autodock-vina'],
        inject_mcp_server_ids: ['bundled:pubmed'],
      })
    );
  });

  it('keeps the draft while the first send is pending and removes it only after runtime acceptance', async () => {
    const deferred = createDeferred<{
      turn_id: string;
      msg_id: string;
      runtime: { backend: string; status: string };
    }>();
    sendMessageInvokeMock.mockReturnValue(deferred.promise);
    const conversationId = 'initial-pending';
    const storageKey = `acp_initial_message_${conversationId}`;
    const stored = JSON.stringify({
      input: 'hello',
      compute_providers: ['local'],
      session_options: {
        ultra_mode: true,
        plan_mode: true,
        verifier_mode: 'off',
        memory_mode: 'on',
        target_agent: 'AIDD_EXPERT',
        model: 'primary-model',
        subagent_model: 'delegate-model',
        effort: 'high',
      },
    });
    sessionStorage.setItem(storageKey, stored);
    const params = createParams(conversationId);

    renderHook(() => useAcpInitialMessage(params));
    await vi.waitFor(() => expect(sendMessageInvokeMock).toHaveBeenCalledTimes(1));
    expect(sessionStorage.getItem(storageKey)).toBe(stored);
    expect(sendMessageInvokeMock).toHaveBeenCalledWith({
      input: 'hello',
      conversation_id: conversationId,
      files: [],
      session_options: {
        ultra_mode: true,
        plan_mode: true,
        verifier_mode: 'off',
        memory_mode: 'on',
        target_agent: 'AIDD_EXPERT',
        model: 'primary-model',
        subagent_model: 'delegate-model',
        effort: 'high',
      },
    });

    await act(async () => {
      deferred.resolve({
        turn_id: 'turn-1',
        msg_id: 'msg-1',
        runtime: { backend: 'synonbiomed', status: 'running' },
      });
      await deferred.promise;
    });

    await vi.waitFor(() => expect(sessionStorage.getItem(storageKey)).toBeNull());
    expect(params.markSendAccepted).toHaveBeenCalledWith(
      'turn-1',
      { backend: 'synonbiomed', status: 'running' },
      'msg-1'
    );
    expect(setComputeProviderMock).toHaveBeenCalledWith(conversationId, 'local', true);
  });

  it('retains the first-message draft after a runtime failure and does not duplicate the send on rerender', async () => {
    const conversationId = 'initial-failure';
    const storageKey = `acp_initial_message_${conversationId}`;
    const stored = JSON.stringify({ input: 'retry me' });
    sessionStorage.setItem(storageKey, stored);
    sendMessageInvokeMock.mockRejectedValue(new Error('runtime unavailable'));
    const params = createParams(conversationId);

    const { rerender } = renderHook(() => useAcpInitialMessage(params));
    await vi.waitFor(() => expect(params.markSendFailed).toHaveBeenCalledWith('Unknown error'));

    rerender();
    await Promise.resolve();

    expect(sendMessageInvokeMock).toHaveBeenCalledTimes(1);
    expect(sessionStorage.getItem(storageKey)).toBe(stored);
    expect(params.addOrUpdateMessage).toHaveBeenCalledTimes(1);
  });
});
