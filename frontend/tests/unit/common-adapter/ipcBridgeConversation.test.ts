/**
 * @vitest-environment node
 */

import { beforeEach, describe, expect, it, vi } from 'vitest';

type HttpCall = {
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  path: string;
  body?: unknown;
  options?: unknown;
};

const httpBridgeMocks = vi.hoisted(() => {
  const calls: HttpCall[] = [];
  const responses = new Map<string, unknown>();
  const realtimeSubscriptions: Array<{ kind: string; type: string }> = [];
  const unavailableCodes: string[] = [];
  const mappedTransforms = new Map<string, (raw: unknown) => unknown>();
  const provider =
    (method: HttpCall['method']) =>
    <Data, Params = undefined>(
      path: string | ((params: Params) => string),
      mapBody?: (params: Params) => unknown,
      options?: unknown
    ) => ({
      provider: vi.fn(),
      invoke: vi.fn(async (params?: Params) => {
        const resolvedPath = typeof path === 'function' ? path(params as Params) : path;
        const call: HttpCall = {
          method,
          path: resolvedPath,
          body: mapBody && params !== undefined ? mapBody(params as Params) : undefined,
        };
        if (options !== undefined) call.options = options;
        calls.push(call);
        return (responses.has(resolvedPath) ? responses.get(resolvedPath) : true) as Data;
      }),
    });
  const emitter = () => ({ on: vi.fn(() => vi.fn()) });

  return {
    calls,
    responses,
    mappedTransforms,
    realtimeSubscriptions,
    unavailableCodes,
    httpGet: provider('GET'),
    httpPost: provider('POST'),
    httpPut: provider('PUT'),
    httpPatch: provider('PATCH'),
    httpDelete: provider('DELETE'),
    httpRequest: vi.fn(),
    stubProvider: vi.fn((name: string, defaultValue: unknown) => ({
      provider: vi.fn(),
      invoke: vi.fn(async () => defaultValue),
    })),
    withResponseMap: vi.fn(
      (
        inner: { provider: unknown; invoke: (params?: unknown) => Promise<unknown> },
        map: (raw: unknown) => unknown
      ) => ({
        provider: inner.provider,
        invoke: vi.fn(async (params?: unknown) => map(await inner.invoke(params))),
      })
    ),
    wsEmitter: vi.fn((kind: string, type: string) => {
      realtimeSubscriptions.push({ kind, type });
      return emitter();
    }),
    wsMappedEmitter: vi.fn((kind: string, type: string, transform: (raw: unknown) => unknown) => {
      realtimeSubscriptions.push({ kind, type });
      mappedTransforms.set(`${kind}:${type}`, transform);
      return emitter();
    }),
    unavailableEmitter: vi.fn((code: string) => {
      unavailableCodes.push(code);
      return emitter();
    }),
    realtimeReconnectedEmitter: vi.fn(emitter),
    stubEmitter: vi.fn(emitter),
  };
});

vi.mock('@/common/adapter/httpBridge', () => httpBridgeMocks);

vi.mock('@office-ai/platform', () => ({
  bridge: {
    buildProvider: vi.fn(() => ({
      provider: vi.fn(),
      invoke: vi.fn(),
    })),
    buildEmitter: vi.fn(() => ({
      on: vi.fn(() => vi.fn()),
      emit: vi.fn(),
    })),
  },
}));

describe('ipcBridge conversation adapter', () => {
  beforeEach(() => {
    httpBridgeMocks.calls.length = 0;
    httpBridgeMocks.responses.clear();
    httpBridgeMocks.httpRequest.mockReset();
    httpBridgeMocks.httpRequest.mockResolvedValue([]);
  });

  it('marks transient history projection errors as retryable diagnostics', async () => {
    const { requestConversationMessages } = await import('@/common/adapter/ipcBridge');
    const controller = new AbortController();

    await requestConversationMessages({ conversation_id: 'conversation/with space', limit: 50 }, controller.signal);

    expect(httpBridgeMocks.httpRequest).toHaveBeenCalledWith(
      'GET',
      '/api/conversations/conversation%2Fwith%20space/messages?limit=50',
      undefined,
      {
        timeoutMs: 15_000,
        signal: controller.signal,
        silentErrorCodes: ['HISTORY_NOT_READY'],
      }
    );
  });

  it('uses the transcript-backed conversation search endpoint with zero-based paging', async () => {
    const { database } = await import('@/common/adapter/ipcBridge');
    const path = '/api/conversations/search?q=CRBN%20binding&page=0&page_size=30';
    httpBridgeMocks.responses.set(path, {
      items: [],
      total: 0,
      page: 0,
      page_size: 30,
      has_more: false,
    });

    await expect(
      database.searchConversationMessages.invoke({
        keyword: 'CRBN binding',
        page: 0,
        page_size: 30,
      })
    ).resolves.toMatchObject({ items: [], page: 0, page_size: 30 });

    expect(httpBridgeMocks.calls).toContainEqual({
      method: 'GET',
      path,
      body: undefined,
    });
  });

  it('requests conversation artifacts by exact version pairs through the bounded POST contract', async () => {
    const { requestConversationArtifacts } = await import('@/common/adapter/ipcBridge');
    const controller = new AbortController();
    const references = [
      { artifact_id: 'artifact-1', version_id: 'version-1' },
      { artifact_id: 'artifact-1', version_id: 'version-2' },
    ];

    await expect(
      requestConversationArtifacts({ conversation_id: 'conversation/with space', references }, controller.signal)
    ).resolves.toEqual([]);

    expect(httpBridgeMocks.httpRequest).toHaveBeenCalledWith(
      'POST',
      '/api/conversations/conversation%2Fwith%20space/artifacts',
      { references },
      { timeoutMs: 15_000, signal: controller.signal }
    );
  });

  it('deletes conversations through the standard conversation endpoint', async () => {
    const { conversation } = await import('@/common/adapter/ipcBridge');

    await conversation.remove.invoke({ id: 'conv-1' });

    expect(httpBridgeMocks.calls).toContainEqual({
      method: 'DELETE',
      path: '/api/conversations/conv-1',
      body: undefined,
    });
  });

  it('uses the canonical frame read cursor and preserves pagehide keepalive', async () => {
    const { database } = await import('@/common/adapter/ipcBridge');
    const path = '/api/frames/conversation-1/read-cursor';
    httpBridgeMocks.responses.set(path, {
      root_frame_id: 'conversation-1',
      message_uuid: 'message-7',
      message_index: 7,
      updated_at: '2026-07-26T00:00:00Z',
    });
    await database.getConversationReadCursor.invoke({ conversation_id: 'conversation-1' });
    expect(httpBridgeMocks.calls).toContainEqual({ method: 'GET', path, body: undefined });

    await database.putConversationReadCursor.invoke({
      conversation_id: 'conversation-1',
      message_uuid: 'message-7',
      message_index: 7,
      observed_message_uuid: 'message-6',
      observed_message_index: 6,
      repair: true,
      keepalive: true,
    });
    expect(httpBridgeMocks.httpRequest).toHaveBeenCalledWith(
      'PUT',
      path,
      {
        message_uuid: 'message-7',
        message_index: 7,
        observed_message_uuid: 'message-6',
        observed_message_index: 6,
        repair: true,
      },
      { keepalive: true }
    );
  });

  it('stops the active Synon Biomed frame and releases the native composer runtime gate', async () => {
    const { conversation } = await import('@/common/adapter/ipcBridge');
    httpBridgeMocks.responses.set('/api/frames/frame%2Fwith%20space/cancel?reason=user', {
      root_frame_id: 'frame/with space',
      cancelled_frames: ['frame/with space'],
    });

    const result = await conversation.stop.invoke({
      conversation_id: 'frame/with space',
      turn_id: 'turn-1',
    });

    expect(httpBridgeMocks.calls).toContainEqual({
      method: 'POST',
      path: '/api/frames/frame%2Fwith%20space/cancel?reason=user',
      body: {},
    });
    expect(result).toEqual({
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
  });

  it('does not release the runtime gate for an invalid cancellation acknowledgement', async () => {
    const { conversation } = await import('@/common/adapter/ipcBridge');
    httpBridgeMocks.responses.set('/api/frames/frame-1/cancel?reason=user', {
      root_frame_id: 'other-frame',
      cancelled_frames: ['other-frame'],
    });

    await expect(conversation.stop.invoke({ conversation_id: 'frame-1', turn_id: 'frame-1' })).rejects.toThrow(
      'conversation_cancel_response_invalid'
    );
  });

  it('validates runtime ensure responses before exposing them to consumers', async () => {
    const { conversation } = await import('@/common/adapter/ipcBridge');
    const path = '/api/conversations/conversation-1/runtime/ensure';
    const ensured = {
      recovered: true,
      config_options: [],
      runtime: {
        state: 'idle',
        can_send_message: true,
        has_task: false,
        task_status: 'finished',
        is_processing: false,
        pending_confirmations: 0,
        turn_id: null,
      },
    };
    httpBridgeMocks.responses.set(path, ensured);
    await expect(conversation.ensureRuntime.invoke({ conversation_id: 'conversation-1' })).resolves.toEqual(ensured);
    expect(httpBridgeMocks.calls).toContainEqual({
      method: 'POST',
      path,
      body: undefined,
      options: { timeoutMs: 15_000 },
    });

    httpBridgeMocks.responses.set(path, {
      ...ensured,
      runtime: { ...ensured.runtime, has_task: true },
    });
    await expect(conversation.ensureRuntime.invoke({ conversation_id: 'conversation-1' })).rejects.toThrow(
      'conversation_runtime_invalid'
    );
  });

  it('accepts a send response only when its runtime owns the returned turn', async () => {
    const { conversation } = await import('@/common/adapter/ipcBridge');
    const path = '/api/conversations/conversation-1/messages';
    const accepted = {
      msg_id: 'message-1',
      turn_id: 'conversation-1',
      runtime: {
        state: 'running',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        pending_confirmations: 0,
        turn_id: 'conversation-1',
      },
    };
    httpBridgeMocks.responses.set(path, accepted);
    await expect(
      conversation.sendMessage.invoke({ conversation_id: 'conversation-1', input: 'run analysis' })
    ).resolves.toEqual(accepted);

    httpBridgeMocks.responses.set(path, { ...accepted, turn_id: 'conversation-2' });
    await expect(
      conversation.sendMessage.invoke({ conversation_id: 'conversation-1', input: 'run analysis' })
    ).rejects.toThrow('conversation_send_response_invalid');
  });

  it('sends exact artifact version pairs and the onboarding model context', async () => {
    const { conversation } = await import('@/common/adapter/ipcBridge');
    const path = '/api/conversations/conversation-1/messages';
    httpBridgeMocks.responses.set(path, {
      msg_id: 'message-1',
      turn_id: 'conversation-1',
      runtime: {
        state: 'running',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        pending_confirmations: 0,
        turn_id: 'conversation-1',
      },
    });
    await conversation.sendMessage.invoke({
      conversation_id: 'conversation-1',
      input: 'Analyze the cohort',
      files: [],
      artifact_refs: [
        {
          artifact_id: 'artifact-profile',
          version_id: 'version-profile',
          relation: 'attached',
          availability: 'available',
          filename: 'profile.csv',
          size_bytes: 42,
          checksum: 'abc123',
        },
      ],
      message_context: 'onboarding_first_task',
      inject_skills: ['single-cell-analysis'],
      inject_mcp_server_ids: ['pubmed'],
    });
    expect(httpBridgeMocks.calls).toContainEqual({
      method: 'POST',
      path,
      body: {
        content: 'Analyze the cohort',
        files: [],
        artifact_refs: [{ artifact_id: 'artifact-profile', version_id: 'version-profile' }],
        message_context: 'onboarding_first_task',
        loading_id: undefined,
        inject_skills: ['single-cell-analysis'],
        inject_mcp_server_ids: ['pubmed'],
        session_options: undefined,
      },
    });
  });

  it('registers the catalog-backed durable routes and history invalidation', async () => {
    await import('@/common/adapter/ipcBridge');

    expect(httpBridgeMocks.realtimeSubscriptions.toSorted((a, b) => a.type.localeCompare(b.type))).toEqual(
      [
        'compute_job_log_chunk',
        'compute_job_update',
        'confirmation.add',
        'confirmation.remove',
        'confirmation.update',
        'conversation.historyRebased',
        'conversation.listChanged',
        'execution_cell_update',
        'message.stream',
        'message.userCreated',
        'runtime.statusChanged',
        'tool_stdout_chunk',
        'turn.completed',
      ]
        .map((type) => ({ kind: 'durable', type }))
        .concat([{ kind: 'control', type: 'realtime.cursorReset' }])
        .toSorted((a, b) => a.type.localeCompare(b.type))
    );
    expect(httpBridgeMocks.unavailableCodes.toSorted()).toEqual([
      'realtime_event_unavailable_conversation_artifact',
      'realtime_event_unavailable_excel_preview_status',
      'realtime_event_unavailable_file_stream_update',
      'realtime_event_unavailable_language_changed',
      'realtime_event_unavailable_ppt_preview_status',
      'realtime_event_unavailable_preview_open',
      'realtime_event_unavailable_word_preview_status',
    ]);
    expect(httpBridgeMocks.unavailableCodes).not.toContain('realtime_message_stream_quarantined');
    expect(httpBridgeMocks.realtimeReconnectedEmitter).toHaveBeenCalledTimes(1);
  });

  it('maps message.stream only from the explicit stream_type contract', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    await import('@/common/adapter/ipcBridge');
    const transform = httpBridgeMocks.mappedTransforms.get('durable:message.stream');
    if (!transform) throw new Error('message.stream transform was not registered');

    expect(
      transform({
        type: 'message.stream',
        stream_type: 'text',
        data: 'ordered delta',
        msg_id: 'assistant-conversation-1-1',
        conversation_id: 'conversation-1',
        turn_id: 'conversation-1',
        created_at: 1_784_704_500_000,
        position: 'left',
        status: 'pending',
      })
    ).toMatchObject({
      type: 'text',
      data: 'ordered delta',
      msg_id: 'assistant-conversation-1-1',
      conversation_id: 'conversation-1',
    });

    expect(() =>
      transform({
        type: 'message.stream',
        data: 'must not be guessed',
        msg_id: 'assistant-conversation-1-1',
        conversation_id: 'conversation-1',
      })
    ).toThrow('realtime_message_stream_invalid');
    expect(() => transform({ type: 'message.stream' })).toThrow('realtime_message_stream_invalid');
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn).toHaveBeenCalledWith('[realtime]', 'realtime_message_stream_invalid');
  });

  it('preserves typed failed turn completion status', async () => {
    await import('@/common/adapter/ipcBridge');
    const transform = httpBridgeMocks.mappedTransforms.get('durable:turn.completed');
    if (!transform) throw new Error('turn.completed transform was not registered');

    const event = transform({
      type: 'turn.completed',
      session_id: 'session-1',
      turn_id: 'session-1',
      status: 'error',
      state: 'error',
      detail: 'provider_failed',
      can_send_message: true,
      runtime: {
        state: 'idle',
        can_send_message: true,
        has_task: false,
        task_status: 'error',
        is_processing: false,
        pending_confirmations: 0,
        turn_id: null,
      },
      workspace: '',
      model: { platform: 'synon-go', name: 'model-1', use_model: 'model-1' },
      last_message: {
        id: 'assistant-1',
        type: 'content',
        content: null,
        status: 'error',
        created_at: 1_784_704_500_000,
      },
    });

    expect(event).toMatchObject({
      status: 'error',
      state: 'error',
      runtime: { task_status: 'error' },
    });
  });

  it.each([
    ['missing runtime', { session_id: 'session-1', turn_id: 'session-1', status: 'finished' }],
    [
      'camel-case aliases',
      {
        sessionId: 'session-1',
        turnId: 'session-1',
        status: 'finished',
        state: 'ai_waiting_input',
        canSendMessage: true,
      },
    ],
    [
      'contradictory terminal runtime',
      {
        session_id: 'session-1',
        turn_id: 'session-1',
        status: 'finished',
        state: 'ai_waiting_input',
        detail: '',
        can_send_message: true,
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
  ])('rejects %s turn completion instead of synthesizing authority', async (_name, value) => {
    await import('@/common/adapter/ipcBridge');
    const transform = httpBridgeMocks.mappedTransforms.get('durable:turn.completed');
    if (!transform) throw new Error('turn.completed transform was not registered');

    expect(() => transform(value)).toThrow();
  });
});
