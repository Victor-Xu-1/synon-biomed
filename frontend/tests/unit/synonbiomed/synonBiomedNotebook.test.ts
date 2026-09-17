import {
  executeSynonBiomedKernel,
  interruptSynonBiomedKernel,
  loadSynonBiomedAllKernels,
  loadSynonBiomedKernels,
  stopSynonBiomedKernel,
} from '@/renderer/services/synonBiomedNotebook';
import { describe, expect, it, vi } from 'vitest';

const inventoryPayload = {
  has_history: true,
  kernels: [
    {
      frame_id: 'frame-1',
      root_frame_id: 'root-1',
      project_id: 'project-1',
      project_name: 'Personal Workspace',
      session_title: 'CRBN Ligand Design',
      agent_name: 'OPERON',
      environment: 'chem',
      language: 'python',
      kind: 'analysis',
      kernel_id: 'kernel-1',
      busy: false,
      starting: false,
      pid_visible: true,
      execution_count: 9,
      cell_count: 9,
      last_used: '2026-08-20T23:00:00Z',
      last_description: 'Generating 2D structure image grid',
      last_cell: { source: 'print(42)', ended_at: '2026-08-20T23:00:00Z' },
      rss_bytes: 33200000,
      cpu_pct: 0,
    },
  ],
  machine: {
    sampled_at: '2026-08-21T00:00:00Z',
    cores: 16,
    host_cores: 16,
    busy_count: 0,
    kernel_count: 1,
    kernel_rss_bytes: 33200000,
    kernel_cpu_pct: 0,
    total_mem_bytes: 1000000000,
    avail_mem_bytes: 500000000,
    disk_total_bytes: 2000000000,
    disk_avail_bytes: 1000000000,
    total_cpu_pct: 12.5,
    cgroup_cpu_pct: 23.9,
  },
};

describe('Synon Biomed notebook service', () => {
  it('normalizes the frame-scoped v1.1 live kernel inventory', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ has_history: inventoryPayload.has_history, kernels: inventoryPayload.kernels }), {
        status: 200,
      })
    );

    const inventory = await loadSynonBiomedKernels('root/1', { fetchImpl });
    expect(inventory.hasHistory).toBe(true);
    expect(inventory.machine).toBeNull();
    expect(inventory.kernels[0]).toEqual(
      expect.objectContaining({
        frameId: 'frame-1',
        rootFrameId: 'root-1',
        projectId: 'project-1',
        kernelId: 'kernel-1',
        kernelKind: 'analysis',
        pidVisible: true,
        rssBytes: 33200000,
        lastCell: { source: 'print(42)', endedAt: '2026-08-20T23:00:00Z' },
      })
    );
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/frames/root%2F1/kernels',
      expect.objectContaining({ credentials: 'include' })
    );
  });

  it('uses the global workspace kernel and machine contract', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(new Response(JSON.stringify(inventoryPayload), { status: 200 }));

    const inventory = await loadSynonBiomedAllKernels({ fetchImpl });
    expect(inventory.machine).toEqual({
      sampledAt: '2026-08-21T00:00:00Z',
      totalMemoryBytes: 1000000000,
      availableMemoryBytes: 500000000,
      cores: 16,
      hostCores: 16,
      busyCores: 0,
      diskTotalBytes: 2000000000,
      diskAvailableBytes: 1000000000,
      cpuPct: 12.5,
      cgroupCpuPct: 23.9,
      kernelRssBytes: 33200000,
      kernelCpuPct: 0,
      kernelCount: 1,
    });
    expect(fetchImpl).toHaveBeenCalledWith('/api/kernels', expect.objectContaining({ credentials: 'include' }));
  });

  it('uses the canonical kernel stop contract', async () => {
    const fetchImpl = vi.fn().mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          ok: true,
          mode: 'clear',
          interrupted: false,
        })
      )
    );
    const options = { fetchImpl };

    await expect(
      stopSynonBiomedKernel(
        'frame-1',
        'kernel-1',
        { mode: 'clear', reason: '  use less memory  ', force: true },
        options
      )
    ).resolves.toEqual({
      ok: true,
      mode: 'clear',
      interrupted: false,
      reason: null,
    });
    expect(fetchImpl).toHaveBeenNthCalledWith(
      1,
      '/api/frames/frame-1/kernels/kernel-1/stop',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({
          mode: 'clear',
          reason: 'use less memory',
          force: true,
        }),
      })
    );
  });

  it('uses the shared live-kernel terminal execute and interrupt contracts', async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ exec_id: 'exec-1', tool_use_id: 'user-exec-1' }), {
          status: 200,
        })
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ interrupted: true, via: 'sigint', dequeued: false }), {
          status: 200,
        })
      );
    const kernel = {
      frameId: 'frame-1',
      language: 'python' as const,
      environment: 'chem',
    };

    await expect(executeSynonBiomedKernel(kernel, 'print(42)', { fetchImpl })).resolves.toEqual({
      execId: 'exec-1',
      toolUseId: 'user-exec-1',
    });
    await expect(interruptSynonBiomedKernel('frame-1', 'exec-1', { fetchImpl })).resolves.toEqual({
      interrupted: true,
      via: 'sigint',
      dequeued: false,
      reason: null,
    });
    expect(fetchImpl).toHaveBeenNthCalledWith(
      1,
      '/api/frames/frame-1/kernel-exec',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ language: 'python', environment: 'chem', code: 'print(42)' }),
      })
    );
    expect(fetchImpl).toHaveBeenNthCalledWith(
      2,
      '/api/frames/frame-1/kernel-exec/exec-1/interrupt',
      expect.objectContaining({ method: 'POST', body: '{}' })
    );
  });
});
