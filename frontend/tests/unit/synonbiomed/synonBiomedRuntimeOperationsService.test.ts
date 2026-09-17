import { beforeEach, describe, expect, it, vi } from 'vitest';

const requestJson = vi.hoisted(() => vi.fn());
const subscribeStatusChanged = vi.hoisted(() => vi.fn());
const subscribeReconnected = vi.hoisted(() => vi.fn());
const subscribeConfirmationAdd = vi.hoisted(() => vi.fn());
const subscribeConfirmationUpdate = vi.hoisted(() => vi.fn());
const subscribeConfirmationRemove = vi.hoisted(() => vi.fn());

vi.mock('@/common', () => ({
  ipcBridge: {
    conversation: {
      confirmation: {
        add: { on: subscribeConfirmationAdd },
        update: { on: subscribeConfirmationUpdate },
        remove: { on: subscribeConfirmationRemove },
      },
    },
    runtime: { statusChanged: { on: subscribeStatusChanged } },
    realtime: { reconnected: { on: subscribeReconnected } },
  },
}));

vi.mock('@/renderer/services/synonBiomedHttp', () => ({
  requestSynonBiomedJson: requestJson,
}));

import {
  loadSynonBiomedExecutionLogPage,
  resumeSynonBiomedFrame,
  subscribeSynonBiomedRuntimeInvalidation,
} from '@/renderer/services/synonBiomedRuntimeOperations';

describe('Synon Biomed runtime operations service', () => {
  beforeEach(() => {
    requestJson.mockReset();
    subscribeStatusChanged.mockReset();
    subscribeReconnected.mockReset();
    subscribeConfirmationAdd.mockReset();
    subscribeConfirmationUpdate.mockReset();
    subscribeConfirmationRemove.mockReset();
  });

  it('invalidates the exact runtime and confirmation scope and rehydrates after reconnect', () => {
    const releaseStatus = vi.fn();
    const releaseReconnect = vi.fn();
    const releaseConfirmationAdd = vi.fn();
    const releaseConfirmationUpdate = vi.fn();
    const releaseConfirmationRemove = vi.fn();
    let listener: ((event: Record<string, unknown>) => void) | undefined;
    let reconnect: (() => void) | undefined;
    let confirmationAdd: ((event: { conversation_id: string }) => void) | undefined;
    let confirmationUpdate: ((event: { conversation_id: string }) => void) | undefined;
    let confirmationRemove: ((event: { conversation_id: string }) => void) | undefined;
    subscribeStatusChanged.mockImplementation((nextListener) => {
      listener = nextListener;
      return releaseStatus;
    });
    subscribeReconnected.mockImplementation((nextListener) => {
      reconnect = nextListener;
      return releaseReconnect;
    });
    subscribeConfirmationAdd.mockImplementation((nextListener) => {
      confirmationAdd = nextListener;
      return releaseConfirmationAdd;
    });
    subscribeConfirmationUpdate.mockImplementation((nextListener) => {
      confirmationUpdate = nextListener;
      return releaseConfirmationUpdate;
    });
    subscribeConfirmationRemove.mockImplementation((nextListener) => {
      confirmationRemove = nextListener;
      return releaseConfirmationRemove;
    });
    const invalidate = vi.fn();

    const release = subscribeSynonBiomedRuntimeInvalidation('frame-a', invalidate);
    listener?.({ scope: { kind: 'mcp', id: 'frame-a' } });
    listener?.({ scope: { kind: 'conversation', id: 'frame-b' } });
    expect(invalidate).not.toHaveBeenCalled();

    listener?.({ scope: { kind: 'conversation', id: 'frame-a' } });
    expect(invalidate).toHaveBeenCalledOnce();
    confirmationAdd?.({ conversation_id: 'frame-b' });
    expect(invalidate).toHaveBeenCalledOnce();
    confirmationAdd?.({ conversation_id: 'frame-a' });
    confirmationUpdate?.({ conversation_id: 'frame-a' });
    confirmationRemove?.({ conversation_id: 'frame-a' });
    expect(invalidate).toHaveBeenCalledTimes(4);
    reconnect?.();
    expect(invalidate).toHaveBeenCalledTimes(5);

    release();
    release();
    expect(releaseReconnect).toHaveBeenCalledOnce();
    expect(releaseConfirmationRemove).toHaveBeenCalledOnce();
    expect(releaseConfirmationUpdate).toHaveBeenCalledOnce();
    expect(releaseConfirmationAdd).toHaveBeenCalledOnce();
    expect(releaseStatus).toHaveBeenCalledOnce();
  });

  it('sends the selected real model in the resume request and reloads the resulting frame', async () => {
    requestJson.mockResolvedValueOnce({ root_frame_id: 'frame-failed' }).mockResolvedValueOnce({
      id: 'frame-failed',
      root_frame_id: 'frame-failed',
      status: 'processing',
      model: 'model-c',
    });

    const result = await resumeSynonBiomedFrame('frame-failed', { model: 'model-c' });

    expect(requestJson).toHaveBeenNthCalledWith(
      1,
      '/api/frames/frame-failed/resume',
      {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: '{"model":"model-c"}',
      },
      {}
    );
    expect(requestJson).toHaveBeenNthCalledWith(2, '/api/frames/frame-failed', {}, { timeoutMs: 15_000 });
    expect(result.snapshot).toMatchObject({
      frameId: 'frame-failed',
      status: 'processing',
      modelId: 'model-c',
    });
    expect(result.runtime).toMatchObject({ state: 'running', is_processing: true });
  });

  it('loads a bounded 200-record execution-log page with an opaque older cursor', async () => {
    requestJson.mockResolvedValue({
      records: [
        {
          id: 'cell-200',
          frame_id: 'frame-a',
          cell_index: 200,
          kernel_id: 'kernel-a',
          conda_env: 'python',
          language: 'python',
          source: 'print(200)',
          exit_status: 'ok',
        },
      ],
      next_before: 'opaque-cursor',
      total: 401,
    });

    const page = await loadSynonBiomedExecutionLogPage('frame-a', 'older-cursor');

    expect(requestJson).toHaveBeenCalledWith('/api/frames/frame-a/execution-log?limit=200&before=older-cursor', {}, {});
    expect(page).toMatchObject({
      nextBefore: 'opaque-cursor',
      total: 401,
      records: [{ id: 'cell-200', cellIndex: 200, source: 'print(200)' }],
    });
  });
});
