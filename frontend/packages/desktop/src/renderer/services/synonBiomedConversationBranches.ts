import { registerRendererAccountReset } from './rendererAccountScope';

export type SynonBiomedCapabilityState = { state: 'supported' } | { state: 'unsupported'; reason: string };

export type SynonBiomedSessionWorkflowCapabilities = {
  goalTextSend: SynonBiomedCapabilityState;
  goalTextClear: SynonBiomedCapabilityState;
  asRoutine: SynonBiomedCapabilityState;
  manualReview: SynonBiomedCapabilityState;
  saveWorkflowAsSkill: SynonBiomedCapabilityState;
  messageFork: SynonBiomedCapabilityState;
  branchSelection: SynonBiomedCapabilityState;
  askUserAnswerFork: SynonBiomedCapabilityState;
};

export const SYNON_BIOMED_SESSION_WORKFLOW_CAPABILITIES: SynonBiomedSessionWorkflowCapabilities = {
  goalTextSend: {
    state: 'unsupported',
    reason: 'The deployed backend rejects non-empty goal_text with "not available in this build".',
  },
  goalTextClear: { state: 'supported' },
  asRoutine: {
    state: 'unsupported',
    reason: 'The deployed backend removes as_routine instead of persisting or executing it.',
  },
  manualReview: { state: 'supported' },
  saveWorkflowAsSkill: { state: 'supported' },
  messageFork: { state: 'supported' },
  branchSelection: { state: 'supported' },
  askUserAnswerFork: { state: 'supported' },
};

export type SynonBiomedConversationBranch = {
  id: string;
  parentId: string | null;
  forkPoint: number | null;
  createdAt: string | null;
  updatedAt: string | null;
  active: boolean;
};

export type SynonBiomedConversationBranchState = {
  activeBranchId: string | null;
  selectedBranchId: string | null;
  generation: number;
  branches: SynonBiomedConversationBranch[];
};

export type ForkSynonBiomedUserMessageInput = {
  rootFrameId: string;
  messageIndex: number;
  editedContent: string;
  sourceBranchId?: string | null;
  sourceClientMessageId?: string;
  clientMutationId?: string;
  verifierMode?: 'on' | 'off';
  memoryMode?: 'on' | 'off';
  ultraMode?: boolean;
  planMode?: boolean;
  targetAgent?: string;
};

export type ForkSynonBiomedUserMessageResult = {
  rootFrameId: string;
  branchId: string;
  generation: number;
  status: string;
};

type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;
type RecordValue = Record<string, unknown>;

export const SYNON_BIOMED_BRANCH_SELECTION_EVENT = 'synonbiomed:branch-selection-changed';
const selectedBranches = new Map<string, string>();
const branchSelectionRevisions = new Map<string, number>();
const BRANCH_ID_PATTERN = /^br_[0-9a-f]{8}$/;

function clearSynonBiomedBranchSelections(): void {
  selectedBranches.clear();
  branchSelectionRevisions.clear();
}

registerRendererAccountReset('synon-biomed-branch-selections', clearSynonBiomedBranchSelections);

export type SynonBiomedBranchSelectionEventDetail = {
  conversationId: string;
  branchId: string | null;
  previousBranchId: string | null;
  revision: number;
  rollback: boolean;
};

export function getSelectedSynonBiomedBranch(conversationId: string): string | null {
  return selectedBranches.get(conversationId) ?? null;
}

export function getSynonBiomedBranchSelectionRevision(conversationId: string): number {
  return branchSelectionRevisions.get(conversationId) ?? 0;
}

export function selectSynonBiomedBranch(
  conversationId: string,
  branchId: string | null
): SynonBiomedBranchSelectionEventDetail {
  const previousBranchId = getSelectedSynonBiomedBranch(conversationId);
  const normalized = branchId?.trim() ?? '';
  if (normalized) selectedBranches.set(conversationId, normalized);
  else selectedBranches.delete(conversationId);
  const revision = getSynonBiomedBranchSelectionRevision(conversationId) + 1;
  branchSelectionRevisions.set(conversationId, revision);
  const detail: SynonBiomedBranchSelectionEventDetail = {
    conversationId,
    branchId: normalized || null,
    previousBranchId,
    revision,
    rollback: false,
  };
  if (typeof window !== 'undefined') {
    window.dispatchEvent(new CustomEvent(SYNON_BIOMED_BRANCH_SELECTION_EVENT, { detail }));
  }
  return detail;
}

export function rollbackSynonBiomedBranchSelection(
  conversationId: string,
  failedBranchId: string | null,
  restoreBranchId: string | null,
  expectedRevision: number
): boolean {
  if (
    getSelectedSynonBiomedBranch(conversationId) !== failedBranchId ||
    getSynonBiomedBranchSelectionRevision(conversationId) !== expectedRevision
  ) {
    return false;
  }
  const normalized = restoreBranchId?.trim() ?? '';
  if (normalized) selectedBranches.set(conversationId, normalized);
  else selectedBranches.delete(conversationId);
  const revision = expectedRevision + 1;
  branchSelectionRevisions.set(conversationId, revision);
  if (typeof window !== 'undefined') {
    const detail: SynonBiomedBranchSelectionEventDetail = {
      conversationId,
      branchId: normalized || null,
      previousBranchId: failedBranchId,
      revision,
      rollback: true,
    };
    window.dispatchEvent(new CustomEvent(SYNON_BIOMED_BRANCH_SELECTION_EVENT, { detail }));
  }
  return true;
}

export function resetSynonBiomedBranchSelection(conversationId: string): void {
  selectedBranches.delete(conversationId);
  branchSelectionRevisions.set(conversationId, getSynonBiomedBranchSelectionRevision(conversationId) + 1);
}

export async function loadSynonBiomedConversationBranches(
  rootFrameId: string,
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedConversationBranchState> {
  return normalizeSynonBiomedConversationBranches(
    rootFrameId,
    await requestJson(`/api/frames/${encodeURIComponent(rootFrameId)}/branches`, { method: 'GET' }, fetchImpl)
  );
}

export function normalizeSynonBiomedConversationBranches(
  rootFrameId: string,
  value: unknown
): SynonBiomedConversationBranchState {
  const payload = asRecord(value);
  if (!payload) throw new Error('Synon Biomed returned an invalid branch response');
  const responseFrameId = stringValue(payload.root_frame_id);
  if (responseFrameId !== rootFrameId) {
    selectedBranches.delete(rootFrameId);
    throw new Error('branch_response_owner_mismatch');
  }
  const activeBranchId = stringValue(payload.active_branch_id);
  const generation = integerValue(payload.generation);
  if (!BRANCH_ID_PATTERN.test(activeBranchId) || generation <= 0 || !Array.isArray(payload.branches)) {
    throw new Error('Synon Biomed returned an invalid branch response');
  }
  const branches = payload.branches
    .map((raw): SynonBiomedConversationBranch => {
      const branch = asRecord(raw);
      const id = stringValue(branch?.id);
      const parentId = nullableString(branch?.parent_id);
      const active = branch?.active;
      if (
        !branch ||
        !BRANCH_ID_PATTERN.test(id) ||
        (parentId !== null && !BRANCH_ID_PATTERN.test(parentId)) ||
        typeof active !== 'boolean' ||
        active !== (id === activeBranchId)
      ) {
        throw new Error('Synon Biomed returned an invalid branch response');
      }
      return {
        id,
        parentId,
        forkPoint: nullableNumber(branch.fork_point),
        createdAt: nullableString(branch.created_at),
        updatedAt: nullableString(branch.updated_at),
        active,
      };
    })
    .toSorted((left, right) => (left.createdAt ?? '').localeCompare(right.createdAt ?? ''));
  if (branches.length === 0 || branches.filter((branch) => branch.active).length !== 1) {
    throw new Error('Synon Biomed returned an invalid branch response');
  }
  const cachedSelection = getSelectedSynonBiomedBranch(rootFrameId);
  const cachedSelectionIsCurrent = cachedSelection ? branches.some((branch) => branch.id === cachedSelection) : false;
  if (cachedSelection && !cachedSelectionIsCurrent) selectedBranches.delete(rootFrameId);

  return {
    activeBranchId,
    selectedBranchId: cachedSelectionIsCurrent ? cachedSelection : activeBranchId,
    generation,
    branches,
  };
}

export async function forkSynonBiomedUserMessage(
  input: ForkSynonBiomedUserMessageInput,
  fetchImpl: FetchLike = fetch
): Promise<ForkSynonBiomedUserMessageResult> {
  if (!Number.isInteger(input.messageIndex) || input.messageIndex < 0) {
    throw new Error('A non-negative source message index is required');
  }
  const editedContent = input.editedContent.trim();
  if (!editedContent) throw new Error('Edited message content cannot be empty');

  const branchState = await loadSynonBiomedConversationBranches(input.rootFrameId, fetchImpl);
  const sourceBranchId = input.sourceBranchId?.trim() || branchState.selectedBranchId || branchState.activeBranchId;
  if (!sourceBranchId || !branchState.branches.some((branch) => branch.id === sourceBranchId)) {
    throw new Error('Source branch is not available');
  }
  const clientMutationId = input.clientMutationId?.trim() || createSynonBiomedBranchMutationId();

  const payload = asRecord(
    await requestJson(
      `/api/frames/${encodeURIComponent(input.rootFrameId)}/fork`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': clientMutationId },
        body: JSON.stringify({
          message_index: input.messageIndex,
          edited_content: editedContent,
          source_branch_id: sourceBranchId,
          expected_branch_id: branchState.activeBranchId,
          expected_generation: branchState.generation,
          client_mutation_id: clientMutationId,
          ...(input.sourceClientMessageId?.trim()
            ? { source_client_message_id: input.sourceClientMessageId.trim() }
            : {}),
          ...(input.verifierMode ? { verifier_mode: input.verifierMode } : {}),
          ...(input.memoryMode ? { memory_mode: input.memoryMode } : {}),
          ...(input.ultraMode === undefined ? {} : { ultra_mode: input.ultraMode }),
          ...(input.planMode === undefined ? {} : { plan_mode: input.planMode }),
          ...(input.targetAgent ? { target_agent: input.targetAgent } : {}),
        }),
      },
      fetchImpl
    )
  );
  const rootFrameId = stringValue(payload?.root_frame_id);
  const branchId = stringValue(payload?.branch_id);
  const generation = integerValue(payload?.generation);
  if (rootFrameId !== input.rootFrameId || !BRANCH_ID_PATTERN.test(branchId) || generation <= 0) {
    throw new Error('Synon Biomed returned an invalid fork response');
  }
  return { rootFrameId, branchId, generation, status: stringValue(payload?.status) || 'accepted' };
}

export function createSynonBiomedBranchMutationId(): string {
  const randomUUID = globalThis.crypto?.randomUUID;
  if (typeof randomUUID !== 'function') throw new Error('secure_branch_mutation_identity_unavailable');
  return randomUUID.call(globalThis.crypto);
}

async function requestJson(path: string, init: RequestInit, fetchImpl: FetchLike): Promise<unknown> {
  const response = await fetchImpl(path, { ...init, headers: { Accept: 'application/json', ...init.headers } });
  if (!response.ok) {
    const payload = asRecord(await response.json().catch((): null => null));
    const detail = stringValue(payload?.detail) || stringValue(payload?.message) || stringValue(payload?.error);
    throw new Error(`Synon Biomed branch request failed: ${response.status}${detail ? ` ${detail}` : ''}`);
  }
  if (response.status === 204) return undefined;
  const text = await response.text();
  return text ? JSON.parse(text) : undefined;
}

function asRecord(value: unknown): RecordValue | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as RecordValue) : null;
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function nullableString(value: unknown): string | null {
  return stringValue(value) || null;
}

function nullableNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null;
}

function integerValue(value: unknown): number {
  return typeof value === 'number' && Number.isSafeInteger(value) ? value : 0;
}
