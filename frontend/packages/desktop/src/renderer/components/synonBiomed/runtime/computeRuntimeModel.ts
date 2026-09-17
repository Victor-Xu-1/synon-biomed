import type { SynonBiomedExecutionObservation, SynonBiomedKernel } from '@/renderer/services/synonBiomedNotebook';

export type ComputeRingPoint = {
  at: number;
  rss: number | null;
  cpu: number | null;
};

export type ComputeSessionGroup = {
  rootFrameId: string;
  projectId: string;
  projectName: string;
  title: string;
  kernels: SynonBiomedKernel[];
  rssBytes: number | null;
  cpuPct: number | null;
  busyCount: number;
  current: boolean;
};

export const formatMemory = (bytes: number | null | undefined): string => {
  if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes < 0) return '—';
  if (bytes === 0) return '0 B';
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} GB`;
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(1)} MB`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${Math.round(bytes)} B`;
};

export const formatCoresFromPercent = (percent: number | null | undefined): string => {
  if (typeof percent !== 'number' || !Number.isFinite(percent) || percent < 0) return '—';
  return `${(percent / 100).toFixed(1)} cores`;
};

export const formatAge = (value: string | null | undefined, now = Date.now()): string => {
  if (!value) return '—';
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return '—';
  const seconds = Math.max(0, Math.floor((now - timestamp) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h`;
  return `${Math.floor(hours / 24)}d`;
};

export const compactSource = (source: string): string => {
  const normalized = source.replace(/\s+/g, ' ').trim();
  return normalized.length > 160 ? `${normalized.slice(0, 157)}…` : normalized;
};

export const kernelExecutionKind = (kernel: SynonBiomedKernel): string => {
  if (kernel.kernelKind === 'bash') return 'Bash';
  if (kernel.kernelKind === 'repl') return 'REPL';
  return kernel.language === 'r' ? 'R' : 'Python';
};

type KernelProcessPresentation = {
  status: SynonBiomedExecutionObservation['status'] | 'stale';
  names: string[];
  processes: SynonBiomedExecutionObservation['processes'];
  memoryPressure?: SynonBiomedExecutionObservation['memoryPressure'];
};

export const observedKernelProcesses = (kernel: SynonBiomedKernel, now: number): KernelProcessPresentation => {
  const observation = kernel.executionObservation;
  const timestamp = observation ? Date.parse(observation.sampledAt) : Number.NaN;
  const currentId = kernel.currentCell?.tag ?? '';
  if (
    !currentId ||
    (!kernel.busy && !kernel.starting) ||
    !observation ||
    observation.status === 'unavailable' ||
    !Number.isFinite(timestamp) ||
    observation.executionId !== currentId ||
    observation.processes.length === 0
  ) {
    return { status: 'unavailable' as const, names: [], processes: [] };
  }
  if (now - timestamp > 15_000 || timestamp - now > 5_000) {
    return { status: 'stale' as const, names: [], processes: [] };
  }
  // Show all observed leaf programs, not the first word of a submitted command
  // or the hottest process. Retain the full tree in details for parallel work.
  const parents = new Set(observation.processes.map((process) => process.parentPid));
  const leaves = observation.processes.filter((process) => !parents.has(process.pid));
  if (leaves.length === 0) return { status: 'unavailable', names: [], processes: [] };
  return {
    status: observation.status,
    names: [...new Set(leaves.map((process) => process.name))],
    processes: observation.processes,
    memoryPressure: observation.memoryPressure,
  };
};

export const formatKernelEnvironment = (environment: string, softwareRuntimeLabel: string): string =>
  /^swr-[a-z0-9]+$/i.test(environment.trim()) ? softwareRuntimeLabel : environment;

export const sumNullable = (values: Array<number | null | undefined>): number | null => {
  const present = values.filter((value): value is number => typeof value === 'number' && Number.isFinite(value));
  return present.length === 0 ? null : present.reduce((total, value) => total + value, 0);
};

export const groupKernels = (
  kernels: SynonBiomedKernel[],
  currentRootFrameId: string,
  fallbackProjectName: string,
  fallbackSessionTitle: string
): ComputeSessionGroup[] => {
  const byRoot = new Map<string, ComputeSessionGroup>();
  for (const kernel of kernels) {
    const rootFrameId = kernel.rootFrameId || kernel.frameId;
    let group = byRoot.get(rootFrameId);
    if (!group) {
      group = {
        rootFrameId,
        projectId: kernel.projectId,
        projectName: kernel.projectName || (rootFrameId === currentRootFrameId ? fallbackProjectName : 'Synon Biomed'),
        title:
          kernel.sessionTitle || (rootFrameId === currentRootFrameId ? fallbackSessionTitle : '') || 'Untitled session',
        kernels: [],
        rssBytes: null,
        cpuPct: null,
        busyCount: 0,
        current: rootFrameId === currentRootFrameId,
      };
      byRoot.set(rootFrameId, group);
    }
    group.kernels.push(kernel);
    if (kernel.busy || kernel.starting) group.busyCount += 1;
  }
  const groups = [...byRoot.values()];
  for (const group of groups) {
    group.rssBytes = sumNullable(group.kernels.map((kernel) => kernel.rssBytes));
    group.cpuPct = sumNullable(group.kernels.map((kernel) => kernel.cpuPct));
  }
  groups.sort((left, right) => {
    if (left.current !== right.current) return left.current ? -1 : 1;
    return (right.rssBytes ?? 0) - (left.rssBytes ?? 0);
  });
  return groups;
};

export const mergeRingSamples = (
  rings: Map<string, ComputeRingPoint[]>,
  kernels: SynonBiomedKernel[],
  sampledAt: string | null,
  windowMs: number
): void => {
  const parsed = sampledAt ? Date.parse(sampledAt) : Date.now();
  const sampleTime = Number.isFinite(parsed) ? parsed : Date.now();
  const activeIds = new Set(kernels.map((kernel) => kernel.kernelId));
  for (const kernel of kernels) {
    const ring = rings.get(kernel.kernelId) ?? [];
    const last = ring[ring.length - 1];
    if (!last || last.at < sampleTime) {
      rings.set(
        kernel.kernelId,
        [...ring, { at: sampleTime, rss: kernel.rssBytes, cpu: kernel.cpuPct }].filter(
          (point) => sampleTime - point.at <= windowMs
        )
      );
    }
  }
  for (const kernelId of rings.keys()) {
    if (!activeIds.has(kernelId)) rings.delete(kernelId);
  }
};
