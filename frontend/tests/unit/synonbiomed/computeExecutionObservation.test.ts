import { describe, expect, it } from 'vitest';
import {
  kernelExecutionKind,
  observedKernelProcesses,
} from '@/renderer/components/synonBiomed/runtime/computeRuntimeModel';
import { loadSynonBiomedAllKernels, type SynonBiomedKernel } from '@/renderer/services/synonBiomedNotebook';

const now = Date.parse('2026-09-17T00:00:00Z');
const sample = (): SynonBiomedKernel => ({
  frameId: 'frame',
  rootFrameId: 'frame',
  projectId: 'project',
  agentName: 'OPERON',
  delegateName: null,
  projectName: 'project',
  sessionTitle: 'task',
  environment: 'environment-is-not-a-program',
  language: 'python',
  kernelKind: 'bash',
  kernelId: 'kernel',
  busy: true,
  starting: false,
  pidVisible: true,
  executionCount: 1,
  cellCount: 1,
  currentCell: {
    tag: 'exec-1',
    source: 'submitted-wrapper',
    origin: 'agent',
    startedAt: new Date(now - 1000).toISOString(),
    humanDescription: 'A model description',
    truncated: false,
  },
  lastCell: null,
  lastUsed: null,
  lastDescription: 'obsolete operation',
  rssBytes: 1,
  cpuPct: 10,
  executionObservation: {
    executionId: 'exec-1',
    sampledAt: new Date(now).toISOString(),
    status: 'observed',
    processes: [
      {
        pid: 10,
        parentPid: 1,
        startIdentity: '100',
        name: 'python3',
        nameSource: 'executable',
        state: 'S',
      },
      {
        pid: 11,
        parentPid: 10,
        startIdentity: '101',
        name: 'converter',
        nameSource: 'executable',
        state: 'R',
      },
      {
        pid: 12,
        parentPid: 10,
        startIdentity: '102',
        name: 'compressor',
        nameSource: 'executable',
        state: 'S',
      },
    ],
  },
});

describe('execution process observations', () => {
  it('binds memory pressure to the same live execution and freshness window', () => {
    const kernel = sample();
    const pressure = {
      status: 'pressured' as const,
      currentBytes: 90,
      highBytes: 85,
      limitBytes: 100,
      swapBytes: 0,
      fullStallPercent: 91,
    };
    kernel.executionObservation!.memoryPressure = pressure;
    expect(observedKernelProcesses(kernel, now).memoryPressure).toEqual(pressure);
    expect(observedKernelProcesses(kernel, now + 15001).memoryPressure).toBeUndefined();
    kernel.executionObservation!.executionId = 'old-execution';
    expect(observedKernelProcesses(kernel, now).memoryPressure).toBeUndefined();
    kernel.executionObservation!.executionId = 'exec-1';
    kernel.busy = false;
    expect(observedKernelProcesses(kernel, now).memoryPressure).toBeUndefined();
  });

  it.each([
    [
      {
        status: 'pressured',
        current_bytes: 90,
        high_bytes: 85,
        limit_bytes: 100,
        swap_bytes: 0,
        full_stall_percent: 95,
      },
      'pressured',
    ],
    [
      {
        status: 'pressured',
        current_bytes: -1,
        high_bytes: 85,
        limit_bytes: 100,
        swap_bytes: 0,
        full_stall_percent: 95,
      },
      null,
    ],
    [
      {
        status: 'pressured',
        current_bytes: 90,
        high_bytes: 85,
        limit_bytes: 100,
        swap_bytes: 0,
        full_stall_percent: 101,
      },
      null,
    ],
  ])('validates optional resource metrics at the API boundary', async (pressure, expectedStatus) => {
    const result = await loadSynonBiomedAllKernels({
      fetchImpl: async () =>
        new Response(
          JSON.stringify({
            kernels: [
              {
                frame_id: 'frame',
                kernel_id: 'kernel',
                language: 'python',
                execution_observation: {
                  execution_id: 'exec-1',
                  sampled_at: new Date(now).toISOString(),
                  status: 'observed',
                  memory_pressure: pressure,
                  processes: [
                    {
                      pid: 1,
                      parent_pid: 0,
                      start_identity: '1',
                      name: 'worker',
                      name_source: 'executable',
                      state: 'R',
                    },
                  ],
                },
              },
            ],
          })
        ),
    });
    expect(result.kernels[0].executionObservation?.memoryPressure?.status ?? null).toBe(expectedStatus);
  });
  it('shows parallel observed leaves rather than environment, prose, or command parsing', () => {
    const kernel = sample();
    expect(kernelExecutionKind(kernel)).toBe('Bash');
    expect(observedKernelProcesses(kernel, now).names).toEqual(['converter', 'compressor']);
  });

  it('discards expired, future, missing, and mismatched execution samples', () => {
    expect(observedKernelProcesses(sample(), now + 15001).status).toBe('stale');
    expect(observedKernelProcesses(sample(), now - 5001).status).toBe('stale');
    const kernel = sample();
    kernel.executionObservation!.executionId = 'old-execution';
    expect(observedKernelProcesses(kernel, now).names).toEqual([]);
    kernel.executionObservation = null;
    expect(observedKernelProcesses(kernel, now).status).toBe('unavailable');
  });

  it('reports partial sampling explicitly and keeps the full observed tree', () => {
    const kernel = sample();
    kernel.executionObservation!.status = 'partial';
    const observed = observedKernelProcesses(kernel, now);
    expect(observed.status).toBe('partial');
    expect(observed.processes).toHaveLength(3);
  });

  it('does not retain processes from a completed cell as a new current execution', () => {
    const kernel = sample();
    kernel.busy = false;
    kernel.currentCell = null;
    expect(observedKernelProcesses(kernel, now).names).toEqual([]);
  });

  it('does not label idle, unbound, or cyclic observations as current programs', () => {
    const idle = sample();
    idle.busy = false;
    expect(observedKernelProcesses(idle, now).status).toBe('unavailable');
    const unbound = sample();
    unbound.currentCell!.tag = '';
    unbound.executionObservation!.executionId = '';
    expect(observedKernelProcesses(unbound, now).status).toBe('unavailable');
    const cyclic = sample();
    cyclic.executionObservation!.processes[0].parentPid = 11;
    cyclic.executionObservation!.processes.pop();
    expect(observedKernelProcesses(cyclic, now).status).toBe('unavailable');
  });

  it('normalizes the optional API observation without trusting malformed process entries', async () => {
    const inventory = await loadSynonBiomedAllKernels({
      fetchImpl: async () =>
        new Response(
          JSON.stringify({
            kernels: [
              {
                frame_id: 'frame',
                kernel_id: 'kernel',
                language: 'python',
                kind: 'bash',
                current_cell_tag: 'exec-1',
                current_cell: {
                  source: 'submitted',
                  started_at: new Date(now).toISOString(),
                },
                execution_observation: {
                  execution_id: 'exec-1',
                  sampled_at: new Date(now).toISOString(),
                  status: 'observed',
                  processes: [
                    {
                      pid: 11,
                      parent_pid: 10,
                      start_identity: '101',
                      name: 'worker\u0000',
                      name_source: 'executable',
                      state: 'R',
                    },
                    {
                      pid: -1,
                      parent_pid: 10,
                      start_identity: '102',
                      name: 'not-a-process',
                      name_source: 'executable',
                    },
                    {
                      pid: 11,
                      parent_pid: 10,
                      start_identity: '101',
                      name: 'duplicate',
                      name_source: 'executable',
                    },
                    {
                      pid: 12,
                      parent_pid: 12,
                      start_identity: '102',
                      name: 'self-parent',
                      name_source: 'executable',
                    },
                    {
                      pid: 13,
                      parent_pid: 10,
                      start_identity: '0',
                      name: 'invalid-start',
                      name_source: 'executable',
                    },
                  ],
                },
              },
            ],
          })
        ),
    });
    expect(inventory.kernels[0].kernelKind).toBe('bash');
    expect(inventory.kernels[0].currentCell?.tag).toBe('exec-1');
    expect(inventory.kernels[0].executionObservation?.status).toBe('partial');
    expect(inventory.kernels[0].executionObservation?.processes.map((item) => item.name)).toEqual(['worker']);
  });
});
