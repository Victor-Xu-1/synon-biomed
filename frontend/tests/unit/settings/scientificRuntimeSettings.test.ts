import { describe, expect, it, vi } from 'vitest';
import {
  loadScientificRuntimeSettings,
  pauseScientificRuntime,
  saveScientificRuntimeSelection,
  retryScientificRuntime,
  uninstallScientificRuntime,
} from '@/renderer/services/scientificRuntimeSettings';

describe('scientific runtime settings protocol', () => {
  it('reads observed progress without inventing a percentage or accepting invalid sizes', async () => {
    const entry = {
      id: 'tools',
      estimated_install_bytes: 1024,
      estimated_install_mb: 1,
      default_enabled: true,
      selected: true,
      available: true,
      runtime: { status: 'preparing', phase: 'download', phase_percent: 24 },
    };
    const fetchImpl = vi.fn().mockResolvedValue(new Response(JSON.stringify({ configured: true, options: [entry] })));
    const signal = new AbortController().signal;
    const result = await loadScientificRuntimeSettings({ fetchImpl, signal });
    expect(result.options[0]).toMatchObject({ status: 'preparing', phase: 'download', phasePercent: 24 });
    expect(fetchImpl.mock.calls[0][1].signal).toBe(signal);
    fetchImpl.mockResolvedValueOnce(
      new Response(JSON.stringify({ options: [{ ...entry, runtime: { status: 'preparing', phase_percent: 400 } }] }))
    );
    expect((await loadScientificRuntimeSettings({ fetchImpl })).options[0].phasePercent).toBeUndefined();
    fetchImpl.mockResolvedValueOnce(
      new Response(JSON.stringify({ options: [{ ...entry, estimated_install_mb: -1 }] }))
    );
    await expect(loadScientificRuntimeSettings({ fetchImpl })).rejects.toThrow('invalid');
  });

  it('accepts zero-sized required core runtimes and keeps their contract explicit', async () => {
    const entry = {
      id: 'synon-biomed-python',
      kind: 'core',
      required: true,
      estimated_install_bytes: 0,
      estimated_install_mb: 0,
      default_enabled: true,
      selected: true,
      available: true,
      runtime: { status: 'ready' },
    };
    const fetchImpl = vi.fn().mockResolvedValue(new Response(JSON.stringify({ options: [entry] })));
    const result = await loadScientificRuntimeSettings({ fetchImpl });
    expect(result.options[0]).toMatchObject({
      id: 'synon-biomed-python',
      kind: 'core',
      required: true,
      estimatedInstallBytes: 0,
      estimatedInstallMB: 0,
    });
  });

  it('saves explicit choices and retries through the existing authority', async () => {
    const fetchImpl = vi.fn().mockImplementation(async () => new Response('{}'));
    await saveScientificRuntimeSelection({ beta: true, alpha: true, disabled: false }, { fetchImpl });
    expect(fetchImpl).toHaveBeenLastCalledWith(
      '/api/preferences/scientific-runtimes',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ enabled_ids: ['alpha', 'beta'] }),
      })
    );
    await retryScientificRuntime('alpha', { fetchImpl });
    expect(fetchImpl).toHaveBeenLastCalledWith(
      '/api/preferences/scientific-runtimes',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ id: 'alpha' }),
      })
    );
    await pauseScientificRuntime('alpha', { fetchImpl });
    expect(fetchImpl).toHaveBeenLastCalledWith(
      '/api/preferences/scientific-runtimes',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ id: 'alpha', action: 'pause' }),
      })
    );
    await uninstallScientificRuntime('alpha', { fetchImpl });
    expect(fetchImpl).toHaveBeenLastCalledWith(
      '/api/preferences/scientific-runtimes',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ id: 'alpha', action: 'uninstall' }),
      })
    );
    fetchImpl.mockResolvedValueOnce(new Response('{"detail":"not selected"}', { status: 409 }));
    await expect(retryScientificRuntime('disabled', { fetchImpl })).rejects.toMatchObject({ status: 409 });
  });
});
