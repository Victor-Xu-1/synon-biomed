import {
  chooseSynonBiomedRecoveryModel,
  doesSynonBiomedRuntimeSnapshotNeedActiveRefresh,
  isSynonBiomedAgentChoiceOption,
  normalizeSynonBiomedExecutionLog,
  normalizeSynonBiomedPlanDocument,
  normalizeSynonBiomedRuntimeSnapshot,
  projectSynonBiomedTaskAuthority,
  toSynonAIRuntimeSummary,
} from '@/renderer/components/synonBiomed/runtime/runtimeOperationsModel';
import { describe, expect, it } from 'vitest';

describe('Synon Biomed runtime operations model', () => {
  it('recognizes v1.1 agent-choice aliases without hiding scientific options', () => {
    expect(isSynonBiomedAgentChoiceOption('You decide for me')).toBe(true);
    expect(isSynonBiomedAgentChoiceOption('Skip this question')).toBe(true);
    expect(isSynonBiomedAgentChoiceOption("I'll figure it out later")).toBe(true);
    expect(isSynonBiomedAgentChoiceOption('Compound A')).toBe(false);
  });
  it('normalizes a failed frame into an actionable runtime snapshot', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-failed',
      root_frame_id: 'frame-failed',
      status: 'failed',
      status_description: 'Checking safe-mol availability',
      output_data: { error: 'daemon restarted while frame was running' },
      runtime_failure_reason: 'runtime_draining',
      created_at: '2026-07-11T00:15:00.000Z',
      completed_at: '2026-07-11T00:16:00.000Z',
      updated_at: '2026-07-11T00:16:03.905Z',
    });

    expect(snapshot).toEqual({
      frameId: 'frame-failed',
      rootFrameId: 'frame-failed',
      projectId: null,
      agentName: null,
      isHidden: false,
      status: 'failed',
      statusDescription: 'Checking safe-mol availability',
      error: 'daemon restarted while frame was running',
      failureKind: 'service_interrupted',
      failureReason: 'runtime_draining',
      errorStatus: null,
      modelId: null,
      taskMetrics: null,
      createdAt: '2026-07-11T00:15:00.000Z',
      completedAt: '2026-07-11T00:16:00.000Z',
      updatedAt: '2026-07-11T00:16:03.905Z',
      runtimeElapsedMs: null,
      runtimeObservedAt: null,
      runtimeStartedAt: null,
      runtimeFinishedAt: null,
      runtimeActive: null,
      runtimeTaskElapsedMs: null,
      runtimeTaskObservedAt: null,
      runtimeTaskStartedAt: null,
      runtimeTaskFinishedAt: null,
      runtimeTaskActive: null,
      runtimeAttempt: null,
      runtimeInputRevision: null,
      runtimeStage: null,
      reviewStatus: null,
      reviewKind: null,
      reviewProfile: null,
      reviewFrameId: null,
      reviewDescription: null,
      reviewTrigger: null,
      reviewVerdict: null,
      reviewIssueCount: 0,
      reviewBlockingIssueCount: 0,
      canCancel: false,
      canResume: true,
      pendingInputRequests: [],
      taskPlan: null,
      planApproval: null,
    });
    expect(toSynonAIRuntimeSummary(snapshot)).toMatchObject({
      state: 'idle',
      can_send_message: true,
      is_processing: false,
      task_status: 'error',
      turn_id: null,
    });
  });

  it('projects the closed workspace failure kind without collapsing it to generic', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-network-bridge',
      status: 'failed',
      output_data: {
        error: 'The governed network bridge is unavailable.',
        failure_kind: 'network_bridge_down',
      },
      runtime_failure_reason: 'tool_failed',
    });

    expect(snapshot.failureKind).toBe('network_bridge_down');
    expect(snapshot.error).toBe('The governed network bridge is unavailable.');
  });

  it('projects authoritative task totals and latest-round metrics while ignoring legacy frame totals', () => {
    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'frame-with-usage',
        status: 'completed',
        input_tokens: 999_999,
        output_tokens: 999_999,
        runtime_input_tokens: 172_493,
        runtime_output_tokens: 298,
        runtime_cache_read_tokens: 90_112,
        runtime_cache_write_tokens: 0,
        runtime_total_tokens: 172_791,
        runtime_model_call_count: 2,
        runtime_tool_call_count: 1,
        runtime_task_input_tokens: 26_895_929,
        runtime_task_output_tokens: 335_910,
        runtime_task_cache_read_tokens: 17_595_136,
        runtime_task_cache_write_tokens: 0,
        runtime_task_total_tokens: 27_231_839,
        runtime_task_model_call_count: 514,
        runtime_task_tool_call_count: 254,
      }).taskMetrics
    ).toEqual({
      total: {
        inputTokens: 26_895_929,
        outputTokens: 335_910,
        cacheReadTokens: 17_595_136,
        cacheWriteTokens: 0,
        totalTokens: 27_231_839,
        modelCallCount: 514,
        toolCallCount: 254,
      },
      latest: {
        inputTokens: 172_493,
        outputTokens: 298,
        cacheReadTokens: 90_112,
        cacheWriteTokens: 0,
        totalTokens: 172_791,
        modelCallCount: 2,
        toolCallCount: 1,
      },
    });
    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'frame-without-task-usage',
        status: 'completed',
        input_tokens: 12_000,
        output_tokens: 3_500,
      }).taskMetrics
    ).toBeNull();
  });

  it('accepts only bounded machine failure reasons from the runtime projection', () => {
    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'frame-safe-failure-reason',
        status: 'failed',
        runtime_failure_reason: 'python_file_not_found',
      }).failureReason
    ).toBe('python_file_not_found');
    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'frame-unsafe-failure-reason',
        status: 'failed',
        runtime_failure_reason: 'sk-secretValue123456 for alice@example.com',
      }).failureReason
    ).toBeNull();
    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'frame-oversized-failure-reason',
        status: 'failed',
        runtime_failure_reason: 'a'.repeat(65),
      }).failureReason
    ).toBeNull();
  });

  it('maps internal result-correction reasons to the public rejected-result category', () => {
    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'frame-presentation-correction',
        status: 'failed',
        runtime_failure_reason: 'response_language_mismatch',
      })
    ).toMatchObject({ failureReason: 'response_language_mismatch', failureKind: 'result_rejected' });
    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'frame-artifact-correction-exhausted',
        status: 'failed',
        runtime_failure_reason: 'artifact_reference_correction_required',
      })
    ).toMatchObject({
      failureReason: 'artifact_reference_correction_required',
      failureKind: 'result_rejected',
    });
  });

  it('uses the durable failed status description when legacy output data has no error', () => {
    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'frame-resume-dispatch-failed',
        status: 'failed',
        status_description: 'read completion recovery candidate: transcript event conflicts with durable state',
        output_data: {},
      })
    ).toMatchObject({
      error: 'read completion recovery candidate: transcript event conflicts with durable state',
      failureKind: 'generic',
    });

    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'frame-generic-output-error',
        status: 'failed',
        status_description: 'OpenAI chat stream function call 0 has invalid JSON arguments',
        output_data: { error: 'failed' },
      })
    ).toMatchObject({
      error: 'OpenAI chat stream function call 0 has invalid JSON arguments',
      failureKind: 'generic',
    });

    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'frame-running-description',
        status: 'processing',
        status_description: 'Querying public biomedical databases',
        output_data: {},
      })
    ).toMatchObject({ error: null, statusDescription: 'Querying public biomedical databases' });
  });

  it('normalizes the durable active-work clock without accepting malformed values', () => {
    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'frame-runtime-clock',
        status: 'processing',
        runtime_stage: 'starting',
        runtime_elapsed_ms: 125_500,
        runtime_observed_at: '2026-08-10T08:00:00Z',
        runtime_started_at: '2026-08-10T07:55:00Z',
        runtime_finished_at: null,
        runtime_active: true,
        runtime_task_elapsed_ms: 600_000,
        runtime_task_observed_at: '2026-08-10T08:00:00Z',
        runtime_task_started_at: '2026-08-09T22:00:00Z',
        runtime_task_finished_at: null,
        runtime_task_active: true,
        runtime_attempt: 7,
        runtime_input_revision: 4,
      })
    ).toMatchObject({
      runtimeElapsedMs: 125_500,
      runtimeObservedAt: '2026-08-10T08:00:00Z',
      runtimeStartedAt: '2026-08-10T07:55:00Z',
      runtimeFinishedAt: null,
      runtimeActive: true,
      runtimeTaskElapsedMs: 600_000,
      runtimeTaskObservedAt: '2026-08-10T08:00:00Z',
      runtimeTaskStartedAt: '2026-08-09T22:00:00Z',
      runtimeTaskFinishedAt: null,
      runtimeTaskActive: true,
      runtimeAttempt: 7,
      runtimeInputRevision: 4,
      runtimeStage: 'starting',
    });

    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'frame-invalid-runtime-clock',
        status: 'processing',
        runtime_elapsed_ms: -1,
        runtime_active: 'true',
        runtime_attempt: 0,
        runtime_input_revision: 1.5,
      })
    ).toMatchObject({
      runtimeElapsedMs: null,
      runtimeActive: null,
      runtimeTaskElapsedMs: null,
      runtimeTaskActive: null,
      runtimeAttempt: null,
      runtimeInputRevision: null,
    });
  });

  it('normalizes detached review state without changing the completed task authority', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-reviewed',
      status: 'completed',
      runtime_active: false,
      runtime_stage: 'reviewing',
      runtime_review_status: 'processing',
      runtime_review_kind: 'runner_completion_review',
      runtime_review_profile: 'REVIEWER',
      runtime_review_frame_id: 'completion-review-1',
      runtime_review_description: 'Independent completion review is running',
      runtime_review_trigger: 'manual',
      runtime_review_verdict: 'revise',
      runtime_review_issue_count: 3,
      runtime_review_blocking_issue_count: 2,
    });

    expect(snapshot).toMatchObject({
      status: 'completed',
      runtimeActive: false,
      runtimeStage: 'reviewing',
      reviewStatus: 'processing',
      reviewKind: 'runner_completion_review',
      reviewProfile: 'REVIEWER',
      reviewFrameId: 'completion-review-1',
      reviewTrigger: 'manual',
      reviewVerdict: 'revise',
      reviewIssueCount: 3,
      reviewBlockingIssueCount: 2,
      canResume: false,
    });
    expect(doesSynonBiomedRuntimeSnapshotNeedActiveRefresh(snapshot)).toBe(true);

    expect(
      doesSynonBiomedRuntimeSnapshotNeedActiveRefresh(
        normalizeSynonBiomedRuntimeSnapshot({
          id: 'frame-review-completed',
          status: 'completed',
          runtime_stage: 'review_completed',
          runtime_review_status: 'completed',
        })
      )
    ).toBe(false);
  });

  it('keeps a terminal task in finalizing until its final message projection is readable', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-finalizing',
      status: 'finalizing',
      runtime_stage: 'finalizing',
      runtime_active: false,
      runtime_task_active: false,
      runtime_finished_at: '2026-08-27T07:38:51Z',
      runtime_task_finished_at: '2026-08-27T07:38:51Z',
    });

    expect(snapshot).toMatchObject({
      status: 'finalizing',
      runtimeStage: 'finalizing',
      canCancel: false,
      canResume: false,
    });
    expect(projectSynonBiomedTaskAuthority(snapshot)).toEqual({
      phase: 'finalizing',
      taskCompleted: false,
    });
    expect(doesSynonBiomedRuntimeSnapshotNeedActiveRefresh(snapshot)).toBe(true);
  });

  it('lets a durable failed frame override a stale finalizing stage', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-failed-after-finalizing',
      status: 'failed',
      runtime_stage: 'finalizing',
      runtime_active: false,
      runtime_task_active: false,
      runtime_finished_at: '2026-08-27T19:18:14Z',
      runtime_task_finished_at: '2026-08-27T19:18:14Z',
    });

    expect(projectSynonBiomedTaskAuthority(snapshot)).toEqual({
      phase: 'failed',
      taskCompleted: false,
    });
    expect(doesSynonBiomedRuntimeSnapshotNeedActiveRefresh(snapshot)).toBe(false);
  });

  it('fails closed for malformed runtime timestamps', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-running',
      status: 'running',
      created_at: '2026-02-31T00:00:00Z',
      completed_at: '2026-07-11T25:00:00Z',
      updated_at: '2026-07-11T00:16:03.905Z',
    });

    expect(snapshot).toMatchObject({
      createdAt: null,
      completedAt: null,
      updatedAt: '2026-07-11T00:16:03.905Z',
    });
  });

  it('does not report cancelled frames as successfully finished tasks', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-cancelled',
      status: 'cancelled',
      status_description: 'Stopped by user',
    });

    expect(snapshot).toMatchObject({ status: 'cancelled', canResume: true });
    expect(toSynonAIRuntimeSummary(snapshot)).toMatchObject({
      state: 'idle',
      task_status: 'error',
      is_processing: false,
      can_send_message: true,
    });
  });

  it('projects a provider-paused run as resumable pending work without spinning', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-paused',
      root_frame_id: 'frame-paused',
      status: 'paused',
      status_description: 'provider quota is exhausted; choose another model and continue',
      runtime_interruption_reason: 'model_provider_unavailable',
      runtime_active: false,
      runtime_elapsed_ms: 120_000,
      runtime_observed_at: '2026-08-10T08:02:00Z',
      runtime_started_at: '2026-08-10T08:00:00Z',
    });

    expect(snapshot).toMatchObject({
      status: 'paused',
      runtimeActive: false,
      canResume: true,
      failureReason: 'model_provider_unavailable',
    });
    expect(toSynonAIRuntimeSummary(snapshot)).toEqual({
      state: 'paused',
      can_send_message: true,
      has_task: true,
      task_status: 'pending',
      is_processing: false,
      pending_confirmations: 0,
      turn_id: 'frame-paused',
    });
  });

  it('keeps an unconfirmed legacy repair status running instead of fabricating user input', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-repair-input',
      root_frame_id: 'frame-repair-input',
      status: 'awaiting_input',
      status_description: 'Review found an incomplete requested compute lane',
    });

    expect(toSynonAIRuntimeSummary(snapshot)).toEqual({
      state: 'running',
      can_send_message: false,
      has_task: true,
      task_status: 'running',
      is_processing: true,
      pending_confirmations: 0,
      turn_id: 'frame-repair-input',
    });
  });

  it('keeps cancelling authoritative until the backend publishes a terminal state', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-cancelling',
      root_frame_id: 'frame-cancelling',
      status: 'cancelling',
      status_description: 'Stopping active tools',
    });

    expect(toSynonAIRuntimeSummary(snapshot)).toEqual({
      state: 'cancelling',
      can_send_message: false,
      has_task: true,
      task_status: 'running',
      is_processing: true,
      pending_confirmations: 0,
      turn_id: 'frame-cancelling',
    });
  });

  it('keeps initializing as an active starting phase', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-initializing',
      root_frame_id: 'frame-initializing',
      status: 'initializing',
      status_description: 'Preparing the runtime',
    });

    expect(snapshot).toMatchObject({ status: 'initializing', canCancel: true });
    expect(toSynonAIRuntimeSummary(snapshot)).toMatchObject({
      state: 'starting',
      can_send_message: false,
      has_task: true,
      task_status: 'pending',
      is_processing: true,
      turn_id: 'frame-initializing',
    });
  });

  it('fails closed instead of treating an unsupported runtime status as sendable', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-unsupported',
      status: 'future_runtime_state',
    });

    expect(() => toSynonAIRuntimeSummary(snapshot)).toThrowError('runtime_status_unsupported');
  });

  it('does not classify unrelated daemon or analysis failures as service interruptions', () => {
    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'analysis-failed',
        status: 'failed',
        output_data: { error: 'analysis process exited with status 1' },
      })
    ).toMatchObject({ failureKind: 'generic' });
    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'daemon-unavailable',
        status: 'failed',
        output_data: { error: 'daemon connection unavailable' },
      })
    ).toMatchObject({ failureKind: 'generic' });
  });

  it('classifies model, safety, and overload failures from structured frame data', () => {
    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'model-missing',
        status: 'failed',
        model: 'provider/model-a',
        output_data: { error_kind: 'model_not_found' },
      })
    ).toMatchObject({
      failureKind: 'model_not_found',
      modelId: 'provider/model-a',
      errorStatus: null,
    });

    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'safety-paused',
        status: 'failed',
        context_data: { _original_input: { model: 'provider/model-b' } },
        output_data: { stop_reason: 'refusal' },
      })
    ).toMatchObject({
      failureKind: 'safety_refusal',
      modelId: 'provider/model-b',
      errorStatus: null,
    });

    expect(
      normalizeSynonBiomedRuntimeSnapshot({
        id: 'model-overloaded',
        status: 'failed',
        output_data: { error_status: 529 },
      })
    ).toMatchObject({ failureKind: 'model_overloaded', errorStatus: 529 });
  });

  it('prefers the selected real model and never retries the unavailable model', () => {
    const models = [
      { id: 'model-a', label: 'Model A' },
      { id: 'model-b', label: 'Model B' },
      { id: 'model-c', label: 'Model C' },
    ];

    expect(chooseSynonBiomedRecoveryModel('model-a', 'model-c', models)).toEqual({
      id: 'model-c',
      label: 'Model C',
    });
    expect(chooseSynonBiomedRecoveryModel('model-a', 'model-a', models)).toEqual({
      id: 'model-b',
      label: 'Model B',
    });
    expect(chooseSynonBiomedRecoveryModel('model-a', 'model-a', [models[0]])).toBeNull();
  });

  it('normalizes an awaiting plan and its artifact document into reviewable phases', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-plan',
      status: 'awaiting_plan_approval',
      output_data: {
        plan_artifact_id: 'artifact-plan',
        plan_version_id: 'version-plan',
        plan_approved: false,
      },
    });
    expect(snapshot.taskPlan).toEqual({ artifactId: 'artifact-plan', versionId: 'version-plan' });
    expect(snapshot.planApproval).toEqual({
      artifactId: 'artifact-plan',
      versionId: 'version-plan',
    });
    expect(toSynonAIRuntimeSummary(snapshot)).toMatchObject({
      state: 'waiting_approval',
      can_send_message: true,
      is_processing: false,
      pending_confirmations: 1,
    });

    expect(
      normalizeSynonBiomedPlanDocument({
        version: 3,
        task_summary: 'Characterize STAT6 binding pockets',
        phases: [
          {
            id: 'phase-0',
            name: 'Plan',
            steps: [
              {
                id: 'phase-direct',
                title: 'Confirm inclusion criteria',
                description: 'Freeze the cohort definition.',
                status: 'in_progress',
              },
            ],
            delegations: [
              {
                id: 'delegation-structure',
                name: 'Structure review',
                agent_name: 'OPERON',
                steps: [
                  { title: 'Fetch structures', description: 'Load curated PDB structures.' },
                  { title: 'Rank pockets', description: 'Score and compare ligand pockets.' },
                ],
              },
            ],
          },
        ],
        feasibility: { confidence: 'high', rationale: 'Required public structures are available.' },
      })
    ).toEqual({
      version: 3,
      taskSummary: 'Characterize STAT6 binding pockets',
      phases: [
        {
          id: 'phase-0',
          name: 'Plan',
          delegations: [
            {
              id: 'delegation-structure',
              name: 'Structure review',
              agentName: 'OPERON',
              steps: [
                {
                  id: 'delegation-structure-0',
                  title: 'Fetch structures',
                  description: 'Load curated PDB structures.',
                },
                {
                  id: 'delegation-structure-1',
                  title: 'Rank pockets',
                  description: 'Score and compare ligand pockets.',
                },
              ],
            },
          ],
          steps: [
            {
              id: 'phase-direct',
              title: 'Confirm inclusion criteria',
              description: 'Freeze the cohort definition.',
              status: 'in_progress',
            },
          ],
        },
      ],
      feasibility: { confidence: 'high', rationale: 'Required public structures are available.' },
    });
  });

  it.each(['processing', 'completed'])(
    'retains the current task plan for %s frames without approval actions',
    (status) => {
      const snapshot = normalizeSynonBiomedRuntimeSnapshot({
        id: `frame-${status}`,
        status,
        output_data: {
          plan_artifact_id: `artifact-${status}`,
          plan_version_id: `version-${status}`,
        },
      });

      expect(snapshot.taskPlan).toEqual({
        artifactId: `artifact-${status}`,
        versionId: `version-${status}`,
      });
      expect(snapshot.planApproval).toBeNull();
    }
  );

  it('treats backend pending input requests as explicit runtime confirmations', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-waiting',
      root_frame_id: 'frame-waiting',
      status: 'processing',
      runtime_stage: 'starting',
      status_description: 'Checking API keys for NVIDIA NIMs',
      output_data: {
        pending_input_requests: [
          {
            requestId: 'request-1',
            tool_id: 'call-1',
            kind: 'local_exec',
            tool: 'python',
            code: 'print("NGC_API_KEY")',
            environment: 'python',
            mode: 'live',
            target: 'files.rcsb.org',
            rememberable: false,
          },
        ],
      },
    });

    expect(snapshot.pendingInputRequests).toEqual([
      {
        requestId: 'request-1',
        toolId: 'call-1',
        kind: 'local_exec',
        tool: 'python',
        code: 'print("NGC_API_KEY")',
        description: null,
        target: 'files.rcsb.org',
        environment: 'python',
        mode: 'live',
        rememberable: false,
        questions: [],
      },
    ]);
    expect(toSynonAIRuntimeSummary(snapshot)).toMatchObject({
      state: 'waiting_confirmation',
      can_send_message: false,
      is_processing: true,
      pending_confirmations: 1,
      turn_id: 'frame-waiting',
    });
  });

  it('preserves ask_user questions, option rationale, multi-select, and SMILES metadata', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-ask-user',
      status: 'awaiting_user_response',
      output_data: {
        pending_input_requests: [
          {
            requestId: 'request-ask-1',
            tool_id: 'toolu_ask_1',
            kind: 'ask_user',
            questions: [
              {
                header: 'Lead selection',
                question: 'Which compound should be prioritized?',
                multi_select: true,
                options: [
                  {
                    label: 'Compound A',
                    description: 'Best potency profile',
                    pros: 'Low nanomolar activity',
                    cons: 'Moderate clearance',
                    metadata: { smiles: 'CCO' },
                  },
                  { label: '', description: 'invalid option' },
                ],
              },
            ],
          },
        ],
      },
    });

    expect(snapshot.pendingInputRequests[0]).toMatchObject({
      requestId: 'request-ask-1',
      toolId: 'toolu_ask_1',
      questions: [
        {
          header: 'Lead selection',
          question: 'Which compound should be prioritized?',
          multiSelect: true,
          options: [
            {
              label: 'Compound A',
              description: 'Best potency profile',
              pros: 'Low nanomolar activity',
              cons: 'Moderate clearance',
              smiles: 'CCO',
            },
          ],
        },
      ],
    });
  });

  it('normalizes the canonical ask tool kind and tool_name fields', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-ask-user-tool-name',
      status: 'awaiting_user_response',
      output_data: {
        pending_input_requests: [
          {
            requestId: 'request-ask-tool-name',
            tool_id: 'toolu_ask_tool_name',
            kind: 'ask',
            tool_name: 'ask_user',
            questions: [],
          },
        ],
      },
    });

    expect(snapshot.pendingInputRequests[0]).toMatchObject({ kind: 'ask', tool: 'ask_user' });
  });

  it('normalizes running state and execution records without losing scientific provenance fields', () => {
    const snapshot = normalizeSynonBiomedRuntimeSnapshot({
      id: 'frame-running',
      status: 'processing',
      status_description: 'Running protein structure analysis',
    });
    expect(snapshot.canCancel).toBe(true);
    expect(snapshot.canResume).toBe(false);
    expect(toSynonAIRuntimeSummary(snapshot)).toMatchObject({
      state: 'running',
      can_send_message: false,
      is_processing: true,
      turn_id: 'frame-running',
    });

    expect(
      normalizeSynonBiomedExecutionLog([
        {
          id: 'exec-1',
          cell_index: 3,
          language: 'python',
          source: 'print("STAT6")',
          stdout: 'STAT6\n',
          stderr: '',
          exit_status: 'ok',
          kernel_kind: 'operon',
          conda_env: 'synon',
          files_written: ['/workspace/stat6.csv'],
          executed_at: '2026-07-11T01:00:00.000Z',
        },
      ])
    ).toEqual([
      {
        kind: 'cell',
        id: 'exec-1',
        cellIndex: 3,
        language: 'python',
        source: 'print("STAT6")',
        stdout: 'STAT6\n',
        stderr: '',
        exitStatus: 'ok',
        errorLine: null,
        kernelKind: 'operon',
        environment: 'synon',
        filesWritten: ['/workspace/stat6.csv'],
        at: '2026-07-11T01:00:00.000Z',
      },
    ]);
  });
});
