import { requestSynonBiomedJson, type SynonBiomedGatewayOptions } from './synonBiomedHttp';

export type SynonBiomedKernelCell = {
  tag: string | null;
  source: string;
  origin: string;
  startedAt: string;
  humanDescription: string | null;
  truncated: boolean;
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
    },
  ];
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
