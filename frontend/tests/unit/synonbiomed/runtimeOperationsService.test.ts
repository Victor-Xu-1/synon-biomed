import {
  forkSynonBiomedAtAskUserAnswer,
  notifySynonBiomedRuntimeInvalidation,
  resolveSynonBiomedInputRequest,
  resolveSynonBiomedAskUserRequest,
  subscribeSynonBiomedRuntimeInvalidation,
} from '@/renderer/services/synonBiomedRuntimeOperations';
import { describe, expect, it, vi } from 'vitest';

const ipcRuntimeInvalidationOnMock = vi.hoisted(() => vi.fn(() => () => {}));

vi.mock('@/common', () => ({
  ipcBridge: {
    runtime: { statusChanged: { on: ipcRuntimeInvalidationOnMock } },
    conversation: {
      confirmation: {
        add: { on: ipcRuntimeInvalidationOnMock },
        update: { on: ipcRuntimeInvalidationOnMock },
        remove: { on: ipcRuntimeInvalidationOnMock },
      },
    },
    realtime: { reconnected: { on: ipcRuntimeInvalidationOnMock } },
  },
}));

const request = {
  requestId: 'request-ask-1',
  toolId: 'toolu_ask_1',
  kind: 'ask_user',
  tool: 'ask_user',
  code: null,
  description: null,
  environment: null,
  mode: null,
  questions: [],
};

const approvalRequest = {
  requestId: 'request-exec-1',
  toolId: 'toolu_exec_1',
  kind: 'local_exec',
  tool: 'python',
  code: 'print("STAT6")',
  description: null,
  environment: 'python',
  mode: 'live',
  questions: [],
};

describe('Synon Biomed ask_user runtime service', () => {
  it('routes local runtime invalidations only to the matching frame subscription', () => {
    const first = vi.fn();
    const second = vi.fn();
    const releaseFirst = subscribeSynonBiomedRuntimeInvalidation('frame-1', first);
    const releaseSecond = subscribeSynonBiomedRuntimeInvalidation('frame-2', second);

    notifySynonBiomedRuntimeInvalidation('frame-1');
    expect(first).toHaveBeenCalledOnce();
    expect(second).not.toHaveBeenCalled();

    releaseFirst();
    notifySynonBiomedRuntimeInvalidation('frame-1');
    expect(first).toHaveBeenCalledOnce();
    releaseSecond();
  });

  it('defaults to a once-only approval, omits persistent scope and does not forward runtime mode', async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true }), { status: 200 }))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ id: 'frame-1', root_frame_id: 'frame-1', status: 'processing', output_data: {} }),
          { status: 200 }
        )
      );

    await resolveSynonBiomedInputRequest('frame-1', approvalRequest, 'allow', undefined, {
      baseUrl: 'http://gateway.test',
      fetchImpl,
    });

    const [, init] = fetchImpl.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      responses: [
        {
          requestId: 'request-exec-1',
          tool_id: 'toolu_exec_1',
          approved: true,
          action: 'allow',
        },
      ],
    });
  });

  it('sends the exact persistent scope and omits it when denying', async () => {
    const projectFetch = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true }), { status: 200 }))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ id: 'frame-1', root_frame_id: 'frame-1', status: 'processing', output_data: {} }),
          { status: 200 }
        )
      );
    await resolveSynonBiomedInputRequest('frame-1', approvalRequest, 'allow', 'project', {
      baseUrl: 'http://gateway.test',
      fetchImpl: projectFetch,
    });
    expect(JSON.parse(String((projectFetch.mock.calls[0][1] as RequestInit).body))).toEqual({
      responses: [
        {
          requestId: 'request-exec-1',
          tool_id: 'toolu_exec_1',
          approved: true,
          action: 'allow',
          scope: 'project',
        },
      ],
    });

    const denyFetch = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true }), { status: 200 }))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ id: 'frame-1', root_frame_id: 'frame-1', status: 'processing', output_data: {} }),
          { status: 200 }
        )
      );
    await resolveSynonBiomedInputRequest('frame-1', approvalRequest, 'deny', 'always', {
      baseUrl: 'http://gateway.test',
      fetchImpl: denyFetch,
    });
    expect(JSON.parse(String((denyFetch.mock.calls[0][1] as RequestInit).body))).toEqual({
      responses: [
        {
          requestId: 'request-exec-1',
          tool_id: 'toolu_exec_1',
          approved: false,
          action: 'deny',
        },
      ],
    });
  });

  it('submits an answer using the v1.1 resolve-input response contract', async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true }), { status: 200 }))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ id: 'frame-1', root_frame_id: 'frame-1', status: 'processing', output_data: {} }),
          { status: 200 }
        )
      );

    await resolveSynonBiomedAskUserRequest(
      'frame-1',
      request,
      { action: 'answer', answers: { 'Primary endpoint?': 'pIC50' } },
      { baseUrl: 'http://gateway.test', fetchImpl }
    );

    const [, init] = fetchImpl.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      responses: [
        {
          requestId: 'request-ask-1',
          tool_id: 'toolu_ask_1',
          approved: true,
          action: 'answer',
          answers: { 'Primary endpoint?': 'pIC50' },
        },
      ],
    });
  });

  it('forks at the original tool use on the exact canonical branch generation', async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            root_frame_id: 'frame-root',
            active_branch_id: 'br_00000001',
            generation: 3,
            branches: [
              {
                id: 'br_00000001',
                parent_id: null,
                fork_point: null,
                active: true,
                created_at: '2026-07-25T00:00:00Z',
                updated_at: '2026-07-25T00:00:00Z',
              },
            ],
          }),
          { status: 200 }
        )
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ root_frame_id: 'frame-root', branch_id: 'br_00000002', generation: 4, status: 'accepted' }),
          { status: 200 }
        )
      );
    await expect(
      forkSynonBiomedAtAskUserAnswer(
        {
          rootFrameId: 'frame-root',
          sourceFrameId: 'frame-root',
          sourceBranchId: 'br_00000001',
          toolUseId: 'toolu_ask_1',
          response: { action: 'decide_for_me' },
          clientMutationId: 'ask-mutation-1',
        },
        { baseUrl: 'http://gateway.test', fetchImpl }
      )
    ).resolves.toEqual({ rootFrameId: 'frame-root', branchId: 'br_00000002', generation: 4 });
    const request = fetchImpl.mock.calls[1][1] as RequestInit;
    const body = JSON.parse(String(request.body)) as Record<string, unknown>;
    expect(body).toMatchObject({
      tool_use_id: 'toolu_ask_1',
      response: { action: 'decide_for_me' },
      source_branch_id: 'br_00000001',
      expected_branch_id: 'br_00000001',
      expected_generation: 3,
    });
    expect(body.client_mutation_id).toBe('ask-mutation-1');
    expect((request.headers as Record<string, string>)['Idempotency-Key']).toBe('ask-mutation-1');
  });
});
