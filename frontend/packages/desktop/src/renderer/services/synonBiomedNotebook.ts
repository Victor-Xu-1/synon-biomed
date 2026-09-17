import { requestSynonBiomedJson, type SynonBiomedGatewayOptions } from './synonBiomedHttp';

export type SynonBiomedKernelCell = {
  tag: string | null;
  source: string;
  origin: string;
  startedAt: string;
  humanDescription: string | null;
  truncated: boolean;
};

export type SynonBiomedExecutionObservation = {
  executionId: string;
  sampledAt: string;
  status: 'observed' | 'partial' | 'unavailable';
  memoryPressure?: {
    status: 'normal' | 'pressured' | 'unavailable';
    currentBytes: number;
    highBytes: number;
    limitBytes: number;
    swapBytes: number;
    fullStallPercent: number;
  } | null;
  processes: Array<{
    pid: number;
    parentPid: number;
    startIdentity: string;
    name: string;
    nameSource: 'executable' | 'process_name';
    state: string;
  }>;
};

export type SynonBiomedKernel = {
  frameId: string;
  rootFrameId: string;
  projectId: string;
  agentName: string;
  delegateName: string | null;
  projectName: string;
  sessionTitle: string;
  environment: string;
  language: 'python' | 'r';
  kernelKind: string;
  kernelId: string;
  busy: boolean;
  starting: boolean;
  pidVisible: boolean;
  executionCount: number;
  cellCount: number;
  currentCell: SynonBiomedKernelCell | null;
  lastCell: { source: string; endedAt: string } | null;
  lastUsed: string | null;
  lastDescription: string;
  rssBytes: number | null;
  cpuPct: number | null;
  executionObservation?: SynonBiomedExecutionObservation | null;
};

export type SynonBiomedMachineMetrics = {
  sampledAt: string | null;
  totalMemoryBytes: number | null;
  availableMemoryBytes: number | null;
  cores: number | null;
  hostCores: number | null;
  busyCores: number | null;
  diskTotalBytes: number | null;
  diskAvailableBytes: number | null;
  cpuPct: number | null;
  cgroupCpuPct: number | null;
  kernelRssBytes: number | null;
  kernelCpuPct: number | null;
  kernelCount: number | null;
};

export type SynonBiomedKernelInventory = {
  kernels: SynonBiomedKernel[];
  hasHistory: boolean;
  machine: SynonBiomedMachineMetrics | null;
};

export type SynonBiomedKernelStopRequest = {
  mode: 'interrupt' | 'clear';
  reason?: string | null;
  force?: boolean;
  attachOnly?: boolean;
};

type RecordValue = Record<string, unknown>;

export async function loadSynonBiomedAllKernels(
  options: SynonBiomedGatewayOptions = {}
): Promise<SynonBiomedKernelInventory> {
  return loadKernelInventory('/api/kernels', options);
}

export async function loadSynonBiomedKernels(
  rootFrameId: string,
  options: SynonBiomedGatewayOptions = {}
): Promise<SynonBiomedKernelInventory> {
  return loadKernelInventory(`/api/frames/${encodeURIComponent(rootFrameId)}/kernels`, options);
}

export async function executeSynonBiomedKernel(
  kernel: Pick<SynonBiomedKernel, 'frameId' | 'language' | 'environment'>,
  code: string,
  options: SynonBiomedGatewayOptions = {}
): Promise<{ execId: string; toolUseId: string }> {
  const payload = await requestSynonBiomedJson<unknown>(
    `/api/frames/${encodeURIComponent(kernel.frameId)}/kernel-exec`,
    {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ language: kernel.language, environment: kernel.environment, code }),
    },
    options
  );
  if (!isRecord(payload)) throw new Error('Synon Biomed kernel execution response is invalid');
  const execId = stringValue(payload.exec_id);
  const toolUseId = stringValue(payload.tool_use_id);
  if (!execId || !toolUseId) throw new Error('Synon Biomed kernel execution response is incomplete');
  return { execId, toolUseId };
}

export async function interruptSynonBiomedKernel(
  frameId: string,
  execId: string,
  options: SynonBiomedGatewayOptions = {}
): Promise<{ interrupted: boolean; via: string | null; dequeued: boolean; reason: string | null }> {
  const payload = await requestSynonBiomedJson<unknown>(
    `/api/frames/${encodeURIComponent(frameId)}/kernel-exec/${encodeURIComponent(execId)}/interrupt`,
    { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' },
    options
  );
  if (!isRecord(payload)) throw new Error('Synon Biomed kernel interrupt response is invalid');
  return {
    interrupted: payload.interrupted === true,
    via: nullableString(payload.via),
    dequeued: payload.dequeued === true,
    reason: nullableString(payload.reason),
  };
}

async function loadKernelInventory(
  endpoint: string,
  options: SynonBiomedGatewayOptions
): Promise<SynonBiomedKernelInventory> {
  const payload = await requestSynonBiomedJson<unknown>(endpoint, {}, options);
  if (!isRecord(payload) || !Array.isArray(payload.kernels)) {
    throw new Error('Synon Biomed kernel response is invalid');
  }
  const kernels = payload.kernels.flatMap(normalizeKernel);
  return {
    kernels,
    hasHistory: payload.has_history === true || kernels.length > 0,
    machine: normalizeMachine(payload.machine),
  };
}

export async function stopSynonBiomedKernel(
  frameId: string,
  kernelId: string,
  request: SynonBiomedKernelStopRequest,
  options: SynonBiomedGatewayOptions = {}
): Promise<{
  ok: boolean;
  mode: 'interrupt' | 'clear';
  interrupted: boolean;
  reason: string | null;
}> {
  const reason = request.reason?.trim();
  const body = {
    mode: request.mode,
    ...(reason ? { reason: Array.from(reason).slice(0, 500).join('') } : {}),
    ...(request.force ? { force: true } : {}),
    ...(request.attachOnly ? { attach_only: true } : {}),
  };
  const payload = await requestSynonBiomedJson<unknown>(
    `/api/frames/${encodeURIComponent(frameId)}/kernels/${encodeURIComponent(kernelId)}/stop`,
    {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(body),
    },
    options
  );
  if (!isRecord(payload) || payload.ok !== true) {
    throw new Error('Synon Biomed kernel stop response is invalid');
  }
  return {
    ok: true,
    mode: payload.mode === 'interrupt' ? 'interrupt' : 'clear',
    interrupted: payload.interrupted === true,
    reason: nullableString(payload.reason),
  };
}

function normalizeMachine(value: unknown): SynonBiomedMachineMetrics | null {
  if (!isRecord(value)) return null;
  return {
    sampledAt: nullableString(value.sampled_at ?? value.sampledAt),
    totalMemoryBytes: nullableFiniteNumber(value.total_mem_bytes ?? value.totalMemoryBytes),
    availableMemoryBytes: nullableFiniteNumber(value.avail_mem_bytes ?? value.availableMemoryBytes),
    cores: nullableFiniteNumber(value.cores),
    hostCores: nullableFiniteNumber(value.host_cores ?? value.hostCores ?? value.cores),
    busyCores: nullableFiniteNumber(value.busy_count ?? value.busy_cores ?? value.busyCores),
    diskTotalBytes: nullableFiniteNumber(value.disk_total_bytes ?? value.diskTotalBytes),
    diskAvailableBytes: nullableFiniteNumber(value.disk_avail_bytes ?? value.diskAvailableBytes),
    cpuPct: nullableFiniteNumber(value.total_cpu_pct ?? value.cpu_pct ?? value.cpuPct),
    cgroupCpuPct: nullableFiniteNumber(value.cgroup_cpu_pct ?? value.cgroupCpuPct),
    kernelRssBytes: nullableFiniteNumber(value.kernel_rss_bytes ?? value.kernelRssBytes),
    kernelCpuPct: nullableFiniteNumber(value.kernel_cpu_pct ?? value.kernelCpuPct),
    kernelCount: nullableFiniteNumber(value.kernel_count ?? value.kernelCount),
  };
}

function normalizeKernel(value: unknown): SynonBiomedKernel[] {
  if (!isRecord(value)) return [];
  const frameId = stringValue(value.frame_id);
  const kernelId = stringValue(value.kernel_id);
  const language = stringValue(value.language).toLowerCase();
  if (!frameId || !kernelId || (language !== 'python' && language !== 'r')) return [];
  const current = isRecord(value.current_cell) ? value.current_cell : null;
  const last = isRecord(value.last_cell) ? value.last_cell : null;
  return [
    {
      frameId,
      rootFrameId: stringValue(value.root_frame_id) || frameId,
      projectId: stringValue(value.project_id),
      agentName: stringValue(value.agent_name),
      delegateName: nullableString(value.delegate_name),
      projectName: stringValue(value.project_name),
      sessionTitle: stringValue(value.session_title),
      environment: stringValue(value.environment),
      language,
      kernelKind: stringValue(value.kind ?? value.kernel_kind) || (language === 'r' ? 'r' : 'analysis'),
      kernelId,
      busy: value.busy === true,
      starting: value.starting === true,
      pidVisible: value.pid_visible === true,
      executionCount: finiteNumber(value.execution_count),
      cellCount: finiteNumber(value.cell_count),
      currentCell: current
        ? {
            tag: nullableString(current.tag ?? value.current_cell_tag),
            source: stringValue(current.source),
            origin: stringValue(current.origin),
            startedAt: stringValue(current.started_at),
            humanDescription: nullableString(current.human_description),
            truncated: current.truncated === true,
          }
        : null,
      lastCell: last
        ? {
            source: stringValue(last.source),
            endedAt: stringValue(last.ended_at),
          }
        : null,
      lastUsed: nullableString(value.last_used),
      lastDescription: stringValue(value.last_description),
      rssBytes: nullableFiniteNumber(value.rss_bytes),
      cpuPct: nullableFiniteNumber(value.cpu_pct),
      executionObservation: normalizeExecutionObservation(value.execution_observation),
    },
  ];
}

function normalizeExecutionObservation(value: unknown): SynonBiomedExecutionObservation | null {
  if (!isRecord(value) || !['observed', 'partial', 'unavailable'].includes(stringValue(value.status))) return null;
  const processes: SynonBiomedExecutionObservation['processes'] = [];
  const seenPids = new Set<number>();
  const input = Array.isArray(value.processes) ? value.processes : [];
  for (const item of input.slice(0, 64)) {
    if (
      !isRecord(item) ||
      !Number.isSafeInteger(item.pid) ||
      (item.pid as number) <= 0 ||
      !Number.isSafeInteger(item.parent_pid) ||
      (item.parent_pid as number) < 0 ||
      item.pid === item.parent_pid ||
      seenPids.has(item.pid as number) ||
      !/^[1-9]\d{0,19}$/.test(stringValue(item.start_identity)) ||
      !['executable', 'process_name'].includes(stringValue(item.name_source))
    )
      continue;
    const name = Array.from(stringValue(item.name).replace(/[\p{Cc}\p{Cf}]/gu, ''))
      .slice(0, 255)
      .join('');
    if (!name) continue;
    seenPids.add(item.pid as number);
    processes.push({
      pid: item.pid as number,
      parentPid: item.parent_pid as number,
      startIdentity: stringValue(item.start_identity),
      name,
      nameSource: item.name_source as 'executable' | 'process_name',
      state: stringValue(item.state).slice(0, 16),
    });
  }
  return {
    executionId: stringValue(value.execution_id),
    sampledAt: stringValue(value.sampled_at),
    status:
      input.length !== processes.length && value.status === 'observed'
        ? 'partial'
        : (value.status as SynonBiomedExecutionObservation['status']),
    processes: value.status === 'unavailable' ? [] : processes,
    memoryPressure: value.status === 'unavailable' ? null : normalizeMemoryPressure(value.memory_pressure),
  };
}

function normalizeMemoryPressure(value: unknown): SynonBiomedExecutionObservation['memoryPressure'] {
  if (!isRecord(value) || !['normal', 'pressured', 'unavailable'].includes(stringValue(value.status))) return null;
  const counters = ['current_bytes', 'high_bytes', 'limit_bytes', 'swap_bytes'] as const;
  if (
    counters.some((key) => !Number.isSafeInteger(value[key]) || (value[key] as number) < 0) ||
    typeof value.full_stall_percent !== 'number' ||
    !Number.isFinite(value.full_stall_percent) ||
    value.full_stall_percent < 0 ||
    value.full_stall_percent > 100
  )
    return null;
  return {
    status: value.status as 'normal' | 'pressured' | 'unavailable',
    currentBytes: value.current_bytes as number,
    highBytes: value.high_bytes as number,
    limitBytes: value.limit_bytes as number,
    swapBytes: value.swap_bytes as number,
    fullStallPercent: value.full_stall_percent,
  };
}

function isRecord(value: unknown): value is RecordValue {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function nullableString(value: unknown): string | null {
  return typeof value === 'string' && value.trim() ? value : null;
}

function finiteNumber(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0;
}

function nullableFiniteNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null;
}
