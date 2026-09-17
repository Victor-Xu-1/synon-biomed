export type SynonBiomedDelegateStatus = 'needs-input' | 'running' | 'failed' | 'completed' | 'stopped';

export type SynonBiomedDelegateFrame = {
  frameId: string;
  rootFrameId: string;
  parentFrameId: string | null;
  ordinal: number;
  label: string;
  agentName: string | null;
  status: SynonBiomedDelegateStatus;
  statusDescription: string | null;
  taskSummary: string | null;
  messageCount: number;
  directChildCount: number;
};

export type SynonBiomedDelegateLineage = {
  rootFrameId: string;
  currentFrameId: string;
  current: SynonBiomedDelegateFrame;
  ancestors: SynonBiomedDelegateFrame[];
  directChildren: SynonBiomedDelegateFrame[];
  rootChildren: SynonBiomedDelegateFrame[];
  totals: {
    needsInput: number;
    running: number;
    failed: number;
    completed: number;
    stopped: number;
  };
};

export type SynonBiomedDelegateLineageOptions = {
  fetchImpl?: typeof fetch;
  signal?: AbortSignal;
};

const STATUS_PRIORITY: Record<SynonBiomedDelegateStatus, number> = {
  'needs-input': 0,
  running: 1,
  failed: 2,
  completed: 3,
  stopped: 4,
};

export function orderSynonBiomedDelegates(children: SynonBiomedDelegateFrame[]): SynonBiomedDelegateFrame[] {
  return children.toSorted(
    (left, right) => STATUS_PRIORITY[left.status] - STATUS_PRIORITY[right.status] || left.ordinal - right.ordinal
  );
}

export function summarizeSynonBiomedDelegates(children: SynonBiomedDelegateFrame[]) {
  const totals = { needsInput: 0, running: 0, failed: 0, completed: 0, stopped: 0 };
  for (const child of children) {
    if (child.status === 'needs-input') totals.needsInput += 1;
    else totals[child.status] += 1;
  }
  return totals;
}

export async function loadSynonBiomedDelegateLineage(
  frameId: string,
  options: SynonBiomedDelegateLineageOptions = {}
): Promise<SynonBiomedDelegateLineage> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const response = await fetchImpl(`/api/conversations/${encodeURIComponent(frameId)}/lineage`, {
    headers: { accept: 'application/json' },
    signal: options.signal,
  });
  if (!response.ok) {
    throw new Error(`Synon Biomed delegate lineage request failed with ${response.status}`);
  }
  const payload: unknown = await response.json();
  if (!isDelegateLineage(payload)) throw new Error('Synon Biomed delegate lineage payload is invalid');
  return payload;
}

function isDelegateLineage(value: unknown): value is SynonBiomedDelegateLineage {
  if (!isRecord(value)) return false;
  if (!isNonEmptyString(value.rootFrameId) || !isNonEmptyString(value.currentFrameId)) return false;
  if (!isDelegateFrame(value.current)) return false;
  if (
    !isDelegateFrames(value.ancestors) ||
    !isDelegateFrames(value.directChildren) ||
    !isDelegateFrames(value.rootChildren)
  ) {
    return false;
  }
  const totals = value.totals;
  return (
    isRecord(totals) &&
    isCount(totals.needsInput) &&
    isCount(totals.running) &&
    isCount(totals.failed) &&
    isCount(totals.completed) &&
    isCount(totals.stopped)
  );
}

function isDelegateFrames(value: unknown): value is SynonBiomedDelegateFrame[] {
  return Array.isArray(value) && value.every(isDelegateFrame);
}

function isDelegateFrame(value: unknown): value is SynonBiomedDelegateFrame {
  if (!isRecord(value)) return false;
  return (
    isNonEmptyString(value.frameId) &&
    isNonEmptyString(value.rootFrameId) &&
    (value.parentFrameId === null || isNonEmptyString(value.parentFrameId)) &&
    isCount(value.ordinal) &&
    isNonEmptyString(value.label) &&
    (value.agentName === null || typeof value.agentName === 'string') &&
    isDelegateStatus(value.status) &&
    (value.statusDescription === null || typeof value.statusDescription === 'string') &&
    (value.taskSummary === null || typeof value.taskSummary === 'string') &&
    isCount(value.messageCount) &&
    isCount(value.directChildCount)
  );
}

function isDelegateStatus(value: unknown): value is SynonBiomedDelegateStatus {
  return (
    value === 'needs-input' || value === 'running' || value === 'failed' || value === 'completed' || value === 'stopped'
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0;
}

function isCount(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0;
}
