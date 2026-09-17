/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React from 'react';
import { fireEvent, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { ConversationCommandQueueItem } from '@/renderer/pages/conversation/platforms/useConversationCommandQueue';
import CommandQueuePanel from '@/renderer/components/chat/CommandQueuePanel';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@arco-design/web-react', () => {
  const Button = ({
    children,
    ...props
  }: React.PropsWithChildren<React.ButtonHTMLAttributes<HTMLButtonElement> & { status?: string }>) => (
    <button type='button' {...props}>
      {children}
    </button>
  );
  const Dropdown = ({ children, droplist }: React.PropsWithChildren<{ droplist: React.ReactNode }>) => (
    <div>
      {children}
      {droplist}
    </div>
  );
  const Menu = ({ children }: React.PropsWithChildren) => <div>{children}</div>;
  Menu.Item = ({
    children,
    onClick,
  }: React.PropsWithChildren<{
    onClick?: () => void;
  }>) => (
    <button type='button' onClick={onClick}>
      {children}
    </button>
  );
  const Typography = {
    Ellipsis: ({
      children,
      showTooltip: _showTooltip,
      ...props
    }: React.PropsWithChildren<{ showTooltip?: boolean }>) => <span {...props}>{children}</span>,
  };
  const Tooltip = ({ children }: React.PropsWithChildren) => <>{children}</>;
  return { Button, Dropdown, Menu, Tooltip, Typography };
});

vi.mock('@icon-park/react', () => ({
  Attention: () => <span data-testid='attention-icon' />,
  CornerDownRight: () => <span data-testid='corner-down-right-icon' />,
  Delete: () => <span data-testid='delete-icon' />,
  Drag: () => <span data-testid='drag-icon' />,
  EditOne: () => <span data-testid='edit-icon' />,
  MessageOne: () => <span data-testid='message-icon' />,
  MoreOne: () => <span data-testid='more-icon' />,
  PauseOne: () => <span data-testid='pause-icon' />,
  PlayOne: () => <span data-testid='play-icon' />,
  Redo: () => <span data-testid='redo-icon' />,
  Switch: () => <span data-testid='switch-icon' />,
}));

const item: ConversationCommandQueueItem = {
  id: 'queued-1',
  input: 'queued follow-up',
  files: [],
  contextItems: [],
  created_at: 1,
};

const renderPanel = async (overrides: Partial<React.ComponentProps<typeof CommandQueuePanel>> = {}) => {
  const props: React.ComponentProps<typeof CommandQueuePanel> = {
    items: [item],
    isInterrupted: false,
    isQueueingEnabled: true,
    isSendNowDisabled: false,
    onInteractionLock: vi.fn(),
    onInteractionUnlock: vi.fn(),
    onReorder: vi.fn(),
    onRemove: vi.fn(),
    onRetry: vi.fn(),
    onSendNow: vi.fn(),
    onQueueingChange: vi.fn(),
    onResumeInterruptedQueue: vi.fn(),
    ...overrides,
  };

  await renderWithI18n(<CommandQueuePanel {...props} />, 'en-US');
  return props;
};

describe('CommandQueuePanel', () => {
  it('does not render an interrupted resume control during ordinary queueing', async () => {
    await renderPanel();

    expect(screen.queryByText('Queue paused because you interrupted')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Resume' })).not.toBeInTheDocument();
  });

  it('keeps remove and steer callbacks wired', async () => {
    const onRemove = vi.fn();
    const onSendNow = vi.fn();
    await renderPanel({ onRemove, onSendNow });

    fireEvent.click(screen.getByRole('button', { name: 'Delete queued message' }));
    fireEvent.click(screen.getByRole('button', { name: 'Steer: queued follow-up' }));

    expect(onRemove).toHaveBeenCalledExactlyOnceWith('queued-1');
    expect(onSendNow).toHaveBeenCalledExactlyOnceWith('queued-1');
  });

  it('keeps queued-message editing in the compact actions menu', async () => {
    const onEdit = vi.fn();
    await renderPanel({ onEdit });

    fireEvent.click(screen.getByRole('button', { name: 'Edit message' }));

    expect(onEdit).toHaveBeenCalledExactlyOnceWith(item);
  });

  it('hides the empty queue while the current task is running', async () => {
    await renderPanel({ items: [] });

    expect(screen.queryByTestId('conversation-command-queue')).not.toBeInTheDocument();
    expect(screen.queryByText('Task running')).not.toBeInTheDocument();
    expect(screen.queryByText('Ready for another task')).not.toBeInTheDocument();
  });

  it('renders queued tasks without an exposed status header', async () => {
    await renderPanel();

    expect(screen.getByTestId('conversation-command-queue')).toBeInTheDocument();
    expect(screen.getByText('queued follow-up')).toBeInTheDocument();
    expect(screen.queryByText('Task running')).not.toBeInTheDocument();
    expect(screen.queryByText('Ready for another task')).not.toBeInTheDocument();
  });

  it('can steer a selected queued task without changing the queue order in the view', async () => {
    const secondItem = { ...item, id: 'queued-2', input: 'urgent follow-up' };
    const onSendNow = vi.fn();
    await renderPanel({ items: [item, secondItem], onSendNow });

    fireEvent.click(screen.getByRole('button', { name: 'Steer: urgent follow-up' }));

    expect(onSendNow).toHaveBeenCalledExactlyOnceWith('queued-2');
  });

  it('shows an explicit resume action only after the user interrupted a task', async () => {
    const onResumeInterruptedQueue = vi.fn();
    await renderPanel({ isInterrupted: true, onResumeInterruptedQueue });

    fireEvent.click(screen.getByRole('button', { name: 'Resume' }));
    expect(onResumeInterruptedQueue).toHaveBeenCalledTimes(1);
  });
});
