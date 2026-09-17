import {
  normalizeSynonBiomedExecutionLog,
  normalizeSynonBiomedPlanDocument,
  normalizeSynonBiomedRuntimeSnapshot,
  toSynonAIRuntimeSummary,
  type SynonBiomedExecutionRecord,
  type SynonBiomedApprovalScope,
  type SynonBiomedPendingInputRequest,
  type SynonBiomedPlanDocument,
  type SynonBiomedRuntimeSnapshot,
} from '@/renderer/components/synonBiomed/runtime/runtimeOperationsModel';
import type { TConversationRuntimeSummary } from '@/common/config/storage';
import { ipcBridge } from '@/common';
import type { IRuntimeStatusEvent } from '@/common/adapter/ipcBridge';
import {
  requestSynonBiomedJson as requestJson,
  type SynonBiomedGatewayOptions as SynonBiomedRuntimeGatewayOptions,
} from './synonBiomedHttp';
import { normalizeSynonBiomedConversationBranches } from './synonBiomedConversationBranches';
import { invalidateSynonBiomedFrameReads, loadCachedSynonBiomedFrame } from './synonBiomedFrameReads';

export type { SynonBiomedGatewayOptions as SynonBiomedRuntimeGatewayOptions } from './synonBiomedHttp';

export type SynonBiomedAskUserResponse = {
  action: 'answer' | 'decide_for_me' | 'discuss' | 'cancel';
  answers?: Record<string, string>;
  message?: string;
};

export type SynonBiomedAskUserForkResult = {
  rootFrameId: string;
  branchId: string;
  generation: number;
};

export type ForkSynonBiomedAtAskUserAnswerInput = {
  rootFrameId: string;
  sourceFrameId: string;
  sourceBranchId: string;
  toolUseId: string;
  response: SynonBiomedAskUserResponse;
  clientMutationId: string;
};

export type SynonBiomedResumeInput = {
  model?: string;
};

export type SynonBiomedExecutionLogPage = {
  records: SynonBiomedExecutionRecord[];
  nextBefore: string | null;
  total: number;
};

export const SYNON_BIOMED_EXECUTION_LOG_PAGE_SIZE = 200;

export type SynonBiomedRuntimeInvalidation = Pick<IRuntimeStatusEvent, 'terminal_status'>;

type SynonBiomedRuntimeInvalidationListener = (event?: SynonBiomedRuntimeInvalidation) => void;

const localRuntimeInvalidationListeners = new Map<string, Set<SynonBiomedRuntimeInvalidationListener>>();

export function notifySynonBiomedRuntimeInvalidation(frameId: string, event?: SynonBiomedRuntimeInvalidation): void {
  const normalizedFrameId = frameId.trim();
  if (!normalizedFrameId) return;
  invalidateSynonBiomedFrameReads(normalizedFrameId);
  for (const listener of localRuntimeInvalidationListeners.get(normalizedFrameId) ?? []) listener(event);
}

export function subscribeSynonBiomedRuntimeInvalidation(
  frameId: string,
  onInvalidate: (event?: SynonBiomedRuntimeInvalidation) => void
): () => void {
  const normalizedFrameId = frameId.trim();
  const invalidate = (event?: SynonBiomedRuntimeInvalidation) => {
    invalidateSynonBiomedFrameReads(normalizedFrameId);
    onInvalidate(event);
  };
  let localListeners = localRuntimeInvalidationListeners.get(normalizedFrameId);
  if (!localListeners) {
    localListeners = new Set();
    localRuntimeInvalidationListeners.set(normalizedFrameId, localListeners);
  }
  localListeners.add(onInvalidate);
  const releaseStatus = ipcBridge.runtime.statusChanged.on((event) => {
    if (event.scope.kind === 'conversation' && event.scope.id === normalizedFrameId) invalidate(event);
  });
  const invalidateConfirmation = (event: { conversation_id: string }) => {
    if (event.conversation_id === normalizedFrameId) invalidate();
  };
  const releaseConfirmationAdd = ipcBridge.conversation.confirmation.add.on(invalidateConfirmation);
  const releaseConfirmationUpdate = ipcBridge.conversation.confirmation.update.on(invalidateConfirmation);
  const releaseConfirmationRemove = ipcBridge.conversation.confirmation.remove.on(invalidateConfirmation);
  const releaseReconnect = ipcBridge.realtime.reconnected.on(() => invalidate());
  let active = true;
  return () => {
    if (!active) return;
    active = false;
    releaseReconnect();
    releaseConfirmationRemove();
    releaseConfirmationUpdate();
    releaseConfirmationAdd();
    releaseStatus();
    localListeners?.delete(onInvalidate);
    if (localListeners?.size === 0) localRuntimeInvalidationListeners.delete(normalizedFrameId);
  };
}

export async function loadSynonBiomedRuntimeSnapshot(
  frameId: string,
  options: SynonBiomedRuntimeGatewayOptions = {}
): Promise<SynonBiomedRuntimeSnapshot> {
  const payload = await loadCachedSynonBiomedFrame(frameId, options);
  const snapshot = normalizeSynonBiomedRuntimeSnapshot(payload);
  if (snapshot.frameId !== frameId) throw new Error('runtime_snapshot_owner_mismatch');
  return snapshot;
}

export async function loadSynonBiomedExecutionLogPage(
  frameId: string,
  before: string | null = null,
  options: SynonBiomedRuntimeGatewayOptions = {}
): Promise<SynonBiomedExecutionLogPage> {
  const query = new URLSearchParams({ limit: String(SYNON_BIOMED_EXECUTION_LOG_PAGE_SIZE) });
  if (before) query.set('before', before);
  const payload = await requestJson<unknown>(
    `/api/frames/${encodeURIComponent(frameId)}/execution-log?${query.toString()}`,
    {},
    options
  );
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
    throw new Error('Synon Biomed execution-log page response is invalid');
  }
  const record = payload as Record<string, unknown>;
  if (!Array.isArray(record.records) || !Number.isSafeInteger(record.total) || Number(record.total) < 0) {
    throw new Error('Synon Biomed execution-log page response is invalid');
  }
  const nextBefore = record.next_before;
  if (nextBefore !== undefined && typeof nextBefore !== 'string') {
    throw new Error('Synon Biomed execution-log page cursor is invalid');
  }
  return {
    records: normalizeSynonBiomedExecutionLog(record.records),
    nextBefore: typeof nextBefore === 'string' && nextBefore ? nextBefore : null,
    total: Number(record.total),
  };
}

export async function loadSynonBiomedPlanDocument(
  artifactId: string,
  options: SynonBiomedRuntimeGatewayOptions = {}
): Promise<SynonBiomedPlanDocument> {
  const payload = await requestJson<unknown>(`/api/artifacts/${encodeURIComponent(artifactId)}`, {}, options);
  return normalizeSynonBiomedPlanDocument(payload);
}

export async function resumeSynonBiomedFrame(
  frameId: string,
  input: SynonBiomedResumeInput = {},
  options: SynonBiomedRuntimeGatewayOptions = {}
): Promise<{ snapshot: SynonBiomedRuntimeSnapshot; runtime: TConversationRuntimeSummary }> {
  await requestJson<unknown>(
    `/api/frames/${encodeURIComponent(frameId)}/resume`,
    {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(input),
    },
    options
  );
  invalidateSynonBiomedFrameReads(frameId);
  const snapshot = await loadSynonBiomedRuntimeSnapshot(frameId, options);
  return { snapshot, runtime: toSynonAIRuntimeSummary(snapshot) };
}

export async function resolveSynonBiomedInputRequest(
  frameId: string,
  request: SynonBiomedPendingInputRequest,
  decision: 'allow' | 'deny',
  scope: SynonBiomedApprovalScope = 'once',
  options: SynonBiomedRuntimeGatewayOptions = {}
): Promise<{ snapshot: SynonBiomedRuntimeSnapshot; runtime: TConversationRuntimeSummary }> {
  const approved = decision === 'allow';
  await requestJson<unknown>(
    `/api/frames/${encodeURIComponent(frameId)}/resolve-input`,
    {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        responses: [
          {
            requestId: request.requestId,
            ...(request.toolId ? { tool_id: request.toolId } : {}),
            approved,
            action: decision,
            ...(approved && scope !== 'once' ? { scope } : {}),
            ...(approved && (request.mode === 'ro' || request.mode === 'rw') ? { mode: request.mode } : {}),
          },
        ],
      }),
    },
    options
  );
  invalidateSynonBiomedFrameReads(frameId);
  const snapshot = await loadSynonBiomedRuntimeSnapshot(frameId, options);
  return { snapshot, runtime: toSynonAIRuntimeSummary(snapshot) };
}

export async function resolveSynonBiomedAskUserRequest(
  frameId: string,
  request: SynonBiomedPendingInputRequest,
  response: SynonBiomedAskUserResponse,
  options: SynonBiomedRuntimeGatewayOptions = {}
): Promise<{ snapshot: SynonBiomedRuntimeSnapshot; runtime: TConversationRuntimeSummary }> {
  await requestJson<unknown>(
    `/api/frames/${encodeURIComponent(frameId)}/resolve-input`,
    {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        responses: [
          {
            requestId: request.requestId,
            ...(request.toolId ? { tool_id: request.toolId } : {}),
            approved: response.action !== 'cancel',
            action: response.action,
            ...(response.answers ? { answers: response.answers } : {}),
            ...(response.message ? { message: response.message } : {}),
          },
        ],
      }),
    },
    options
  );
  invalidateSynonBiomedFrameReads(frameId);
  const snapshot = await loadSynonBiomedRuntimeSnapshot(frameId, options);
  return { snapshot, runtime: toSynonAIRuntimeSummary(snapshot) };
}

export async function forkSynonBiomedAtAskUserAnswer(
  input: ForkSynonBiomedAtAskUserAnswerInput,
  options: SynonBiomedRuntimeGatewayOptions = {}
): Promise<SynonBiomedAskUserForkResult> {
  const { rootFrameId, sourceFrameId, sourceBranchId, toolUseId, response } = input;
  if (sourceFrameId !== rootFrameId) throw new Error('ask_user_branch_source_must_use_root_conversation');
  if (!/^br_[0-9a-f]{8}$/.test(sourceBranchId)) throw new Error('AskUser source branch is unavailable');
  const clientMutationId = input.clientMutationId.trim();
  if (!clientMutationId) throw new Error('AskUser branch mutation identity is required');
  const branchState = normalizeSynonBiomedConversationBranches(
    rootFrameId,
    await requestJson<unknown>(`/api/frames/${encodeURIComponent(rootFrameId)}/branches`, {}, options)
  );
  if (!branchState.branches.some((branch) => branch.id === sourceBranchId)) {
    throw new Error('AskUser source branch is unavailable');
  }
  const payload = await requestJson<unknown>(
    `/api/frames/${encodeURIComponent(rootFrameId)}/fork-at-answer`,
    {
      method: 'POST',
      headers: { 'content-type': 'application/json', 'Idempotency-Key': clientMutationId },
      body: JSON.stringify({
        tool_use_id: toolUseId,
        response,
        source_branch_id: sourceBranchId,
        expected_branch_id: branchState.activeBranchId,
        expected_generation: branchState.generation,
        client_mutation_id: clientMutationId,
      }),
    },
    options
  );
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
    throw new Error('Synon Biomed fork-at-answer response is invalid');
  }
  const record = payload as Record<string, unknown>;
  const responseRootFrameId = typeof record.root_frame_id === 'string' ? record.root_frame_id.trim() : '';
  const branchId = typeof record.branch_id === 'string' ? record.branch_id.trim() : '';
  const generation =
    typeof record.generation === 'number' && Number.isSafeInteger(record.generation) ? record.generation : 0;
  if (responseRootFrameId !== rootFrameId || !/^br_[0-9a-f]{8}$/.test(branchId) || generation <= 0) {
    throw new Error('Synon Biomed fork-at-answer response is invalid');
  }
  invalidateSynonBiomedFrameReads(rootFrameId);
  return { rootFrameId: responseRootFrameId, branchId, generation };
}

export async function approveSynonBiomedPlan(
  frameId: string,
  options: SynonBiomedRuntimeGatewayOptions = {}
): Promise<{ snapshot: SynonBiomedRuntimeSnapshot; runtime: TConversationRuntimeSummary }> {
  return completePlanReview(frameId, 'approve-plan', options);
}

export async function discardSynonBiomedPlan(
  frameId: string,
  options: SynonBiomedRuntimeGatewayOptions = {}
): Promise<{ snapshot: SynonBiomedRuntimeSnapshot; runtime: TConversationRuntimeSummary }> {
  return completePlanReview(frameId, 'discard-plan', options);
}

async function completePlanReview(
  frameId: string,
  operation: 'approve-plan' | 'discard-plan',
  options: SynonBiomedRuntimeGatewayOptions
): Promise<{ snapshot: SynonBiomedRuntimeSnapshot; runtime: TConversationRuntimeSummary }> {
  await requestJson<unknown>(
    `/api/frames/${encodeURIComponent(frameId)}/${operation}`,
    {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: '{}',
    },
    options
  );
  invalidateSynonBiomedFrameReads(frameId);
  const snapshot = await loadSynonBiomedRuntimeSnapshot(frameId, options);
  return { snapshot, runtime: toSynonAIRuntimeSummary(snapshot) };
}
