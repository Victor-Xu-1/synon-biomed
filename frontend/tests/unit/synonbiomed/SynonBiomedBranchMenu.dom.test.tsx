import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const branchMocks = vi.hoisted(() => ({
  load: vi.fn(),
  select: vi.fn(),
  capability: { state: 'supported' as 'supported' | 'unsupported', reason: '' },
}));

vi.mock('@/renderer/services/synonBiomedConversationBranches', () => ({
  SYNON_BIOMED_SESSION_WORKFLOW_CAPABILITIES: {
    branchSelection: branchMocks.capability,
  },
  loadSynonBiomedConversationBranches: branchMocks.load,
  selectSynonBiomedBranch: branchMocks.select,
}));

import SynonBiomedBranchMenu from '@/renderer/components/synonBiomed/runtime/SynonBiomedBranchMenu';

const branchState = (owner: string) => ({
  activeBranchId: `${owner}-main`,
  selectedBranchId: `${owner}-main`,
  branches: [
    {
      id: `${owner}-main`,
      parentId: null,
      forkPoint: null,
      createdAt: '2026-07-01T00:00:00Z',
      updatedAt: '2026-07-01T00:00:00Z',
      active: true,
    },
    {
      id: `${owner}-branch`,
      parentId: `${owner}-main`,
      forkPoint: 2,
      createdAt: '2026-07-01T00:01:00Z',
      updatedAt: '2026-07-01T00:01:00Z',
      active: false,
    },
  ],
});

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
};

describe('SynonBiomedBranchMenu', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    branchMocks.capability.state = 'supported';
    branchMocks.capability.reason = '';
    branchMocks.load.mockResolvedValue(branchState('root-a'));
  });

  it('loads branches on demand and selects the exact branch coordinate', async () => {
    await renderWithI18n(<SynonBiomedBranchMenu rootFrameId='root-a' />);

    fireEvent.click(screen.getByTestId('synon-biomed-branch-trigger'));
    const menu = await screen.findByRole('menu', { name: '会话分支' });
    expect(branchMocks.load).toHaveBeenCalledWith('root-a');
    expect(within(menu).getByRole('menuitemradio', { name: '主分支 · 当前' })).toHaveAttribute('aria-checked', 'true');

    fireEvent.click(within(menu).getByRole('menuitemradio', { name: '分支 1' }));
    expect(branchMocks.select).toHaveBeenCalledWith('root-a', 'root-a-branch');
  });

  it('numbers child branches independently of root ordering', async () => {
    const state = branchState('root-a');
    state.activeBranchId = 'root-a-branch';
    state.selectedBranchId = 'root-a-branch';
    state.branches = state.branches.toReversed();
    state.branches.forEach((branch) => {
      branch.active = branch.id === 'root-a-branch';
    });
    branchMocks.load.mockResolvedValue(state);
    await renderWithI18n(<SynonBiomedBranchMenu rootFrameId='root-a' />);

    fireEvent.click(screen.getByTestId('synon-biomed-branch-trigger'));
    const menu = await screen.findByRole('menu', { name: '会话分支' });
    expect(within(menu).getByRole('menuitemradio', { name: '分支 1 · 当前' })).toHaveAttribute('aria-checked', 'true');
    expect(within(menu).getByRole('menuitemradio', { name: '主分支' })).toHaveAttribute('aria-checked', 'false');
  });

  it('shows a redacted recoverable error instead of an empty branch state', async () => {
    const errorSpy = vi.spyOn(console, 'error');
    branchMocks.load
      .mockRejectedValueOnce(new Error('branch failed for researcher@example.com with sk-secret123456'))
      .mockResolvedValueOnce(branchState('root-a'));
    await renderWithI18n(<SynonBiomedBranchMenu rootFrameId='root-a' />);

    fireEvent.click(screen.getByTestId('synon-biomed-branch-trigger'));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('无法加载会话分支。');
    expect(screen.queryByText('暂无分支')).not.toBeInTheDocument();
    expect(errorSpy).toHaveBeenCalledWith('[branches] load failed');
    expect(errorSpy.mock.calls.flat().map(String).join(' ')).not.toMatch(/researcher|secret123456/i);

    fireEvent.click(within(alert).getByRole('button', { name: '重试' }));
    expect(await screen.findByRole('menuitemradio', { name: '主分支 · 当前' })).toBeInTheDocument();
    expect(branchMocks.load).toHaveBeenCalledTimes(2);
  });

  it('never exposes a previous owner branch while the next owner is loading', async () => {
    const rootB = deferred<ReturnType<typeof branchState>>();
    branchMocks.load.mockImplementation((owner: string) =>
      owner === 'root-a' ? Promise.resolve(branchState('root-a')) : rootB.promise
    );
    const view = await renderWithI18n(<SynonBiomedBranchMenu rootFrameId='root-a' />);
    fireEvent.click(screen.getByTestId('synon-biomed-branch-trigger'));
    expect(await screen.findByRole('menuitemradio', { name: '主分支 · 当前' })).toBeInTheDocument();

    view.rerender(<SynonBiomedBranchMenu rootFrameId='root-b' />);
    await waitFor(() => expect(branchMocks.load).toHaveBeenCalledWith('root-b'));
    expect(screen.queryByRole('menuitemradio', { name: '主分支 · 当前' })).not.toBeInTheDocument();

    await act(async () => {
      rootB.resolve(branchState('root-b'));
      await rootB.promise;
    });
    expect(await screen.findByRole('menuitemradio', { name: '主分支 · 当前' })).toBeInTheDocument();
  });

  it('does not expose a branch selector while the canonical Transcript authority is unavailable', async () => {
    branchMocks.capability.state = 'unsupported';
    branchMocks.capability.reason = 'Transcript branch authority is unavailable.';
    await renderWithI18n(<SynonBiomedBranchMenu rootFrameId='root-a' />);

    expect(screen.queryByTestId('synon-biomed-branch-trigger')).not.toBeInTheDocument();
    expect(branchMocks.load).not.toHaveBeenCalled();
  });
});
