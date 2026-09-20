import { act, fireEvent, renderHook, screen, waitFor, within } from '@testing-library/react';
import React from 'react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';
import { StorageUsagePanel } from '@/renderer/pages/settings/storage/StorageUsagePanel';
import { StorageLocationPanel } from '@/renderer/pages/settings/storage/StorageLocationPanel';
import { StorageRulesDialog } from '@/renderer/pages/settings/storage/StorageDialogs';
import { SynonBiomedSettingsRequestError } from '@/renderer/services/synonBiomedWorkspaceSettings';
import { formatStorageBytes, directoryChangeAllowed } from '@/renderer/pages/settings/storage/storagePresentation';
import { useStorageResource, type StorageResource } from '@/renderer/pages/settings/storage/useStorageResource';
import type { SynonBiomedDiskUsage, SynonBiomedDataDirectory } from '@/renderer/services/synonBiomedWorkspaceSettings';
import { renderWithSettingsI18n } from './settingsI18nTestUtils';

const saveRules = vi.hoisted(() => vi.fn());
vi.mock('@/renderer/services/synonBiomedWorkspaceSettings', async (load) => ({
  ...(await load<typeof import('@/renderer/services/synonBiomedWorkspaceSettings')>()),
  saveSynonBiomedStorageRules: saveRules,
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

function resource<T>(data: T | null): StorageResource<T> {
  return {
    data,
    loading: false,
    failed: false,
    refresh: vi.fn().mockResolvedValue(undefined),
    cancel: vi.fn(),
    replace: vi.fn(),
  };
}

const directory: SynonBiomedDataDirectory = {
  current: '/data/research',
  resolved: null,
  defaultPath: '/data/default',
  source: 'pointer',
  usageBytes: null,
  freeBytes: null,
  activeFrames: 0,
  configPath: '',
  pendingMove: null,
  lastMove: null,
};
const usage: SynonBiomedDiskUsage = {
  artifactsBytes: 1024,
  workspaceBytes: 2048,
  condaBytes: 0,
  toolResultsBytes: 0,
  logsBytes: 0,
  tempBytes: 0,
  availableBytes: null,
  warnings: [],
  scannedAt: '2026-01-02T03:04:00Z',
  accounting: 'logical-unique-within-category',
};

describe('storage resource request lifetime', () => {
  it('aborts replaced requests and ignores late results', async () => {
    const first = deferred<string>();
    const second = deferred<string>();
    const load = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const { result, unmount } = renderHook(() => useStorageResource(load));
    let refresh!: Promise<void>;
    act(() => {
      refresh = result.current.refresh(true);
    });
    expect(load.mock.calls[0][0].aborted).toBe(true);
    await act(async () => {
      second.resolve('new');
      await refresh;
    });
    await act(async () => {
      first.resolve('old');
      await first.promise;
    });
    expect(result.current.data).toBe('new');
    unmount();
  });

  it('retains values after refresh failure and recovers on retry', async () => {
    const load = vi
      .fn()
      .mockResolvedValueOnce('previous')
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValueOnce('current');
    const { result } = renderHook(() => useStorageResource(load));
    await waitFor(() => expect(result.current.data).toBe('previous'));
    await act(async () => {
      await result.current.refresh();
    });
    expect(result.current).toMatchObject({ data: 'previous', failed: true, loading: false });
    await act(async () => {
      await result.current.refresh();
    });
    expect(result.current).toMatchObject({ data: 'current', failed: false, loading: false });
  });

  it('cancels lazy scans without publishing errors or state after unmount', async () => {
    const pending = deferred<string>();
    const load = vi.fn().mockReturnValue(pending.promise);
    const { result, unmount } = renderHook(() => useStorageResource(load, false));
    expect(load).not.toHaveBeenCalled();
    let refresh!: Promise<void>;
    act(() => {
      refresh = result.current.refresh(false);
    });
    act(() => {
      result.current.cancel();
    });
    expect(load.mock.calls[0][0].aborted).toBe(true);
    expect(result.current.loading).toBe(false);
    unmount();
    await act(async () => {
      pending.reject(new Error('cancelled'));
      await refresh;
    });
  });
});

describe('storage presentation and safety', () => {
  it('blocks duplicate saves and dismissals while pending, then explains a failed write', async () => {
    const pending = deferred<never>();
    saveRules.mockReturnValueOnce(pending.promise);
    const onClose = vi.fn();
    const onSaved = vi.fn();
    const rules = { taskArtifacts: 'artifacts', logs: 'logs', toolResults: 'results', temp: 'tmp' };
    await renderWithSettingsI18n(
      <StorageRulesDialog
        rules={{
          root: '/data',
          rules,
          paths: rules,
          systemPaths: { taskRuns: '/data/runs', workspace: '/data/workspace' },
        }}
        onClose={onClose}
        onSaved={onSaved}
        beforeSave={vi.fn()}
      />,
      'en-US'
    );
    const save = screen.getByRole('button', { name: 'Save' });
    fireEvent.click(save);
    fireEvent.click(save);
    expect(saveRules).toHaveBeenCalledOnce();
    expect(save).toBeDisabled();
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(onClose).not.toHaveBeenCalled();
    await act(async () => {
      pending.reject(new SynonBiomedSettingsRequestError('conflict', 409));
    });
    expect(await screen.findByRole('alert')).toHaveTextContent('Storage state changed or tasks are active.');
    expect(onSaved).not.toHaveBeenCalled();
    expect(save).toBeEnabled();
  });
  it('distinguishes unknown or invalid sizes from genuine zero', () => {
    for (const value of [null, undefined, NaN, Infinity, -1]) expect(formatStorageBytes(value)).toBe('—');
    expect(formatStorageBytes(0)).toBe('0 B');
    expect(formatStorageBytes(1024)).toBe('1 KiB');
    expect(directoryChangeAllowed(null)).toBe(false);
    expect(directoryChangeAllowed({ ...directory, activeFrames: null })).toBe(false);
    expect(directoryChangeAllowed({ ...directory, activeFrames: 1 })).toBe(false);
    expect(directoryChangeAllowed(directory)).toBe(true);
  });

  it('shows partial and failed measurements truthfully, with a usable retry', async () => {
    const value = { ...resource({ ...usage, artifactsBytes: null, warnings: ['incomplete'] }), failed: true };
    await renderWithSettingsI18n(
      <MemoryRouter>
        <StorageUsagePanel resource={value} />
      </MemoryRouter>,
      'en-US'
    );
    expect(screen.getByText('Partial measurement')).toBeInTheDocument();
    expect(screen.getByText('Refresh failed. Previous values are still shown.')).toBeInTheDocument();
    const row = screen.getByText('Artifacts').closest('.storage-usage-row')!;
    expect(row).toHaveTextContent('—');
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(value.refresh).toHaveBeenCalledWith(true);
  });

  it('does not present entirely unknown usage as an empty disk', async () => {
    const value = resource({
      ...usage,
      artifactsBytes: null,
      workspaceBytes: null,
      condaBytes: null,
      toolResultsBytes: null,
      logsBytes: null,
      tempBytes: null,
    });
    const { container } = await renderWithSettingsI18n(
      <MemoryRouter>
        <StorageUsagePanel resource={value} />
      </MemoryRouter>,
      'en-US'
    );
    expect(container.querySelector('.storage-usage-summary strong')).toHaveTextContent('—');
    expect(screen.queryByText('0 B')).not.toBeInTheDocument();
  });

  it('keeps stale or active-task location controls disabled', async () => {
    const onChange = vi.fn();
    await renderWithSettingsI18n(
      <StorageLocationPanel
        resource={{ ...resource(directory), failed: true }}
        onChange={onChange}
        onFinishMove={vi.fn()}
      />,
      'en-US'
    );
    const button = screen.getByRole('button', { name: 'Change location' });
    expect(button).toBeDisabled();
    fireEvent.click(button);
    expect(onChange).not.toHaveBeenCalled();
    expect(within(screen.getByRole('alert')).getByRole('button', { name: 'Retry' })).toBeEnabled();
  });
});
