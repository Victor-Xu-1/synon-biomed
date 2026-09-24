import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import ScientificEnvironmentSettings from '@/renderer/pages/settings/ScientificEnvironmentSettings';
import { renderWithSettingsI18n } from './settingsI18nTestUtils';

vi.mock('@/renderer/pages/settings/components/SettingsPageWrapper', () => ({
  default: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));
const mocks = vi.hoisted(() => ({ load: vi.fn(), save: vi.fn(), retry: vi.fn(), pause: vi.fn(), uninstall: vi.fn() }));
vi.mock('@/renderer/services/scientificRuntimeSettings', () => ({
  loadScientificRuntimeSettings: mocks.load,
  pauseScientificRuntime: mocks.pause,
  saveScientificRuntimeSelection: mocks.save,
  retryScientificRuntime: mocks.retry,
  uninstallScientificRuntime: mocks.uninstall,
}));
const item = {
  id: 'autodock-vina',
  estimatedInstallBytes: 1024,
  estimatedInstallMB: 1,
  defaultEnabled: true,
  selected: false,
  available: true,
  status: 'waiting_for_selection',
  packages: [{ manager: 'pip', spec: 'vina==1.2.7' }],
};
const coreItem = {
  id: 'synon-biomed-python',
  kind: 'core' as const,
  required: true,
  estimatedInstallBytes: 0,
  estimatedInstallMB: 0,
  defaultEnabled: true,
  selected: true,
  available: true,
  status: 'ready',
  packages: [{ manager: 'conda', spec: 'python' }],
};
describe('scientific environment library', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.load.mockResolvedValue({ configured: true, options: [item] });
    mocks.save.mockResolvedValue(undefined);
    mocks.retry.mockResolvedValue(undefined);
    mocks.pause.mockResolvedValue(undefined);
    mocks.uninstall.mockResolvedValue(undefined);
  });
  it('requires explicit confirmation and preserves other selections when downloading', async () => {
    await renderWithSettingsI18n(<ScientificEnvironmentSettings />, 'en-US');
    fireEvent.click(await screen.findByRole('button', { name: 'Download environment' }));
    expect(mocks.save).not.toHaveBeenCalled();
    expect(mocks.retry).not.toHaveBeenCalled();
    mocks.load.mockResolvedValue({
      configured: true,
      options: [item, { ...item, id: 'medical-imaging', selected: true }],
    });
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Confirm download' }));
    await waitFor(() => expect(mocks.save).toHaveBeenCalledWith({ 'autodock-vina': true, 'medical-imaging': true }));
    expect(mocks.retry).not.toHaveBeenCalled();
  });
  it('filters actual packages, categories and readiness including future catalog entries', async () => {
    mocks.load.mockResolvedValue({
      configured: true,
      options: [item, { ...item, id: 'medical-imaging', status: 'ready' }, { ...item, id: 'future-tool' }],
    });
    await renderWithSettingsI18n(<ScientificEnvironmentSettings />, 'en-US');
    await waitFor(() =>
      expect(
        screen.getByTestId('scientific-environments').querySelectorAll('article[data-environment-id]')
      ).toHaveLength(3)
    );
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'vina==1.2.7' } });
    fireEvent.change(screen.getByRole('combobox', { name: 'Research category' }), { target: { value: 'other' } });
    expect(screen.getByTestId('scientific-environments').querySelectorAll('article[data-environment-id]')).toHaveLength(
      1
    );
    expect(screen.getByText('future-tool')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Filters' }));
    fireEvent.change(screen.getByRole('combobox', { name: 'Installation status' }), { target: { value: 'ready' } });
    expect(screen.getByText('No scientific environments match these filters')).toBeInTheDocument();
  });
  it('opens selection without installing and only saves an explicit choice', async () => {
    await renderWithSettingsI18n(<ScientificEnvironmentSettings />, 'en-US');
    await waitFor(() => expect(screen.getByRole('button', { name: 'Manage predownloads' })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: 'Manage predownloads' }));
    expect(mocks.save).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('checkbox'));
    fireEvent.click(screen.getByRole('button', { name: 'Save and prepare in background' }));
    await waitFor(() => expect(mocks.save).toHaveBeenCalledWith({ 'autodock-vina': true }));
  });
  it('retries selected failures without replacing the selection', async () => {
    mocks.load.mockResolvedValue({ configured: true, options: [{ ...item, selected: true, status: 'failed' }] });
    await renderWithSettingsI18n(<ScientificEnvironmentSettings />, 'en-US');
    fireEvent.click(await screen.findByRole('button', { name: 'Retry' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Confirm download' }));
    await waitFor(() => expect(mocks.retry).toHaveBeenCalledWith('autodock-vina'));
    expect(mocks.save).not.toHaveBeenCalled();
  });
  it('retains failure feedback and allows a deliberate retry', async () => {
    mocks.save.mockRejectedValueOnce(new Error('offline'));
    await renderWithSettingsI18n(<ScientificEnvironmentSettings />, 'en-US');
    fireEvent.click(await screen.findByRole('button', { name: 'Download environment' }));
    fireEvent.click(screen.getByRole('button', { name: 'Confirm download' }));
    expect(await screen.findByRole('alert')).toBeInTheDocument();
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('offers pause while preparing and uninstalls a ready environment through explicit actions', async () => {
    mocks.load.mockResolvedValueOnce({
      configured: true,
      options: [{ ...item, selected: true, status: 'preparing', phasePercent: 42 }],
    });
    const view = await renderWithSettingsI18n(<ScientificEnvironmentSettings />, 'en-US');
    fireEvent.click(await screen.findByRole('button', { name: 'Pause install' }));
    await waitFor(() => expect(mocks.pause).toHaveBeenCalledWith('autodock-vina'));
    view.unmount();

    mocks.load.mockResolvedValueOnce({
      configured: true,
      options: [{ ...item, selected: true, status: 'ready', environment: 'autodock-vina', generation: 'unused' }],
    });
    await renderWithSettingsI18n(<ScientificEnvironmentSettings />);
    fireEvent.click(await screen.findByRole('button', { name: '卸载' }));
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: '确认卸载' }));
    await waitFor(() => expect(mocks.uninstall).toHaveBeenCalledWith('autodock-vina'));
  });

  it('keeps required core runtimes visible but outside optional selection actions', async () => {
    mocks.load.mockResolvedValue({ configured: false, options: [coreItem, item] });
    await renderWithSettingsI18n(<ScientificEnvironmentSettings />, 'en-US');
    expect(await screen.findByText('Synon Biomed Python')).toBeInTheDocument();
    expect(screen.getByText('Required')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Manage predownloads' }));
    const dialog = await screen.findByRole('dialog');
    const coreCheckbox = within(dialog).getAllByRole('checkbox')[0];
    expect(coreCheckbox).toBeChecked();
    expect(coreCheckbox).toBeDisabled();
  });
  it.each(['ready', 'preparing'])('keeps required %s runtimes protected in the merged toolkit', async (status) => {
    mocks.load.mockResolvedValue({ configured: true, options: [{ ...coreItem, status }] });
    const view = await renderWithSettingsI18n(
      <ScientificEnvironmentSettings withWrapper={false} withHeader={false} compactHeader />,
      'en-US'
    );
    const required = await screen.findByText('Required');
    const card = required.closest('article');
    expect(card).toHaveClass('settings-library-card');
    expect(within(card!).queryByRole('button', { name: 'Pause install' })).not.toBeInTheDocument();
    expect(within(card!).queryByRole('button', { name: 'Uninstall' })).not.toBeInTheDocument();
    expect(mocks.pause).not.toHaveBeenCalled();
    expect(mocks.uninstall).not.toHaveBeenCalled();
    view.unmount();
  });
  it('retries a failed required runtime from the merged toolkit without changing optional selection', async () => {
    mocks.load.mockResolvedValue({ configured: true, options: [{ ...coreItem, status: 'failed' }] });
    await renderWithSettingsI18n(
      <ScientificEnvironmentSettings withWrapper={false} withHeader={false} compactHeader />,
      'en-US'
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Retry' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Confirm download' }));
    await waitFor(() => expect(mocks.retry).toHaveBeenCalledWith('synon-biomed-python'));
    expect(mocks.save).not.toHaveBeenCalled();
  });
  it('polls observed preparation and stops polling after network failure', async () => {
    mocks.load.mockResolvedValueOnce({
      configured: true,
      options: [{ ...item, status: 'preparing', phasePercent: 30 }],
    });
    mocks.load.mockRejectedValueOnce(new Error('offline'));
    const view = await renderWithSettingsI18n(<ScientificEnvironmentSettings />, 'en-US');
    expect(await screen.findByRole('progressbar')).toHaveAttribute('value', '30');
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 3200));
    });
    expect(await screen.findByText('Refresh failed. Previous values are still shown.')).toBeInTheDocument();
    expect(screen.queryByText('Ready for tasks')).not.toBeInTheDocument();
    view.unmount();
  });
});
