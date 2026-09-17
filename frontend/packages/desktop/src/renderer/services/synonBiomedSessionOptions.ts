import { invalidateSynonBiomedFrameReads, loadCachedSynonBiomedFrame } from './synonBiomedFrameReads';

export type SynonBiomedSessionOptions = {
  delegation: boolean;
  autoReview: boolean;
  memory: boolean;
  targetAgent: string;
  goalText: string | null;
  asRoutine: boolean;
};

export type SynonBiomedSessionConfigPatch = {
  autoReview?: boolean;
  memory?: boolean;
  goalText?: null;
};

type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;
type RecordValue = Record<string, unknown>;

const SESSION_REQUEST_TIMEOUT_MS = 8_000;

export const SYNON_BIOMED_SESSION_DEFAULTS: Readonly<SynonBiomedSessionOptions> = Object.freeze({
  delegation: false,
  autoReview: false,
  memory: false,
  targetAgent: 'OPERON',
  goalText: null,
  asRoutine: false,
});

export function createSynonBiomedSessionDefaults(targetAgent = 'OPERON'): SynonBiomedSessionOptions {
  return {
    ...SYNON_BIOMED_SESSION_DEFAULTS,
    targetAgent: targetAgent.trim() || SYNON_BIOMED_SESSION_DEFAULTS.targetAgent,
  };
}

export async function loadSynonBiomedSessionOptions(
  rootFrameId: string,
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedSessionOptions> {
  const framePayload =
    fetchImpl === fetch
      ? await loadCachedSynonBiomedFrame(rootFrameId)
      : await requestJson(`/api/frames/${encodeURIComponent(rootFrameId)}`, { method: 'GET' }, fetchImpl);
  const payloadRecord = asRecord(framePayload);
  const frame = asRecord(payloadRecord?.frame) ?? payloadRecord;
  if (!frame) throw new Error('Synon Biomed returned invalid session data');

  const inputData = asRecord(frame.input_data) ?? {};
  const contextData = asRecord(frame.context_data);
  const originalInput = asRecord(contextData?._original_input) ?? {};
  const delegation = readBooleanMode(originalInput.ultra_mode ?? inputData.ultra_mode);
  const autoReview = readOnOffMode(originalInput.verifier_mode ?? inputData.verifier_mode);
  const memoryOverride = readOnOffMode(originalInput.memory_mode ?? inputData.memory_mode);
  const goalText = nullableString(originalInput.goal_text ?? inputData.goal_text);
  const asRoutine = readBooleanMode(originalInput.as_routine ?? inputData.as_routine);

  return {
    delegation: delegation ?? SYNON_BIOMED_SESSION_DEFAULTS.delegation,
    autoReview: autoReview ?? SYNON_BIOMED_SESSION_DEFAULTS.autoReview,
    memory: memoryOverride ?? SYNON_BIOMED_SESSION_DEFAULTS.memory,
    targetAgent:
      stringValue(originalInput.target_agent) ||
      stringValue(frame.agent_name) ||
      SYNON_BIOMED_SESSION_DEFAULTS.targetAgent,
    goalText,
    asRoutine: asRoutine ?? SYNON_BIOMED_SESSION_DEFAULTS.asRoutine,
  };
}

export async function updateSynonBiomedSessionConfig(
  rootFrameId: string,
  patch: SynonBiomedSessionConfigPatch,
  fetchImpl: FetchLike = fetch
): Promise<void> {
  const body: Record<string, 'on' | 'off' | null> = {};
  if (patch.autoReview !== undefined) body.verifier_mode = patch.autoReview ? 'on' : 'off';
  if (patch.memory !== undefined) body.memory_mode = patch.memory ? 'on' : 'off';
  if (patch.goalText === null) body.goal_text = null;
  if (Object.keys(body).length === 0) return;

  await requestJson(
    `/api/frames/${encodeURIComponent(rootFrameId)}/session-config`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    },
    fetchImpl
  );
  invalidateSynonBiomedFrameReads(rootFrameId);
}

export function toSynonBiomedMessageSessionOptions(
  options: SynonBiomedSessionOptions,
  overrides: {
    planMode?: boolean;
    targetBranchId?: string | null;
    expectedBranchId?: string | null;
    expectedGeneration?: number;
  } = {}
) {
  return {
    ultra_mode: options.delegation,
    verifier_mode: options.autoReview ? ('on' as const) : ('off' as const),
    memory_mode: options.memory ? ('on' as const) : ('off' as const),
    target_agent: options.targetAgent,
    ...(overrides.planMode === undefined ? {} : { plan_mode: overrides.planMode }),
    ...(overrides.targetBranchId ? { target_branch_id: overrides.targetBranchId } : {}),
    ...(overrides.expectedBranchId ? { expected_branch_id: overrides.expectedBranchId } : {}),
    ...(overrides.expectedGeneration === undefined ? {} : { expected_generation: overrides.expectedGeneration }),
  };
}

async function requestJson(path: string, init: RequestInit, fetchImpl: FetchLike): Promise<unknown> {
  const controller = new AbortController();
  const timeoutId = setTimeout(() => controller.abort('session_request_timeout'), SESSION_REQUEST_TIMEOUT_MS);
  try {
    const response = await fetchImpl(path, {
      ...init,
      headers: {
        Accept: 'application/json',
        ...init.headers,
      },
      signal: controller.signal,
    });
    const text = response.status === 204 ? '' : await response.text();
    if (!response.ok) {
      let decoded: unknown = null;
      try {
        decoded = text ? JSON.parse(text) : null;
      } catch {
        // Preserve the bounded status-only error when the backend returns a non-JSON body.
      }
      const payload = asRecord(decoded);
      const detail = stringValue(payload?.detail) || stringValue(payload?.message) || stringValue(payload?.error);
      throw new Error(`Synon Biomed session request failed: ${response.status}${detail ? ` ${detail}` : ''}`);
    }
    return text ? JSON.parse(text) : undefined;
  } catch (error) {
    if (controller.signal.aborted) throw new Error('Synon Biomed session request timed out', { cause: error });
    throw error;
  } finally {
    clearTimeout(timeoutId);
  }
}

function readBooleanMode(value: unknown): boolean | null {
  if (typeof value === 'boolean') return value;
  if (value === 'on') return true;
  if (value === 'off') return false;
  return null;
}

function readOnOffMode(value: unknown): boolean | null {
  if (value === 'on' || value === true) return true;
  if (value === 'off' || value === false) return false;
  return null;
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
