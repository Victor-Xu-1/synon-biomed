import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { StorageRuntimePanel } from '@/renderer/pages/settings/storage/StorageRuntimePanel';
import { renderWithSettingsI18n } from './settingsI18nTestUtils';

const mocks = vi.hoisted(() => ({ load: vi.fn(), save: vi.fn(), retry: vi.fn() }));
vi.mock('@/renderer/services/scientificRuntimeSettings', () => ({
  loadScientificRuntimeSettings: mocks.load,
  saveScientificRuntimeSelection: mocks.save,
  retryScientificRuntime: mocks.retry,
}));
const item = {
  id: 'autodock-vina',
  estimatedInstallBytes: 1024,
  estimatedInstallMB: 1,
  defaultEnabled: true,
  selected: true,
  available: true,
  status: 'ready',
};

describe('scientific software storage panel', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.load.mockResolvedValue({ configured: true, options: [item] });
    mocks.save.mockResolvedValue(undefined);
    mocks.retry.mockResolvedValue(undefined);
  });

  it('shows readiness but never treats it as task execution, and retries failed installations', async () => {
    mocks.load.mockResolvedValueOnce({ configured: true, options: [{ ...item, status: 'failed' }] });
    await renderWithSettingsI18n(<StorageRuntimePanel />, 'en-US');
    expect(await screen.findByText('Installation failed')).toBeInTheDocument();
    fireEvent.click(screen.getByText('Installation status and available tools'));
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(mocks.retry).toHaveBeenCalledWith('autodock-vina'));
    expect(await screen.findByText('Ready for tasks')).toBeInTheDocument();
  });

  it('does not install from opening the page or dialog; only confirmed saving starts preparation', async () => {
    await renderWithSettingsI18n(<StorageRuntimePanel />, 'en-US');
    await waitFor(() => expect(screen.getByRole('button', { name: 'Choose software' })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: 'Choose software' }));
    expect(await screen.findByRole('dialog')).toBeInTheDocument();
    expect(mocks.save).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('checkbox'));
    fireEvent.click(screen.getByRole('button', { name: 'Save and prepare in background' }));
    await waitFor(() => expect(mocks.save).toHaveBeenCalledWith({ 'autodock-vina': false }));
  });

  it('refreshes a running install but stops on a network failure without presenting readiness', async () => {
    mocks.load.mockResolvedValueOnce({
      configured: true,
      options: [{ ...item, status: 'preparing', phasePercent: 30 }],
    });
    mocks.load.mockRejectedValueOnce(new Error('offline'));
    const view = await renderWithSettingsI18n(<StorageRuntimePanel />, 'en-US');
    expect(await screen.findByText('Installing and checking')).toBeInTheDocument();
    expect(screen.getByRole('progressbar')).toHaveAttribute('value', '30');
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 3200));
    });
    expect(await screen.findByText('Refresh failed. Previous values are still shown.')).toBeInTheDocument();
    expect(screen.queryByText('Ready for tasks')).not.toBeInTheDocument();
    const count = mocks.load.mock.calls.length;
    view.unmount();
    expect(mocks.load).toHaveBeenCalledTimes(count);
  });
});
