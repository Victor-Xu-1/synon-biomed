/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { fireEvent, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import React from 'react';
import AgentModeSelector from '@/renderer/components/synonBiomed/runtime/SynonBiomedModeSelector';
import { renderWithI18n } from '../i18nTestUtils';

const { useAcpConfigOptionsMock } = vi.hoisted(() => ({
  useAcpConfigOptionsMock: vi.fn(),
}));

vi.mock('@/renderer/hooks/synonBiomed/runtime/useAcpConfigOptions', () => ({
  classifyConfigSetError: () => 'unknown',
  useAcpConfigOptions: useAcpConfigOptionsMock,
}));

vi.mock('@/renderer/hooks/context/LayoutContext', () => ({
  useLayoutContext: () => ({ isMobile: false }),
}));

vi.mock('@/renderer/components/synonBiomed/runtime/MarqueePillLabel', () => ({
  default: ({ children }: { children?: React.ReactNode }) => <span>{children}</span>,
}));

const menuContext = React.createContext<((key: string) => void) | null>(null);

vi.mock('@icon-park/react', () => ({
  Down: () => <span aria-hidden='true'>v</span>,
  Loading: ({ className }: { className?: string }) => <span aria-hidden='true' className={className} />,
  Robot: ({ className }: { className?: string }) => <span aria-hidden='true' className={className} />,
}));

vi.mock('@arco-design/web-react', () => {
  const Menu = Object.assign(
    ({ children, onClickMenuItem }: { children?: React.ReactNode; onClickMenuItem?: (key: string) => void }) => (
      <menuContext.Provider value={onClickMenuItem ?? null}>{children}</menuContext.Provider>
    ),
    {
      ItemGroup: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
      Item: ({ children }: { children?: React.ReactNode }) => {
        const onClickMenuItem = React.useContext(menuContext);
        const child = React.isValidElement(children) ? children : null;
        const itemKey = child?.props?.['data-mode-value'] as string | undefined;
        return (
          <button type='button' onClick={() => itemKey && onClickMenuItem?.(itemKey)}>
            {children}
          </button>
        );
      },
    }
  );
  return {
    Button: ({ children, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement>) => (
      <button type='button' {...props}>
        {children}
      </button>
    ),
    Dropdown: ({ children, droplist }: { children?: React.ReactNode; droplist?: React.ReactNode }) => (
      <>
        {children}
        {droplist}
      </>
    ),
    Menu,
    Message: {
      success: vi.fn(),
      error: vi.fn(),
    },
    Tooltip: ({ children, content }: { children?: React.ReactNode; content?: React.ReactNode }) => (
      <span data-tooltip-content={typeof content === 'string' ? content : undefined}>{children}</span>
    ),
  };
});

const runtimeMode = () => ({
  id: 'mode',
  category: 'mode',
  currentValue: 'default',
  options: [
    { value: 'default', label: 'Default', description: 'Ask before sensitive changes' },
    { value: 'bypassPermissions', label: 'Bypass Permissions', description: 'Run without permission prompts' },
  ],
});

describe('AgentModeSelector', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAcpConfigOptionsMock.mockImplementation(() => ({
      setStatus: { state: 'idle' },
      mode: runtimeMode(),
      model: null,
      thoughtLevel: null,
      reload: vi.fn(),
      setConfigOption: vi.fn(),
    }));
  });

  it('keeps observed runtime mode after rerender when initialMode is stale', async () => {
    const { rerender } = await renderWithI18n(
      <AgentModeSelector
        backend='claude'
        conversation_id='conv-1'
        compact
        initialMode='bypassPermissions'
        modeLabelFormatter={(mode) => (mode.value === 'default' ? '默认' : '全自动')}
        compactLabelPrefix='权限'
      />,
      'en-US'
    );

    await waitFor(() => expect(screen.getByTestId('mode-selector')).toHaveAttribute('data-current-mode', 'default'));

    rerender(
      <AgentModeSelector
        backend='claude'
        conversation_id='conv-1'
        compact
        initialMode='bypassPermissions'
        modeLabelFormatter={(mode) => (mode.value === 'default' ? '默认' : '全自动')}
        compactLabelPrefix='权限'
      />
    );

    await waitFor(() => expect(screen.getByTestId('mode-selector')).toHaveAttribute('data-current-mode', 'default'));
    expect(screen.getByText('权限 · 默认')).toBeInTheDocument();
  });

  it('renders setting progress at the compact trailing edge instead of using Arco button loading', async () => {
    useAcpConfigOptionsMock.mockImplementation(() => ({
      setStatus: { state: 'setting' },
      mode: runtimeMode(),
      model: null,
      thoughtLevel: null,
      reload: vi.fn(),
      setConfigOption: vi.fn(),
    }));

    await renderWithI18n(
      <AgentModeSelector
        backend='claude'
        conversation_id='conv-1'
        compact
        modeLabelFormatter={(mode) => (mode.value === 'default' ? '默认' : '全自动')}
        compactLabelPrefix='权限'
      />,
      'en-US'
    );

    const button = screen.getByTestId('agent-mode-selector-claude');
    const loading = screen.getByTestId('runtime-selector-loading-indicator');

    expect(button).not.toHaveAttribute('loading');
    expect(button).toHaveTextContent('权限 · 默认');
    expect(loading.parentElement?.lastElementChild).toBe(loading);
  });

  it('does not persist runtime mode changes to global agent preferences', async () => {
    const setConfigOption = vi.fn().mockResolvedValue(undefined);
    useAcpConfigOptionsMock.mockImplementation(() => ({
      setStatus: { state: 'idle' },
      mode: runtimeMode(),
      model: null,
      thoughtLevel: null,
      reload: vi.fn(),
      setConfigOption,
    }));

    await renderWithI18n(<AgentModeSelector backend='claude' conversation_id='conv-1' />, 'en-US');

    fireEvent.click(screen.getByText('YOLO'));

    await waitFor(() => {
      expect(setConfigOption).toHaveBeenCalledWith('mode', 'bypassPermissions');
    });
  });

  it('shows runtime mode descriptions in option tooltips', async () => {
    await renderWithI18n(<AgentModeSelector backend='claude' conversation_id='conv-1' />, 'en-US');

    expect(screen.queryByText('Run without permission prompts')).not.toBeInTheDocument();
    expect(screen.getByText('YOLO').closest('[data-tooltip-content]')).toHaveAttribute(
      'data-tooltip-content',
      'Run without permission prompts'
    );
  });
});
