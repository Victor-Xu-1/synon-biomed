import { act, cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import React, { useState } from 'react';
import { MemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import SynonBiomedSessionOptionsMenu from '@/renderer/components/synonBiomed/runtime/SynonBiomedSessionOptionsMenu';
import type { SynonBiomedSessionOptions } from '@/renderer/services/synonBiomedSessionOptions';
import { renderWithI18n } from '../i18nTestUtils';

const mocks = vi.hoisted(() => ({
  loadAssistants: vi.fn(),
  loadProviders: vi.fn(),
  loadSessionProviders: vi.fn(),
  setSessionProvider: vi.fn(),
  persistOption: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedCatalog', () => ({
  loadSynonBiomedAssistants: mocks.loadAssistants,
}));

vi.mock('@/renderer/services/synonBiomedCompute', () => ({
  loadSynonBiomedComputeProviders: mocks.loadProviders,
  loadSynonBiomedSessionComputeProviders: mocks.loadSessionProviders,
  setSynonBiomedSessionComputeProvider: mocks.setSessionProvider,
}));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: { ...actual.Message, success: mocks.success, error: mocks.error },
  };
});

const initialValue: SynonBiomedSessionOptions = {
  delegation: false,
  autoReview: false,
  memory: false,
  targetAgent: 'OPERON',
  goalText: null,
  asRoutine: false,
};

const Harness = () => {
  const [value, setValue] = useState(initialValue);
  const persistOption = async (key: 'autoReview' | 'memory', checked: boolean) => {
    await mocks.persistOption(key, checked);
    setValue((current) => ({ ...current, [key]: checked }));
  };
  return (
    <MemoryRouter>
      <>
        <SynonBiomedSessionOptionsMenu
          rootFrameId='frame-stat6'
          value={value}
          onChange={setValue}
          onPersistentOptionChange={persistOption}
        />
        <output data-testid='session-value'>{JSON.stringify(value)}</output>
      </>
    </MemoryRouter>
  );
};

describe('SynonBiomedSessionOptionsMenu component', () => {
  beforeEach(() => {
    mocks.loadAssistants.mockResolvedValue([
      { id: 'synonbiomed:operon', name: 'OPERON', agent_id: 'OPERON' },
      {
        id: 'synonbiomed:aidd-expert',
        name: 'AI Drug Discovery Expert',
        name_i18n: {
          'en-US': 'AI Drug Discovery Expert',
          'zh-CN': 'AI 药物研发专家',
        },
        agent_id: 'AIDD_EXPERT',
      },
    ]);
    mocks.loadProviders.mockResolvedValue([
      { name: 'local', displayName: 'Local', family: 'local', checked: true },
      { name: 'ssh:hpc-a', displayName: 'HPC A', family: 'ssh', checked: true },
    ]);
    mocks.loadSessionProviders.mockResolvedValue(['local']);
    mocks.setSessionProvider.mockResolvedValue(undefined);
    mocks.persistOption.mockResolvedValue(undefined);
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it('renders every v1.1 session control in the original order', async () => {
    await renderWithI18n(<Harness />);
    const trigger = screen.getByRole('button', { name: '会话选项' });
    expect(trigger).toHaveClass('composer-icon-control');
    fireEvent.click(trigger);

    const menu = await screen.findByRole('menu', { name: '会话选项' });
    expect(menu).toHaveClass('app-overlay-menu', 'composer-control-menu');
    expect(menu).toHaveStyle({
      maxWidth: 'calc(100vw - 24px)',
      maxHeight: 'calc(100vh - 24px)',
    });
    expect(menu).toHaveTextContent('委派自动审阅记忆专家无计算');
    expect(screen.queryByTestId('session-config-row-control-center')).not.toBeInTheDocument();
    expect(screen.queryByText('对话能力中枢')).not.toBeInTheDocument();
    expect(screen.queryByRole('menuitem', { name: '手动审阅' })).not.toBeInTheDocument();
    expect(screen.getByRole('menuitemcheckbox', { name: '委派' })).not.toBeChecked();
    const autoReviewOption = screen.getByRole('menuitemcheckbox', { name: '自动审阅' });
    expect(autoReviewOption).not.toBeChecked();
    expect(autoReviewOption).toHaveClass('session-options-menu__row');
    expect(screen.getByRole('menuitemcheckbox', { name: '记忆' })).not.toBeChecked();

    expect(screen.queryByText('AI 药物研发专家')).not.toBeInTheDocument();
    fireEvent.mouseEnter(screen.getByTestId('session-config-row-specialist'));
    const specialistOption = await screen.findByText('AI 药物研发专家');
    expect(specialistOption.closest('[role="menu"]')).toHaveClass('app-overlay-menu', 'composer-control-submenu');
    expect(specialistOption.closest('[role="menuitemradio"]')).toHaveAttribute('aria-checked', 'false');
    expect(screen.getByRole('menuitem', { name: '新建专家...' })).toBeInTheDocument();
    fireEvent.mouseEnter(screen.getByTestId('session-config-row-compute'));
    expect(
      (await screen.findByText('HPC A')).parentElement?.querySelector('[role="menuitemcheckbox"]')
    ).toHaveAttribute('aria-checked', 'false');
    expect(screen.getByRole('menuitem', { name: '管理计算资源...' })).toBeInTheDocument();
  });

  it('updates delegation locally and persists reviewer and memory changes before committing UI state', async () => {
    await renderWithI18n(<Harness />);
    fireEvent.click(screen.getByRole('button', { name: '会话选项' }));

    fireEvent.click(await screen.findByRole('menuitemcheckbox', { name: '委派' }));
    expect(screen.getByTestId('session-value')).toHaveTextContent('"delegation":true');

    fireEvent.click(screen.getByRole('menuitemcheckbox', { name: '自动审阅' }));
    await waitFor(() => expect(mocks.persistOption).toHaveBeenCalledWith('autoReview', true));
    expect(screen.getByTestId('session-value')).toHaveTextContent('"autoReview":true');

    fireEvent.click(screen.getByRole('menuitemcheckbox', { name: '记忆' }));
    await waitFor(() => expect(mocks.persistOption).toHaveBeenCalledWith('memory', true));
    expect(screen.getByTestId('session-value')).toHaveTextContent('"memory":true');
  });

  it('keeps the previous option value when persistence fails', async () => {
    mocks.persistOption.mockRejectedValueOnce(new Error('frame is archived'));
    await renderWithI18n(<Harness />);
    fireEvent.click(screen.getByRole('button', { name: '会话选项' }));

    fireEvent.click(await screen.findByRole('menuitemcheckbox', { name: '自动审阅' }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith('会话选项更新失败。'));
    expect(screen.getByTestId('session-value')).toHaveTextContent('"autoReview":false');
  });

  it('keeps manual review out of session settings', async () => {
    await renderWithI18n(<Harness />);
    fireEvent.click(screen.getByRole('button', { name: '会话选项' }));
    await screen.findByRole('menu', { name: '会话选项' });
    expect(screen.queryByRole('menuitem', { name: '手动审阅' })).not.toBeInTheDocument();
    expect(screen.queryByTestId('session-config-request-review')).not.toBeInTheDocument();
  });

  it('keeps task recovery out of session settings because the task status center owns it', async () => {
    await renderWithI18n(<Harness />);
    fireEvent.click(screen.getByRole('button', { name: '会话选项' }));

    await screen.findByRole('menu', { name: '会话选项' });
    expect(screen.queryByRole('menuitem', { name: '继续任务' })).not.toBeInTheDocument();
  });

  it('selects a specialist and changes a compute provider through real service boundaries', async () => {
    await renderWithI18n(<Harness />);
    fireEvent.click(screen.getByRole('button', { name: '会话选项' }));

    fireEvent.mouseEnter(screen.getByTestId('session-config-row-specialist'));
    fireEvent.click((await screen.findByText('AI 药物研发专家')).closest('[role="menuitemradio"]')!);
    expect(screen.getByTestId('session-value')).toHaveTextContent('"targetAgent":"AIDD_EXPERT"');

    fireEvent.mouseEnter(screen.getByTestId('session-config-row-compute'));
    const computeToggle = (await screen.findByText('HPC A')).parentElement?.querySelector('[role="menuitemcheckbox"]');
    if (!computeToggle) throw new Error('compute provider toggle is unavailable');
    fireEvent.click(computeToggle);
    await waitFor(() => expect(mocks.setSessionProvider).toHaveBeenCalledWith('frame-stat6', 'ssh:hpc-a', true));
  });

  it('keeps the expert submenu open while the pointer crosses from the parent menu', async () => {
    await renderWithI18n(<Harness />);
    fireEvent.click(screen.getByRole('button', { name: '会话选项' }));

    const parentMenu = await screen.findByRole('menu', { name: '会话选项' });
    fireEvent.mouseEnter(screen.getByTestId('session-config-row-specialist'));
    const expertMenu = await screen.findByRole('menu', { name: '专家' });

    fireEvent.mouseLeave(parentMenu);
    fireEvent.mouseEnter(expertMenu);

    await act(async () => new Promise((resolve) => window.setTimeout(resolve, 180)));
    expect(screen.getByRole('menu', { name: '专家' })).toBeInTheDocument();

    fireEvent.mouseLeave(expertMenu);
    await waitFor(() => expect(screen.queryByRole('menu', { name: '专家' })).not.toBeInTheDocument());
  });

  it('shows a truthful empty state when no compute provider is enabled', async () => {
    mocks.loadProviders.mockResolvedValue([]);
    mocks.loadSessionProviders.mockResolvedValue([]);
    await renderWithI18n(<Harness />);

    fireEvent.click(screen.getByRole('button', { name: '会话选项' }));
    fireEvent.mouseEnter(screen.getByTestId('session-config-row-compute'));
    expect(await screen.findByText('尚未配置额外计算资源；任务仍可使用本机')).toBeInTheDocument();
    expect(screen.getByRole('menuitemradio', { name: '本机' })).toHaveAttribute('aria-checked', 'true');
  });

  it('renders session controls in English', async () => {
    await renderWithI18n(<Harness />, 'en-US');
    fireEvent.click(screen.getByRole('button', { name: 'Conversation options' }));

    const menu = await screen.findByRole('menu', { name: 'Conversation options' });
    expect(menu).toHaveTextContent('DelegationAuto reviewMemoryExpertNoneComputeLocal');
    expect(screen.getByRole('menuitemcheckbox', { name: 'Delegation' })).toBeInTheDocument();
    fireEvent.mouseEnter(screen.getByTestId('session-config-row-specialist'));
    expect(await screen.findByText('AI Drug Discovery Expert')).toBeInTheDocument();
  });
});
