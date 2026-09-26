import type { TChatConversationStatus, TConversationRuntimeSummary } from '@/common/config/storage';

export type SynonBiomedTaskMetricScope = {
  inputTokens: number | null;
  outputTokens: number | null;
  cacheReadTokens: number | null;
  cacheWriteTokens: number | null;
  totalTokens: number | null;
  modelCallCount: number | null;
  toolCallCount: number | null;
};

export type SynonBiomedTaskRunMetrics = {
  /** Cumulative durable metrics for every logical input revision in this frame task. */
  total: SynonBiomedTaskMetricScope;
  /** Durable metrics for the latest logical input revision, including its retries. */
  latest: SynonBiomedTaskMetricScope;
};

export type SynonBiomedRuntimeSnapshot = {
  frameId: string;
  rootFrameId: string;
  projectId: string | null;
  agentName: string | null;
  isHidden: boolean;
  status: string;
  statusDescription: string | null;
  error: string | null;
  failureReason: string | null;
  failureKind: SynonBiomedRuntimeFailureKind;
  errorStatus: number | null;
  modelId: string | null;
  /** Durable metrics split into whole-task totals and the latest logical input revision. */
  taskMetrics: SynonBiomedTaskRunMetrics | null;
  createdAt: string | null;
  completedAt: string | null;
  updatedAt: string | null;
  runtimeElapsedMs: number | null;
  runtimeObservedAt: string | null;
  runtimeStartedAt: string | null;
  runtimeFinishedAt: string | null;
  runtimeActive: boolean | null;
  runtimeTaskElapsedMs: number | null;
  runtimeTaskObservedAt: string | null;
  runtimeTaskStartedAt: string | null;
  runtimeTaskFinishedAt: string | null;
  runtimeTaskActive: boolean | null;
  runtimeAttempt: number | null;
  runtimeInputRevision: number | null;
  runtimeStage: SynonBiomedRuntimeStage;
  reviewStatus: string | null;
  reviewKind: string | null;
  reviewProfile: string | null;
  reviewFrameId: string | null;
  reviewDescription: string | null;
  reviewTrigger: 'auto' | 'manual' | null;
  reviewVerdict: SynonBiomedReviewVerdict;
  reviewIssueCount: number;
  reviewBlockingIssueCount: number;
  canCancel: boolean;
  canResume: boolean;
  pendingInputRequests: SynonBiomedPendingInputRequest[];
  taskPlan: SynonBiomedPlanReference | null;
  planApproval: SynonBiomedPlanApproval | null;
};

export type SynonBiomedReviewVerdict = 'pass' | 'pass_with_warnings' | 'revise' | null;

export type SynonBiomedRuntimeStage =
  | 'starting'
  | 'finalizing'
  | 'reviewing'
  | 'scientific_reviewing'
  | 'review_completed'
  | 'review_failed'
  | null;

export function doesSynonBiomedRuntimeSnapshotNeedActiveRefresh(snapshot: SynonBiomedRuntimeSnapshot | null): boolean {
  if (!snapshot) return false;
  if (snapshot.status === 'failed' || snapshot.status === 'cancelled') return false;
  return (
    snapshot.canCancel ||
    snapshot.status === 'cancelling' ||
    snapshot.pendingInputRequests.length > 0 ||
    snapshot.runtimeStage === 'finalizing' ||
    snapshot.runtimeStage === 'reviewing' ||
    snapshot.runtimeStage === 'scientific_reviewing'
  );
}

export type SynonBiomedRuntimeFailureKind =
  | 'not_found'
  | 'unauthorized'
  | 'rate_limited'
  | 'quota_exhausted'
  | 'invalid_request'
  | 'transient'
  | 'image_build_failed'
  | 'network_denied'
  | 'network_bridge_down'
  | 'ownership_mismatch'
  | 'provider_degraded'
  | 'result_rejected'
  | 'safety_refusal'
  | 'model_not_found'
  | 'model_overloaded'
  | 'service_interrupted'
  | 'generic'
  | null;

export type SynonBiomedRuntimeModelOption = {
  id: string;
  label: string;
};

export type SynonBiomedPlanReference = {
  artifactId: string;
  versionId: string | null;
  revisionNumber?: number | null;
  revisionCount?: number | null;
  contentVersion?: number | null;
  generatedAt?: string | null;
};

export type SynonBiomedPlanApproval = SynonBiomedPlanReference;

export type SynonBiomedPlanStep = {
  id: string;
  title: string;
  description: string;
  status?: 'pending' | 'in_progress' | 'completed' | 'blocked' | 'failed' | 'cancelled' | 'unknown';
};

export type SynonBiomedPlanPhase = {
  id: string;
  name: string;
  delegations: SynonBiomedPlanDelegation[];
  steps: SynonBiomedPlanStep[];
};

export type SynonBiomedPlanDelegation = {
  id: string;
  name: string;
  agentName: string | null;
  steps: SynonBiomedPlanStep[];
};

export type SynonBiomedPlanDocument = {
  version: number;
  taskSummary: string;
  phases: SynonBiomedPlanPhase[];
  feasibility: { confidence: string | null; rationale: string | null } | null;
};

export type SynonBiomedPendingInputRequest = {
  requestId: string;
  toolId: string | null;
  kind: string;
  tool: string | null;
  code: string | null;
  description: string | null;
  target?: string | null;
  environment: string | null;
  mode: string | null;
  rememberable?: boolean;
  questions: SynonBiomedAskUserQuestion[];
};

export type SynonBiomedApprovalScope = 'once' | 'conversation' | 'project' | 'always';

export type SynonBiomedAskUserOption = {
  label: string;
  description: string | null;
  pros: string | null;
  cons: string | null;
  smiles: string | null;
  recommended?: boolean;
  readinessStatus?: string | null;
};

export type SynonBiomedAskUserQuestion = {
  header: string | null;
  question: string;
  multiSelect: boolean;
  options: SynonBiomedAskUserOption[];
  stageProgress?: SynonBiomedPlanStageProgress | null;
};

export type SynonBiomedPlanStageProgress = {
  planVersionId: string;
  completedCount: number;
  remainingCount: number;
  completedSteps: { id: string; title: string; status: string }[];
  remainingSteps: { id: string; title: string; status: string }[];
};

const AGENT_CHOICE_LABEL_FRAGMENTS = [
  'choose for me',
  'no preference',
  'not sure',
  'unsure',
  'let me decide',
  'you decide',
  'agent decide',
  'skip',
  "i'll figure",
  'figure it out',
];

export function isSynonBiomedAgentChoiceOption(label: string): boolean {
  const normalized = label.trim().toLowerCase();
  return AGENT_CHOICE_LABEL_FRAGMENTS.some((fragment) => normalized.includes(fragment));
}

export type SynonBiomedExecutionCell = {
  kind: 'cell';
  id: string;
  cellIndex: number;
  language: string;
  source: string;
  stdout: string;
  stderr: string;
  exitStatus: string;
  errorLine: number | null;
  kernelKind: string | null;
  environment: string | null;
  filesWritten: string[];
  at: string | null;
};

export type SynonBiomedExecutionEvent = {
  kind: 'save' | 'app_tool' | 'user_edit';
  id: string;
  label: string | null;
  actor: string | null;
  detail: string | null;
  at: string | null;
};

export type SynonBiomedExecutionRecord = SynonBiomedExecutionCell | SynonBiomedExecutionEvent;

type RecordValue = Record<string, unknown>;

const PROCESSING_STATES = new Set(['processing', 'running', 'executing', 'in_progress', 'in-progress']);
const STARTING_STATES = new Set(['queued', 'pending', 'created', 'initializing']);
const WAITING_STATES = new Set(['awaiting_user_response', 'awaiting_plan_approval', 'needs_input', 'awaiting_input']);
const RESUMABLE_STATES = new Set(['failed', 'cancelled', 'canceled', 'paused']);
const CLOSED_RUNTIME_FAILURE_KINDS = new Set<SynonBiomedRuntimeFailureKind>([
  'not_found',
  'unauthorized',
  'rate_limited',
  'quota_exhausted',
  'invalid_request',
  'transient',
  'image_build_failed',
  'network_denied',
  'network_bridge_down',
  'ownership_mismatch',
  'provider_degraded',
  'result_rejected',
]);

const RESULT_REJECTED_REASON_CODES = new Set([
  'artifact_reference_correction_required',
  'completion_review_correction_required',
  'required_tool_choice_unsatisfied',
  'response_language_mismatch',
  'visual_artifact_validation_required',
  'visual_media_unsupported',
]);

/**
 * The runtime returns to `awaiting_input` after publishing a completed turn so
 * the same conversation can accept a follow-up. A durable task-finished fence,
 * with no pending request and no active task clock, distinguishes that ready
 * state from a genuine repair/approval boundary.
 */
export function isSynonBiomedCompletedTaskAwaitingFollowup(snapshot: SynonBiomedRuntimeSnapshot): boolean {
  return (
    snapshot.status === 'awaiting_input' &&
    snapshot.pendingInputRequests.length === 0 &&
    snapshot.runtimeTaskFinishedAt !== null &&
    snapshot.runtimeTaskActive !== true
  );
}

/**
 * Waiting labels are user-action claims, so they require durable action data.
 * A bare legacy frame status is only a transport hint and must not fabricate an
 * AskUser or approval boundary when the corresponding payload is absent.
 */
export function hasSynonBiomedPendingUserAction(snapshot: SynonBiomedRuntimeSnapshot): boolean {
  return snapshot.pendingInputRequests.length > 0;
}

export function isSynonBiomedUnconfirmedWaitingStatus(snapshot: SynonBiomedRuntimeSnapshot): boolean {
  return (
    WAITING_STATES.has(snapshot.status) &&
    !hasSynonBiomedPendingUserAction(snapshot) &&
    snapshot.planApproval === null &&
    !isSynonBiomedCompletedTaskAwaitingFollowup(snapshot)
  );
}

export type SynonBiomedTaskAuthorityPhase =
  | 'queued'
  | 'starting'
  | 'finalizing'
  | 'running'
  | 'reviewing'
  | 'scientific_reviewing'
  | 'waiting_input'
  | 'waiting_approval'
  | 'paused'
  | 'cancelling'
  | 'completed'
  | 'failed'
  | 'cancelled';

export type SynonBiomedTaskAuthorityProjection = {
  phase: SynonBiomedTaskAuthorityPhase;
  taskCompleted: boolean;
};

const TASK_PHASE_BY_FRAME_STATUS = new Map<string, SynonBiomedTaskAuthorityPhase>([
  ['queued', 'queued'],
  ['pending', 'queued'],
  ['created', 'queued'],
  ['starting', 'starting'],
  ['initializing', 'starting'],
  ['finalizing', 'finalizing'],
  ['processing', 'running'],
  ['running', 'running'],
  ['executing', 'running'],
  ['in_progress', 'running'],
  ['in-progress', 'running'],
  ['paused', 'paused'],
  ['cancelling', 'cancelling'],
  ['completed', 'completed'],
  ['finished', 'completed'],
  ['succeeded', 'completed'],
  ['failed', 'failed'],
  ['cancelled', 'cancelled'],
  ['canceled', 'cancelled'],
]);

/** The sole frontend projection from durable runtime facts to a task phase. */
export function projectSynonBiomedTaskAuthority(
  snapshot: SynonBiomedRuntimeSnapshot
): SynonBiomedTaskAuthorityProjection | null {
  const completedAwaitingFollowup = isSynonBiomedCompletedTaskAwaitingFollowup(snapshot);
  let phase = completedAwaitingFollowup ? 'completed' : TASK_PHASE_BY_FRAME_STATUS.get(snapshot.status);
  if (snapshot.planApproval) {
    phase = 'waiting_approval';
  } else if (hasSynonBiomedPendingUserAction(snapshot)) {
    phase = 'waiting_input';
  } else if (isSynonBiomedUnconfirmedWaitingStatus(snapshot)) {
    phase = 'running';
  }
  if (!phase) return null;
  const taskCompleted = phase === 'completed';
  const runtimeStage = snapshot.runtimeStage;
  const activeReviewStage =
    snapshot.reviewStatus?.trim().toLowerCase() === 'processing' &&
    (runtimeStage === 'reviewing' || runtimeStage === 'scientific_reviewing');
  const activeRuntimeStage =
    runtimeStage === 'starting' ||
    runtimeStage === 'finalizing' ||
    runtimeStage === 'reviewing' ||
    runtimeStage === 'scientific_reviewing';
  if (
    phase !== 'waiting_input' &&
    phase !== 'waiting_approval' &&
    phase !== 'failed' &&
    phase !== 'cancelled' &&
    (activeReviewStage || (phase !== 'completed' && activeRuntimeStage))
  ) {
    phase = runtimeStage;
  }
  return { phase, taskCompleted };
}

export function normalizeSynonBiomedRuntimeSnapshot(value: unknown): SynonBiomedRuntimeSnapshot {
  if (!isRecord(value)) throw new Error('Synon Biomed frame response is invalid');
  const frameId = stringValue(value.id) || stringValue(value.frame_id);
  if (!frameId) throw new Error('Synon Biomed frame response is missing an id');
  const status = (stringValue(value.status) || 'unknown').toLowerCase();
  const output = isRecord(value.output_data) ? value.output_data : null;
  const context = isRecord(value.context_data) ? value.context_data : null;
  const originalInput = context && isRecord(context._original_input) ? context._original_input : null;
  const statusDescription = nullableString(value.status_description);
  const stopReason = output ? stringValue(output.stop_reason).toLowerCase() : '';
  const errorKind = output ? stringValue(output.error_kind).toLowerCase() : '';
  const closedFailureKind = normalizeClosedRuntimeFailureKind(
    (output ? stringValue(output.failure_kind) : '') ||
      stringValue(value.runtime_failure_kind) ||
      stringValue(value.runtime_failure_reason)
  );
  const outputError = output ? nullableString(output.error) : null;
  const error =
    status === 'failed' && isGenericRuntimeError(outputError) && statusDescription
      ? statusDescription
      : (outputError ?? (status === 'failed' ? statusDescription : null));
  const errorStatus = output ? finiteNumber(output.error_status) : null;
  const failureKind: SynonBiomedRuntimeFailureKind =
    status !== 'failed'
      ? null
      : closedFailureKind
        ? closedFailureKind
        : stopReason === 'refusal'
          ? 'safety_refusal'
          : errorKind === 'model_not_found'
            ? 'model_not_found'
            : errorStatus === 529
              ? 'model_overloaded'
              : isDaemonRestartInterruption(error)
                ? 'service_interrupted'
                : 'generic';
  const pendingInputRequests = normalizePendingInputRequests(output?.pending_input_requests);
  const planArtifactId = output ? stringValue(output.plan_artifact_id) : '';
  const planRevisionNumber = output ? nonNegativeInteger(output.plan_revision_number) : null;
  const planRevisionCount = output ? nonNegativeInteger(output.plan_revision_count) : null;
  const planContentVersion = output ? nonNegativeInteger(output.plan_content_version) : null;
  const planGeneratedAt = output ? timestampValue(output.plan_generated_at) : null;
  const taskPlan = planArtifactId
    ? {
        artifactId: planArtifactId,
        versionId: output ? nullableString(output.plan_version_id) : null,
        ...(planRevisionNumber !== null ? { revisionNumber: planRevisionNumber } : {}),
        ...(planRevisionCount !== null ? { revisionCount: planRevisionCount } : {}),
        ...(planContentVersion !== null ? { contentVersion: planContentVersion } : {}),
        ...(planGeneratedAt !== null ? { generatedAt: planGeneratedAt } : {}),
      }
    : null;
  const planApproval = status === 'awaiting_plan_approval' && taskPlan ? taskPlan : null;
  const runtimeStage = normalizeRuntimeStage(value.runtime_stage);
  const reviewTriggerValue = stringValue(value.runtime_review_trigger).toLowerCase();
  return {
    frameId,
    rootFrameId: stringValue(value.root_frame_id) || frameId,
    projectId: nullableString(value.project_id),
    agentName: nullableString(value.agent_name),
    isHidden: value.is_hidden === true,
    status,
    statusDescription,
    error,
    failureReason:
      safeRuntimeReason(value.runtime_failure_reason) ?? safeRuntimeReason(value.runtime_interruption_reason),
    failureKind,
    errorStatus,
    modelId: nullableString(value.model) ?? (originalInput ? nullableString(originalInput.model) : null),
    taskMetrics: normalizeTaskRunMetrics(value),
    createdAt: timestampValue(value.created_at),
    completedAt: timestampValue(value.completed_at),
    updatedAt: timestampValue(value.updated_at),
    runtimeElapsedMs: nonNegativeFiniteNumber(value.runtime_elapsed_ms),
    runtimeObservedAt: timestampValue(value.runtime_observed_at),
    runtimeStartedAt: timestampValue(value.runtime_started_at),
    runtimeFinishedAt: timestampValue(value.runtime_finished_at),
    runtimeActive: typeof value.runtime_active === 'boolean' ? value.runtime_active : null,
    runtimeTaskElapsedMs: nonNegativeFiniteNumber(value.runtime_task_elapsed_ms),
    runtimeTaskObservedAt: timestampValue(value.runtime_task_observed_at),
    runtimeTaskStartedAt: timestampValue(value.runtime_task_started_at),
    runtimeTaskFinishedAt: timestampValue(value.runtime_task_finished_at),
    runtimeTaskActive: typeof value.runtime_task_active === 'boolean' ? value.runtime_task_active : null,
    runtimeAttempt: positiveInteger(value.runtime_attempt),
    runtimeInputRevision: nonNegativeInteger(value.runtime_input_revision),
    runtimeStage,
    reviewStatus: nullableString(value.runtime_review_status)?.toLowerCase() ?? null,
    reviewKind: nullableString(value.runtime_review_kind),
    reviewProfile: nullableString(value.runtime_review_profile),
    reviewFrameId: nullableString(value.runtime_review_frame_id),
    reviewDescription: nullableString(value.runtime_review_description),
    reviewTrigger: reviewTriggerValue === 'manual' || reviewTriggerValue === 'auto' ? reviewTriggerValue : null,
    reviewVerdict: normalizeReviewVerdict(value.runtime_review_verdict),
    reviewIssueCount: nonNegativeInteger(value.runtime_review_issue_count) ?? 0,
    reviewBlockingIssueCount: nonNegativeInteger(value.runtime_review_blocking_issue_count) ?? 0,
    canCancel: PROCESSING_STATES.has(status) || STARTING_STATES.has(status) || WAITING_STATES.has(status),
    canResume: RESUMABLE_STATES.has(status),
    pendingInputRequests,
    taskPlan,
    planApproval,
  };
}

function normalizeClosedRuntimeFailureKind(value: unknown): SynonBiomedRuntimeFailureKind {
  const normalizedValue = stringValue(value).trim().toLowerCase();
  if (RESULT_REJECTED_REASON_CODES.has(normalizedValue)) return 'result_rejected';
  const normalized = normalizedValue as SynonBiomedRuntimeFailureKind;
  return CLOSED_RUNTIME_FAILURE_KINDS.has(normalized) ? normalized : null;
}

function normalizeTaskRunMetrics(value: RecordValue): SynonBiomedTaskRunMetrics | null {
  const latestKeys = [
    'runtime_input_tokens',
    'runtime_output_tokens',
    'runtime_cache_read_tokens',
    'runtime_cache_write_tokens',
    'runtime_total_tokens',
    'runtime_model_call_count',
    'runtime_tool_call_count',
  ];
  const totalKeys = [
    'runtime_task_input_tokens',
    'runtime_task_output_tokens',
    'runtime_task_cache_read_tokens',
    'runtime_task_cache_write_tokens',
    'runtime_task_total_tokens',
    'runtime_task_model_call_count',
    'runtime_task_tool_call_count',
  ];
  if (![...latestKeys, ...totalKeys].some((key) => Object.prototype.hasOwnProperty.call(value, key))) return null;
  return {
    total: normalizeTaskMetricScope(value, 'runtime_task_'),
    latest: normalizeTaskMetricScope(value, 'runtime_'),
  };
}

function normalizeTaskMetricScope(
  value: RecordValue,
  prefix: 'runtime_' | 'runtime_task_'
): SynonBiomedTaskMetricScope {
  return {
    inputTokens: nonNegativeInteger(value[`${prefix}input_tokens`]),
    outputTokens: nonNegativeInteger(value[`${prefix}output_tokens`]),
    cacheReadTokens: nonNegativeInteger(value[`${prefix}cache_read_tokens`]),
    cacheWriteTokens: nonNegativeInteger(value[`${prefix}cache_write_tokens`]),
    totalTokens: nonNegativeInteger(value[`${prefix}total_tokens`]),
    modelCallCount: nonNegativeInteger(value[`${prefix}model_call_count`]),
    toolCallCount: nonNegativeInteger(value[`${prefix}tool_call_count`]),
  };
}

function safeRuntimeReason(value: unknown): string | null {
  const reason = stringValue(value).trim().toLowerCase();
  return /^[a-z0-9_]{1,64}$/.test(reason) ? reason : null;
}

function normalizeReviewVerdict(value: unknown): SynonBiomedReviewVerdict {
  const verdict = stringValue(value).toLowerCase();
  return verdict === 'pass' || verdict === 'pass_with_warnings' || verdict === 'revise' ? verdict : null;
}

function normalizeRuntimeStage(value: unknown): SynonBiomedRuntimeStage {
  const stage = stringValue(value).toLowerCase();
  switch (stage) {
    case 'starting':
    case 'finalizing':
    case 'reviewing':
    case 'scientific_reviewing':
    case 'review_completed':
    case 'review_failed':
      return stage;
    default:
      return null;
  }
}

function isDaemonRestartInterruption(error: string | null): boolean {
  return Boolean(error && /daemon\s+(?:was\s+)?restarted\s+while\s+frame\s+was\s+running/i.test(error));
}

function isGenericRuntimeError(error: string | null): boolean {
  const normalized = error?.trim().toLowerCase() ?? '';
  return normalized === '' || normalized === 'error' || normalized === 'failed' || normalized === 'failure';
}

export function chooseSynonBiomedRecoveryModel(
  unavailableModelId: string | null,
  selectedModelId: string | null,
  availableModels: SynonBiomedRuntimeModelOption[]
): SynonBiomedRuntimeModelOption | null {
  const candidates = availableModels.filter((model) => model.id && model.id !== unavailableModelId);
  if (selectedModelId && selectedModelId !== unavailableModelId) {
    const selected = candidates.find((model) => model.id === selectedModelId);
    if (selected) return selected;
  }
  return candidates[0] ?? null;
}

export function toSynonAIRuntimeSummary(snapshot: SynonBiomedRuntimeSnapshot): TConversationRuntimeSummary {
  const authority = projectSynonBiomedTaskAuthority(snapshot);
  if (!authority) throw new Error('runtime_status_unsupported');
  const waitingApproval = snapshot.planApproval !== null;
  const waitingConfirmation = !waitingApproval && hasSynonBiomedPendingUserAction(snapshot);
  const waitingInput = authority.phase === 'waiting_input' && !waitingConfirmation;
  const starting = authority.phase === 'queued' || authority.phase === 'starting';
  const processing =
    authority.phase === 'running' || authority.phase === 'reviewing' || authority.phase === 'scientific_reviewing';
  const cancelling = authority.phase === 'cancelling';
  const paused = authority.phase === 'paused';
  const activeWork = processing || starting || cancelling;
  const unfinished = activeWork || waitingApproval || waitingConfirmation || waitingInput || paused;
  const terminalError = authority.phase === 'failed' || authority.phase === 'cancelled';
  const state = paused
    ? 'paused'
    : waitingApproval
      ? 'waiting_approval'
      : waitingConfirmation
        ? 'waiting_confirmation'
        : waitingInput
          ? 'waiting_input'
          : cancelling
            ? 'cancelling'
            : processing
              ? 'running'
              : starting
                ? 'starting'
                : 'idle';
  const taskStatus: TChatConversationStatus = paused
    ? 'pending'
    : waitingApproval || waitingConfirmation || waitingInput
      ? 'pending'
      : cancelling
        ? 'running'
        : processing
          ? 'running'
          : starting
            ? 'pending'
            : terminalError
              ? 'error'
              : 'finished';
  return {
    state,
    can_send_message: paused || waitingApproval || waitingInput || !unfinished,
    has_task: unfinished,
    task_status: taskStatus,
    is_processing: activeWork || waitingConfirmation,
    pending_confirmations: waitingConfirmation ? snapshot.pendingInputRequests.length : waitingApproval ? 1 : 0,
    turn_id: unfinished ? snapshot.rootFrameId : null,
  };
}

export function normalizeSynonBiomedPlanDocument(value: unknown): SynonBiomedPlanDocument {
  if (!isRecord(value)) throw new Error('Synon Biomed plan response is invalid');
  const rawPhases = Array.isArray(value.phases) ? value.phases : [];
  const phases = rawPhases.flatMap((rawPhase, phaseIndex) => {
    if (!isRecord(rawPhase)) return [];
    const id = stringValue(rawPhase.id) || `phase-${phaseIndex}`;
    const steps = normalizeSynonBiomedPlanSteps(rawPhase.steps, id);
    const rawDelegations = Array.isArray(rawPhase.delegations) ? rawPhase.delegations : [];
    const delegations = rawDelegations.flatMap((rawDelegation, delegationIndex): SynonBiomedPlanDelegation[] => {
      if (!isRecord(rawDelegation)) return [];
      const delegationId = stringValue(rawDelegation.id) || `${id}-delegation-${delegationIndex}`;
      const delegationSteps = normalizeSynonBiomedPlanSteps(rawDelegation.steps, delegationId);
      return [
        {
          id: delegationId,
          name:
            stringValue(rawDelegation.name) ||
            stringValue(rawDelegation.title) ||
            stringValue(rawDelegation.delegate_name) ||
            `Delegation ${delegationIndex + 1}`,
          agentName: nullableString(rawDelegation.agent_name),
          steps: delegationSteps,
        },
      ];
    });
    return [
      {
        id,
        name: stringValue(rawPhase.name) || stringValue(rawPhase.title) || `Phase ${phaseIndex + 1}`,
        delegations,
        steps,
      },
    ];
  });
  const feasibility = isRecord(value.feasibility)
    ? {
        confidence: nullableString(value.feasibility.confidence),
        rationale: nullableString(value.feasibility.rationale),
      }
    : null;
  return {
    version: finiteNumber(value.version) ?? 1,
    taskSummary: stringValue(value.task_summary) || 'Untitled plan',
    phases,
    feasibility,
  };
}

function normalizeSynonBiomedPlanSteps(value: unknown, parentId: string): SynonBiomedPlanStep[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((rawStep, stepIndex): SynonBiomedPlanStep[] => {
    if (!isRecord(rawStep)) return [];
    const title = stringValue(rawStep.title) || stringValue(rawStep.content);
    if (!title) return [];
    const status = normalizeSynonBiomedPlanStepStatus(rawStep.status);
    return [
      {
        id: stringValue(rawStep.id) || `${parentId}-${stepIndex}`,
        title,
        description: stringValue(rawStep.description),
        ...(status ? { status } : {}),
      },
    ];
  });
}

function normalizeSynonBiomedPlanStepStatus(value: unknown): SynonBiomedPlanStep['status'] | undefined {
  if (typeof value !== 'string' || !value.trim()) return undefined;
  const normalized = value.trim().toLowerCase().replace(/-/gu, '_');
  switch (normalized) {
    case 'pending':
    case 'in_progress':
    case 'completed':
    case 'blocked':
    case 'failed':
    case 'cancelled':
      return normalized;
    case 'canceled':
      return 'cancelled';
    default:
      return 'unknown';
  }
}

function normalizePendingInputRequests(value: unknown): SynonBiomedPendingInputRequest[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item) => {
    if (!isRecord(item)) return [];
    const requestId = stringValue(item.requestId) || stringValue(item.request_id);
    if (!requestId) return [];
    return [
      {
        requestId,
        toolId: nullableString(item.tool_id),
        kind: stringValue(item.kind) || 'approval',
        tool: nullableString(item.tool) ?? nullableString(item.tool_name),
        code: nullableString(item.code) ?? nullableString(item.command),
        description: nullableString(item.description) ?? nullableString(item.message) ?? nullableString(item.request),
        target: nullableString(item.target),
        environment: nullableString(item.environment),
        mode: nullableString(item.mode),
        ...(typeof item.rememberable === 'boolean' ? { rememberable: item.rememberable } : {}),
        questions: normalizeSynonBiomedAskUserQuestions(item.questions),
      },
    ];
  });
}

export function normalizeSynonBiomedAskUserQuestions(value: unknown): SynonBiomedAskUserQuestion[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item) => {
    if (!isRecord(item)) return [];
    const question = stringValue(item.question);
    if (!question) return [];
    const rawOptions = Array.isArray(item.options) ? item.options : [];
    const options = rawOptions.flatMap((option): SynonBiomedAskUserOption[] => {
      if (!isRecord(option)) return [];
      const label = stringValue(option.label);
      if (!label) return [];
      const metadata = isRecord(option.metadata) ? option.metadata : null;
      return [
        {
          label,
          description: nullableString(option.description),
          pros: nullableString(option.pros),
          cons: nullableString(option.cons),
          smiles: metadata ? nullableString(metadata.smiles) : null,
          recommended: metadata?.recommended === true,
          readinessStatus: metadata ? nullableString(metadata.readiness_status) : null,
        },
      ];
    });
    return [
      {
        header: nullableString(item.header),
        question,
        multiSelect: item.multi_select === true || item.multiSelect === true,
        options,
        stageProgress: normalizeSynonBiomedPlanStageProgress(item.stage_progress),
      },
    ];
  });
}

function normalizeSynonBiomedPlanStageProgress(value: unknown): SynonBiomedPlanStageProgress | null {
  if (!isRecord(value) || value.schema !== 'synon.plan_stage_progress.v1') return null;
  const planVersionId = stringValue(value.plan_version_id);
  const completedCount = finiteNumber(value.completed_count);
  const remainingCount = finiteNumber(value.remaining_count);
  if (
    !planVersionId ||
    planVersionId.length > 128 ||
    completedCount === null ||
    remainingCount === null ||
    !Number.isInteger(completedCount) ||
    !Number.isInteger(remainingCount) ||
    completedCount < 1 ||
    remainingCount < 1 ||
    completedCount + remainingCount > 256
  )
    return null;
  const steps = (raw: unknown): SynonBiomedPlanStageProgress['completedSteps'] | null => {
    if (!Array.isArray(raw) || raw.length > 12) return null;
    const parsed = raw.map((item) => {
      if (!isRecord(item)) return null;
      const id = stringValue(item.id);
      const title = stringValue(item.title);
      const status = stringValue(item.status);
      if (!id || id.length > 128 || !title || title.length > 512) return null;
      return { id, title, status };
    });
    return parsed.some((item) => item === null) ? null : (parsed as SynonBiomedPlanStageProgress['completedSteps']);
  };
  const completedSteps = steps(value.completed_steps);
  const remainingSteps = steps(value.remaining_steps);
  if (
    !completedSteps ||
    !remainingSteps ||
    completedSteps.length > completedCount ||
    remainingSteps.length > remainingCount ||
    completedSteps.some((step) => step.status !== 'completed') ||
    remainingSteps.some((step) => step.status === 'completed')
  )
    return null;
  return { planVersionId, completedCount, remainingCount, completedSteps, remainingSteps };
}

export function normalizeSynonBiomedExecutionLog(value: unknown): SynonBiomedExecutionRecord[] {
  if (!Array.isArray(value)) throw new Error('Synon Biomed execution-log response is invalid');
  const records: SynonBiomedExecutionRecord[] = [];
  value.forEach((item, index) => {
    if (!isRecord(item)) return;
    const kind = stringValue(item.kind);
    if (kind === 'save' || kind === 'app_tool' || kind === 'user_edit') {
      records.push(normalizeExecutionEvent(item, index, kind));
      return;
    }
    records.push(normalizeExecutionCell(item, index));
  });
  return records;
}

function normalizeExecutionCell(value: RecordValue, index: number): SynonBiomedExecutionCell {
  const files = Array.isArray(value.files_written)
    ? value.files_written
        .map((item) => (typeof item === 'string' ? item : isRecord(item) ? stringValue(item.path) : ''))
        .filter((path): path is string => Boolean(path))
    : [];
  return {
    kind: 'cell',
    id: stringValue(value.id) || `cell-${index}`,
    cellIndex: finiteNumber(value.cell_index) ?? index,
    language: stringValue(value.language) || 'text',
    source: stringValue(value.source),
    stdout: stringValue(value.stdout),
    stderr: stringValue(value.stderr),
    exitStatus: stringValue(value.exit_status) || 'unknown',
    errorLine: finiteNumber(value.error_lineno),
    kernelKind: nullableString(value.kernel_kind),
    environment: nullableString(value.conda_env),
    filesWritten: files,
    at: nullableString(value.at ?? value.executed_at),
  };
}

function normalizeExecutionEvent(
  value: RecordValue,
  index: number,
  kind: SynonBiomedExecutionEvent['kind']
): SynonBiomedExecutionEvent {
  if (kind === 'save') {
    return {
      kind,
      id: stringValue(value.id) || `save-${index}`,
      label: null,
      actor: nullableString(value.by),
      detail: nullableString(value.branched_from),
      at: nullableString(value.at),
    };
  }
  if (kind === 'app_tool') {
    return {
      kind,
      id: stringValue(value.id) || `tool-${index}`,
      label: nullableString(value.tool),
      actor: null,
      detail: stringifyDetail(value.args),
      at: nullableString(value.at),
    };
  }
  return {
    kind,
    id: stringValue(value.id) || `edit-${index}`,
    label: null,
    actor: null,
    detail: stringifyDetail(value.diff),
    at: nullableString(value.at),
  };
}

function stringifyDetail(value: unknown): string | null {
  if (value == null) return null;
  if (typeof value === 'string') return value;
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

function isRecord(value: unknown): value is RecordValue {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function nullableString(value: unknown): string | null {
  return typeof value === 'string' && value.length > 0 ? value : null;
}

const RFC3339_TIMESTAMP = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?(?:Z|[+-](\d{2}):(\d{2}))$/;

function timestampValue(value: unknown): string | null {
  const timestamp = nullableString(value);
  if (!timestamp) return null;
  const match = RFC3339_TIMESTAMP.exec(timestamp);
  if (!match) return null;
  const [, yearValue, monthValue, dayValue, hourValue, minuteValue, secondValue, offsetHour, offsetMinute] = match;
  const year = Number(yearValue);
  const month = Number(monthValue);
  const day = Number(dayValue);
  const validCalendarDay =
    month >= 1 && month <= 12 && day >= 1 && day <= new Date(Date.UTC(year, month, 0)).getUTCDate();
  const validClock = Number(hourValue) <= 23 && Number(minuteValue) <= 59 && Number(secondValue) <= 59;
  const validOffset = offsetHour === undefined || (Number(offsetHour) <= 23 && Number(offsetMinute) <= 59);
  return validCalendarDay && validClock && validOffset && Number.isFinite(Date.parse(timestamp)) ? timestamp : null;
}

function finiteNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null;
}

function nonNegativeFiniteNumber(value: unknown): number | null {
  const number = finiteNumber(value);
  return number !== null && number >= 0 ? number : null;
}

function positiveInteger(value: unknown): number | null {
  const number = finiteNumber(value);
  return number !== null && Number.isInteger(number) && number > 0 ? number : null;
}

function nonNegativeInteger(value: unknown): number | null {
  const number = finiteNumber(value);
  return number !== null && Number.isInteger(number) && number >= 0 ? number : null;
}
