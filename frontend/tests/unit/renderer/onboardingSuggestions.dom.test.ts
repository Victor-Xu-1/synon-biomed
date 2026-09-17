import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  requestJson: vi.fn(),
  loadSnapshot: vi.fn(),
  resolveAskUser: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedHttp', () => ({
  requestSynonBiomedJson: mocks.requestJson,
}));
vi.mock('@/renderer/services/synonBiomedRuntimeOperations', () => ({
  loadSynonBiomedRuntimeSnapshot: mocks.loadSnapshot,
  resolveSynonBiomedAskUserRequest: mocks.resolveAskUser,
}));

import {
  cancelOnboardingSuggestionFrame,
  ONBOARDING_SUGGESTION_DEBOUNCE_MS,
  ONBOARDING_SUGGESTION_FALLBACK_MS,
  ONBOARDING_SUGGESTION_MIN_CHARACTERS,
  ONBOARDING_SUGGESTION_POLL_MS,
  ONBOARDING_SUGGESTION_TIMEOUT_MS,
  requestOnboardingTaskSuggestions,
  resolveOnboardingTaskSuggestion,
} from '@/renderer/services/onboardingSuggestions';

const request = {
  requestId: 'ask-1',
  toolId: 'tool-1',
  kind: 'ask',
  tool: 'ask_user',
  code: null,
  description: null,
  environment: null,
  mode: null,
  questions: [
    {
      header: 'First task',
      question: 'Where should we start?',
      multiSelect: false,
      options: [
        { label: 'Map NEK7 literature', description: 'Review evidence', pros: null, cons: null, smiles: null },
        { label: 'Analyze NEK7 images', description: 'Inspect masks', pros: null, cons: null, smiles: null },
        { label: 'Automate NEK7 QC', description: 'Build workflow', pros: null, cons: null, smiles: null },
      ],
    },
  ],
};

const runningSnapshot = {
  frameId: 'frame-1',
  rootFrameId: 'frame-1',
  projectId: 'project-1',
  agentName: 'ONBOARDING',
  isHidden: true,
  status: 'processing',
  pendingInputRequests: [],
};

describe('structured onboarding suggestions', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    mocks.requestJson.mockResolvedValue({ frame_id: 'frame-1' });
    mocks.resolveAskUser.mockResolvedValue(undefined);
  });

  afterEach(() => vi.useRealTimers());

  it('pins the structured onboarding timing gates', () => {
    expect({
      minimum: ONBOARDING_SUGGESTION_MIN_CHARACTERS,
      debounce: ONBOARDING_SUGGESTION_DEBOUNCE_MS,
      poll: ONBOARDING_SUGGESTION_POLL_MS,
      fallback: ONBOARDING_SUGGESTION_FALLBACK_MS,
      timeout: ONBOARDING_SUGGESTION_TIMEOUT_MS,
    }).toEqual({ minimum: 40, debounce: 800, poll: 500, fallback: 6_000, timeout: 90_000 });
  });

  it('submits the preselected hidden frame and polls a strict one-question three-option AskUser result', async () => {
    mocks.loadSnapshot.mockResolvedValueOnce(runningSnapshot).mockResolvedValueOnce({
      ...runningSnapshot,
      status: 'awaiting_user_response',
      pendingInputRequests: [request],
    });
    const promise = requestOnboardingTaskSuggestions({
      projectId: 'project-1',
      frameId: 'frame-1',
      intentId: 'intent-1',
      description: `  ${'x'.repeat(40)}  `,
      attachments: [
        {
          artifactId: 'artifact-1',
          versionId: 'version-1',
          filename: 'notes.md',
          sizeBytes: 12,
          checksum: 'abc',
        },
      ],
    });
    await vi.advanceTimersByTimeAsync(500);
    await expect(promise).resolves.toMatchObject({
      frameId: 'frame-1',
      question: 'Where should we start?',
      suggestions: [{ label: 'Map NEK7 literature' }, { label: 'Analyze NEK7 images' }, { label: 'Automate NEK7 QC' }],
    });
    const [, init] = mocks.requestJson.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(String(init.body)) as Record<string, unknown>;
    expect(body).toMatchObject({
      target_agent: 'ONBOARDING',
      project_id: 'project-1',
      frame_id: 'frame-1',
      intent_id: 'intent-1',
      onboarding_mode: 'structured_wizard_v1',
    });
    expect(body.artifact_refs).toEqual([{ artifact_id: 'artifact-1', version_id: 'version-1' }]);
    expect(JSON.stringify(body)).toContain('"selfDescription":"' + 'x'.repeat(40) + '"');
    expect(JSON.stringify(body)).toContain('"attachedFilenames":["notes.md"]');
  });

  it.each([
    ['short explicit description', 'short', []],
    [
      'attachment only',
      '',
      [{ artifactId: 'artifact-1', versionId: 'version-1', filename: 'notes.md', sizeBytes: 12, checksum: 'abc' }],
    ],
  ])('accepts %s without applying the 40-character auto-dispatch gate', async (_name, description, attachments) => {
    mocks.loadSnapshot.mockResolvedValueOnce({
      ...runningSnapshot,
      status: 'awaiting_user_response',
      pendingInputRequests: [request],
    });
    await expect(
      requestOnboardingTaskSuggestions({
        projectId: 'project-1',
        frameId: 'frame-1',
        intentId: 'intent-1',
        description,
        attachments,
      })
    ).resolves.toMatchObject({ frameId: 'frame-1' });
  });

  it('rejects only a completely empty explicit request', async () => {
    await expect(
      requestOnboardingTaskSuggestions({
        projectId: 'project-1',
        frameId: 'frame-1',
        intentId: 'intent-1',
        description: '   ',
        attachments: [],
      })
    ).rejects.toThrow('input_empty');
    expect(mocks.requestJson).not.toHaveBeenCalled();
  });

  it('rejects terminal frames before considering stale pending metadata', async () => {
    mocks.loadSnapshot.mockResolvedValueOnce({
      ...runningSnapshot,
      status: 'cancelled',
      pendingInputRequests: [request],
    });
    await expect(validRequest()).rejects.toThrow('ended_without_options');
  });

  it.each([
    ['wrong project', { projectId: 'other' }],
    ['visible frame', { isHidden: false }],
    ['wrong agent', { agentName: 'OPERON' }],
    ['child frame', { rootFrameId: 'root' }],
  ])('rejects %s before accepting suggestions', async (_name, patch) => {
    mocks.loadSnapshot.mockResolvedValueOnce({
      ...runningSnapshot,
      ...patch,
      status: 'awaiting_user_response',
      pendingInputRequests: [request],
    });
    await expect(validRequest()).rejects.toThrow('frame_authority_invalid');
  });

  it('rejects Permissions, duplicate, multi-select, or non-AskUser option sets', async () => {
    for (const changed of [
      { ...request, kind: 'approval' },
      { ...request, questions: [{ ...request.questions[0], header: 'Permissions' }] },
      { ...request, questions: [{ ...request.questions[0], multiSelect: true }] },
      {
        ...request,
        questions: [
          {
            ...request.questions[0],
            options: [
              request.questions[0].options[0],
              request.questions[0].options[0],
              request.questions[0].options[2],
            ],
          },
        ],
      },
    ]) {
      mocks.loadSnapshot.mockResolvedValueOnce({
        ...runningSnapshot,
        status: 'awaiting_user_response',
        pendingInputRequests: [changed],
      });
      // Each request has a fresh exact frame submit.
      // eslint-disable-next-line no-await-in-loop
      await expect(validRequest()).rejects.toThrow(/request_invalid|options_invalid/);
    }
  });

  it('counts a hung submit in the 90 second total deadline', async () => {
    mocks.requestJson.mockImplementationOnce(
      (_path: string, init: RequestInit, options: { signal?: AbortSignal }) =>
        new Promise((_resolve, reject) => {
          options.signal?.addEventListener('abort', () => reject(options.signal?.reason), { once: true });
        })
    );
    const promise = validRequest();
    const rejected = expect(promise).rejects.toMatchObject({ name: 'TimeoutError' });
    await vi.advanceTimersByTimeAsync(90_000);
    await rejected;
  });

  it('resolves only an exact offered label and uses a distinct durable cancel endpoint', async () => {
    const session = {
      frameId: 'frame-1',
      request,
      question: 'Where should we start?',
      suggestions: request.questions[0].options.map((option) => ({
        label: option.label,
        description: option.description ?? '',
      })),
    };
    await resolveOnboardingTaskSuggestion(session, 'Analyze NEK7 images');
    expect(mocks.resolveAskUser).toHaveBeenCalledWith(
      'frame-1',
      request,
      { action: 'answer', answers: { 'Where should we start?': 'Analyze NEK7 images' } },
      {}
    );
    await expect(resolveOnboardingTaskSuggestion(session, 'invented')).rejects.toThrow('selection_invalid');
    await cancelOnboardingSuggestionFrame('frame-1');
    expect(mocks.requestJson).toHaveBeenLastCalledWith(
      '/api/frames/frame-1/cancel?reason=onboarding-suggestion-superseded',
      { method: 'POST' },
      {}
    );
  });
});

function validRequest() {
  return requestOnboardingTaskSuggestions({
    projectId: 'project-1',
    frameId: 'frame-1',
    intentId: 'intent-1',
    description: 'x'.repeat(40),
    attachments: [],
  });
}
