import { act, fireEvent, screen } from '@testing-library/react';
import { Message } from '@arco-design/web-react';
import type { TChatConversation } from '@/common/config/storage';
import type { ConversationRowProps } from '@/renderer/pages/conversation/GroupedHistory/types';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const { copyTextMock } = vi.hoisted(() => ({ copyTextMock: vi.fn(async () => undefined) }));
vi.mock('@/renderer/utils/ui/clipboard', () => ({ copyText: copyTextMock }));

vi.mock('@/renderer/hooks/context/LayoutContext', () => ({
  useLayoutContext: () => ({ isMobile: true }),
}));

vi.mock('@/renderer/hooks/synonBiomed/runtime/usePresetAssistantInfo', () => ({
  usePresetAssistantInfo: () => ({ info: null }),
}));

vi.mock('@/renderer/utils/synonBiomed/runtime/runtimeLogo', () => ({
  useAgentLogos: () => ({}),
}));

import ConversationRow, {
  CONVERSATION_MENU_BOUNDARY_DISTANCE,
  CONVERSATION_PREFETCH_INTENT_MS,
  resolveConversationTaskIndicatorState,
} from '@/renderer/pages/conversation/GroupedHistory/ConversationRow';

describe('ConversationRow v1.1 session actions', () => {
  it('keeps the essential conversation actions and removes obsolete secondary actions', async () => {
    const successMessage = vi.spyOn(Message, 'success').mockImplementation(() => () => undefined);
    const callbacks = {
      onConversationPrefetch: vi.fn(),
      onEditStart: vi.fn(),
      onMove: vi.fn(),
      onExport: vi.fn(),
      onDownloadArtifacts: vi.fn(),
      onViewNotebook: vi.fn(),
      onDelete: vi.fn(),
    };
    const conversation: TChatConversation = {
      id: 'frame-1',
      type: 'acp',
      name: 'STAT6 analysis',
      desc: 'Structure workflow',
      created_at: 1,
      modified_at: 1,
      status: 'finished',
      extra: { backend: 'synonbiomed', project_id: 'project-a' },
    };

    const props: ConversationRowProps = {
      conversation,
      isGenerating: false,
      hasCompletionUnread: false,
      collapsed: false,
      tooltipEnabled: false,
      batchMode: false,
      checked: false,
      selected: false,
      menuVisible: true,
      onToggleChecked: vi.fn(),
      onConversationClick: vi.fn(),
      onOpenMenu: vi.fn(),
      onMenuVisibleChange: vi.fn(),
      ...callbacks,
    };

    const { i18n } = await renderWithI18n(<ConversationRow {...props} />);

    expect(await screen.findByText('重命名')).toBeInTheDocument();
    expect(screen.queryByText('任务 ID')).not.toBeInTheDocument();
    expect(screen.queryByText('frame-1')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('menuitem', { name: '复制任务 ID' }));
    await vi.waitFor(() => {
      expect(copyTextMock).toHaveBeenCalledWith('frame-1');
      expect(successMessage).toHaveBeenCalledWith('任务 ID 已复制');
    });
    expect(screen.getByText('移动到项目')).toBeInTheDocument();
    expect(screen.getByText('下载全部产物')).toBeInTheDocument();
    expect(screen.queryByText('导出')).not.toBeInTheDocument();
    expect(screen.queryByText('查看 notebook')).not.toBeInTheDocument();
    expect(screen.queryByText('笔记')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('menuitem', { name: '删除' }));
    expect(callbacks.onDelete).toHaveBeenCalledWith('frame-1');
    expect(screen.queryByText('conversation.history.pin')).not.toBeInTheDocument();

    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    expect(screen.getByText('Rename')).toBeInTheDocument();
    expect(screen.getByText('Move to project')).toBeInTheDocument();
    expect(screen.getByText('Download all artifacts')).toBeInTheDocument();
    expect(screen.queryByText('Export')).not.toBeInTheDocument();
    expect(screen.queryByText('View notebook')).not.toBeInTheDocument();
    expect(screen.queryByText('Notes')).not.toBeInTheDocument();
    expect(screen.getByText('Delete')).toBeInTheDocument();
  });

  it('uses the Codex-style running ring and lets persisted terminal states win immediately', async () => {
    const conversation: TChatConversation = {
      id: 'frame-running',
      type: 'acp',
      name: 'Running analysis',
      desc: 'Structure workflow',
      created_at: 1,
      modified_at: 1,
      status: 'running',
      extra: {
        backend: 'synonbiomed',
        project_id: 'project-a',
        frame_status: 'running',
      },
    };
    const props: ConversationRowProps = {
      conversation,
      isGenerating: false,
      hasCompletionUnread: false,
      collapsed: false,
      tooltipEnabled: false,
      batchMode: false,
      checked: false,
      selected: false,
      menuVisible: false,
      onToggleChecked: vi.fn(),
      onConversationPrefetch: vi.fn(),
      onConversationClick: vi.fn(),
      onOpenMenu: vi.fn(),
      onMenuVisibleChange: vi.fn(),
      onEditStart: vi.fn(),
      onMove: vi.fn(),
      onDelete: vi.fn(),
      onExport: vi.fn(),
      onDownloadArtifacts: vi.fn(),
      onViewNotebook: vi.fn(),
    };

    const { rerender } = await renderWithI18n(<ConversationRow {...props} />);
    const running = screen.getByTestId('conversation-running-frame-running');
    expect(running).toHaveAttribute('role', 'status');
    const runningRing = running.querySelector<HTMLElement>('.animate-spin.rounded-full');
    expect(runningRing?.style.backgroundImage).toContain('conic-gradient');
    expect(running.querySelector('svg')).toBeNull();

    rerender(
      <ConversationRow
        {...props}
        conversation={{
          ...conversation,
          status: 'finished',
          extra: { ...conversation.extra, frame_status: 'completed' },
        }}
      />
    );
    expect(screen.queryByTestId('conversation-running-frame-running')).not.toBeInTheDocument();
    expect(screen.getByTestId('conversation-success-frame-running')).toBeInTheDocument();

    rerender(<ConversationRow {...props} conversation={{ ...conversation, status: 'finished' }} isGenerating />);
    expect(screen.queryByTestId('conversation-running-frame-running')).not.toBeInTheDocument();
    expect(screen.getByTestId('conversation-success-frame-running')).toBeInTheDocument();

    rerender(
      <ConversationRow
        {...props}
        conversation={{
          ...conversation,
          status: 'pending',
          runtime: {
            state: 'paused',
            can_send_message: true,
            has_task: true,
            task_status: 'pending',
            is_processing: false,
            pending_confirmations: 0,
            turn_id: conversation.id,
          },
          extra: { ...conversation.extra, frame_status: 'cancelled' },
        }}
      />
    );
    expect(screen.queryByTestId('conversation-attention-frame-running')).not.toBeInTheDocument();
    expect(screen.getByTestId('conversation-paused-frame-running')).toHaveAttribute('role', 'status');

    rerender(
      <ConversationRow
        {...props}
        conversation={{
          ...conversation,
          status: 'pending',
          runtime: {
            state: 'waiting_input',
            can_send_message: true,
            has_task: true,
            task_status: 'pending',
            is_processing: false,
            pending_confirmations: 0,
            turn_id: conversation.id,
          },
        }}
      />
    );
    expect(screen.getByTestId('conversation-running-frame-running')).toBeInTheDocument();

    rerender(
      <ConversationRow
        {...props}
        conversation={{
          ...conversation,
          status: 'error',
          extra: { ...conversation.extra, frame_status: 'failed' },
        }}
        isGenerating
      />
    );
    expect(screen.queryByTestId('conversation-running-frame-running')).not.toBeInTheDocument();
    expect(screen.getByTestId('conversation-attention-frame-running')).toBeInTheDocument();
  });

  it('maps success to green and every failure or interruption terminal to orange', () => {
    const conversation: TChatConversation = {
      id: 'frame-state',
      type: 'acp',
      name: 'State mapping',
      desc: '',
      created_at: 1,
      modified_at: 1,
      status: 'running',
      extra: { backend: 'synonbiomed', frame_status: 'running' },
    };

    expect(resolveConversationTaskIndicatorState(conversation, false)).toBe('running');
    expect(
      resolveConversationTaskIndicatorState(
        {
          ...conversation,
          status: 'finished',
          extra: { ...conversation.extra, frame_status: 'completed' },
        },
        true
      )
    ).toBe('success');
    for (const frameStatus of ['failed', 'error', 'cancelled', 'canceled', 'interrupted', 'stopped']) {
      expect(
        resolveConversationTaskIndicatorState(
          {
            ...conversation,
            status: 'running',
            extra: { ...conversation.extra, frame_status: frameStatus },
          },
          true
        )
      ).toBe('attention');
    }
  });

  it('keeps every unfinished backend phase on the running indicator', () => {
    const waitingConversation: TChatConversation = {
      id: 'frame-waiting',
      type: 'acp',
      name: 'Waiting task',
      desc: '',
      created_at: 1,
      modified_at: 2,
      status: 'pending',
      runtime: {
        state: 'waiting_input',
        can_send_message: true,
        has_task: true,
        task_status: 'pending',
        is_processing: false,
        pending_confirmations: 0,
        turn_id: 'frame-waiting',
      },
      extra: {
        backend: 'synonbiomed',
        frame_status: 'interrupted',
      },
    };

    expect(resolveConversationTaskIndicatorState(waitingConversation, false)).toBe('running');
    expect(
      resolveConversationTaskIndicatorState(
        {
          ...waitingConversation,
          status: 'pending',
          runtime: {
            ...waitingConversation.runtime!,
            state: 'paused',
            can_send_message: true,
            has_task: true,
            task_status: 'pending',
          },
          extra: { ...waitingConversation.extra, frame_status: 'cancelled' },
        },
        false
      )
    ).toBe('paused');
    expect(
      resolveConversationTaskIndicatorState(
        {
          ...waitingConversation,
          runtime: undefined,
          extra: { ...waitingConversation.extra, frame_status: 'processing' },
        },
        false
      )
    ).toBe('running');
    expect(
      resolveConversationTaskIndicatorState(
        {
          ...waitingConversation,
          runtime: undefined,
          extra: { ...waitingConversation.extra, frame_status: 'queued' },
        },
        false
      )
    ).toBe('running');
  });

  it('exposes the conversation actions as a clamped opaque menu with keyboard semantics', async () => {
    expect(CONVERSATION_MENU_BOUNDARY_DISTANCE).toEqual({ left: 8, bottom: 8 });

    const onMenuVisibleChange = vi.fn();
    const conversation: TChatConversation = {
      id: 'frame-menu',
      type: 'acp',
      name: 'Menu target',
      desc: '',
      created_at: 1,
      modified_at: 1,
      status: 'finished',
      extra: { backend: 'synonbiomed' },
    };

    await renderWithI18n(
      <ConversationRow
        conversation={conversation}
        isGenerating={false}
        hasCompletionUnread={false}
        collapsed={false}
        tooltipEnabled={false}
        batchMode={false}
        checked={false}
        selected={false}
        menuVisible
        onToggleChecked={vi.fn()}
        onConversationPrefetch={vi.fn()}
        onConversationClick={vi.fn()}
        onOpenMenu={vi.fn()}
        onMenuVisibleChange={onMenuVisibleChange}
        onEditStart={vi.fn()}
        onMove={vi.fn()}
        onDelete={vi.fn()}
        onExport={vi.fn()}
        onDownloadArtifacts={vi.fn()}
        onViewNotebook={vi.fn()}
      />,
      'en-US'
    );

    const trigger = screen.getByTestId('conversation-row-menu-frame-menu');
    expect(trigger).toHaveAttribute('type', 'button');
    expect(trigger).toHaveAttribute('aria-haspopup', 'menu');
    expect(trigger).toHaveAttribute('aria-expanded', 'true');

    const menu = await screen.findByRole('menu', { name: 'More' });
    expect(menu).toHaveStyle({
      backgroundColor: '#fff',
      opacity: '1',
      maxWidth: 'calc(100vw - 16px)',
    });
    expect(screen.getByRole('menuitem', { name: 'Rename' })).toBeInTheDocument();
    fireEvent.keyDown(menu, { key: 'Escape' });
    expect(onMenuVisibleChange).toHaveBeenCalledWith('frame-menu', false);
  });

  it('prefetches only after sustained pointer intent and cancels fly-over requests', async () => {
    vi.useFakeTimers();
    try {
      const conversation: TChatConversation = {
        id: 'frame-prefetch',
        type: 'acp',
        name: 'Prefetch target',
        desc: '',
        created_at: 1,
        modified_at: 1,
        status: 'finished',
        extra: { backend: 'synonbiomed' },
      };
      const onConversationPrefetch = vi.fn();
      await renderWithI18n(
        <ConversationRow
          conversation={conversation}
          isGenerating={false}
          hasCompletionUnread={false}
          collapsed={false}
          tooltipEnabled={false}
          batchMode={false}
          checked={false}
          selected={false}
          menuVisible={false}
          onToggleChecked={vi.fn()}
          onConversationPrefetch={onConversationPrefetch}
          onConversationClick={vi.fn()}
          onOpenMenu={vi.fn()}
          onMenuVisibleChange={vi.fn()}
          onEditStart={vi.fn()}
          onMove={vi.fn()}
          onDelete={vi.fn()}
          onExport={vi.fn()}
          onDownloadArtifacts={vi.fn()}
          onViewNotebook={vi.fn()}
        />
      );
      const row = document.getElementById('c-frame-prefetch');
      expect(row).not.toBeNull();

      fireEvent.pointerEnter(row!);
      await act(async () => vi.advanceTimersByTimeAsync(CONVERSATION_PREFETCH_INTENT_MS - 1));
      expect(onConversationPrefetch).not.toHaveBeenCalled();
      fireEvent.pointerLeave(row!);
      await act(async () => vi.advanceTimersByTimeAsync(1));
      expect(onConversationPrefetch).not.toHaveBeenCalled();

      fireEvent.pointerEnter(row!);
      await act(async () => vi.advanceTimersByTimeAsync(CONVERSATION_PREFETCH_INTENT_MS));
      expect(onConversationPrefetch).toHaveBeenCalledOnce();
    } finally {
      vi.useRealTimers();
    }
  });
});
