import type { IMessageText } from '@/common/chat/chatLib';
import SynonBiomedUserMessageActions from '@/renderer/pages/conversation/Messages/components/SynonBiomedUserMessageActions';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const branchMocks = vi.hoisted(() => ({
  fork: vi.fn(),
  select: vi.fn(),
  ensureRuntime: vi.fn(),
  messageSuccess: vi.fn(),
  messageError: vi.fn(),
  createMutation: vi.fn(() => 'branch-mutation-1'),
  capability: { state: 'supported' as 'supported' | 'unsupported', reason: '' },
  runtime: {
    isProcessing: false,
    markSendStarted: vi.fn(),
    markSendAccepted: vi.fn(),
    markSendFailed: vi.fn(),
  },
}));

vi.mock('@/renderer/services/synonBiomedConversationBranches', () => ({
  SYNON_BIOMED_SESSION_WORKFLOW_CAPABILITIES: {
    messageFork: branchMocks.capability,
    branchSelection: { state: 'supported' },
  },
  forkSynonBiomedUserMessage: branchMocks.fork,
  createSynonBiomedBranchMutationId: branchMocks.createMutation,
  selectSynonBiomedBranch: branchMocks.select,
}));
vi.mock('@/renderer/pages/conversation/utils/ensureConversationRuntime', () => ({
  ensureConversationRuntime: branchMocks.ensureRuntime,
}));
vi.mock('@/renderer/pages/conversation/runtime/useConversationRuntimeView', () => ({
  useConversationRuntimeView: () => branchMocks.runtime,
}));
vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: {
      success: branchMocks.messageSuccess,
      error: branchMocks.messageError,
    },
  };
});

const message: IMessageText = {
  id: 'message-1',
  msg_id: 'message-1',
  conversation_id: 'conversation-1',
  type: 'text',
  position: 'right',
  content: {
    content: 'Original prompt',
    synonBiomed: { messageIndex: 4, blockIndex: 0, branchId: 'br_00000001' },
  },
  created_at: 1,
};

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
};

describe('SynonBiomedUserMessageActions', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    branchMocks.capability.state = 'supported';
    branchMocks.capability.reason = '';
    branchMocks.runtime.isProcessing = false;
    branchMocks.fork.mockResolvedValue({
      rootFrameId: 'conversation-1',
      branchId: 'br_00000002',
      status: 'accepted',
    });
    branchMocks.ensureRuntime.mockResolvedValue({
      runtime: {
        state: 'running',
        is_processing: true,
        turn_id: 'branch-edited',
      },
    });
  });

  it('edits a historical prompt and creates a branch when the canonical capability is enabled', async () => {
    await renderWithI18n(
      <SynonBiomedUserMessageActions message={message}>
        <span data-testid='original-message'>Original prompt</span>
      </SynonBiomedUserMessageActions>,
      'en-US'
    );

    const messageRow = screen.getByTestId('user-message-action-row');
    const editAction = screen.getByRole('button', { name: 'Edit message' });
    const originalMessage = screen.getByTestId('original-message');
    expect(messageRow).toHaveClass('synon-biomed-user-message__row');
    expect(editAction).toHaveClass('synon-biomed-user-message__edit-action');
    expect(editAction).toHaveAttribute('title', 'Edit message');
    expect(editAction.compareDocumentPosition(originalMessage) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.queryByTestId('user-message-detached-actions')).not.toBeInTheDocument();

    fireEvent.click(editAction);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(screen.queryByTestId('original-message')).not.toBeInTheDocument();
    const inlineEditor = screen.getByTestId('inline-user-message-editor');
    expect(inlineEditor).toHaveClass('synon-biomed-user-message__editor');
    expect(inlineEditor).not.toHaveAttribute('style');
    const editor = screen.getByRole('textbox', { name: 'Edit previous user message' });
    expect(editor).toHaveClass('synon-biomed-user-message__textarea');
    fireEvent.change(editor, {
      target: { value: 'Revised prompt' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Save and submit' }));

    await waitFor(() =>
      expect(branchMocks.fork).toHaveBeenCalledWith({
        rootFrameId: 'conversation-1',
        messageIndex: 4,
        editedContent: 'Revised prompt',
        sourceBranchId: 'br_00000001',
        sourceClientMessageId: 'message-1',
        clientMutationId: 'branch-mutation-1',
      })
    );
    expect(branchMocks.select).toHaveBeenCalledWith('conversation-1', 'br_00000002');
    expect(branchMocks.runtime.markSendAccepted).toHaveBeenCalledWith(
      'branch-edited',
      expect.objectContaining({ state: 'running' })
    );
    expect(branchMocks.ensureRuntime.mock.invocationCallOrder[0]).toBeLessThan(
      branchMocks.runtime.markSendAccepted.mock.invocationCallOrder[0]
    );
    expect(branchMocks.runtime.markSendAccepted.mock.invocationCallOrder[0]).toBeLessThan(
      branchMocks.select.mock.invocationCallOrder[0]
    );
  });

  it('uses the persisted client message id when branching from an edited branch message', async () => {
    const editedBranchMessage: IMessageText = {
      ...message,
      id: 'branch-edit:br_00000002',
      msg_id: 'branch-edit:mutation-0002',
      content: {
        content: 'First branch prompt',
        synonBiomed: { messageIndex: 0, blockIndex: 0, branchId: 'br_00000002' },
      },
    };
    await renderWithI18n(<SynonBiomedUserMessageActions message={editedBranchMessage} />, 'en-US');

    fireEvent.click(screen.getByRole('button', { name: 'Edit message' }));
    fireEvent.change(screen.getByRole('textbox', { name: 'Edit previous user message' }), {
      target: { value: 'Second branch prompt' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Save and submit' }));

    await waitFor(() =>
      expect(branchMocks.fork).toHaveBeenCalledWith(
        expect.objectContaining({
          rootFrameId: 'conversation-1',
          sourceBranchId: 'br_00000002',
          sourceClientMessageId: 'branch-edit:mutation-0002',
          editedContent: 'Second branch prompt',
        })
      )
    );
  });

  it('does not apply an old message fork after the conversation owner changes', async () => {
    const fork = deferred<{ rootFrameId: string; branchId: string; status: string }>();
    branchMocks.fork.mockReturnValue(fork.promise);
    const view = await renderWithI18n(<SynonBiomedUserMessageActions message={message} />, 'en-US');
    fireEvent.click(screen.getByRole('button', { name: 'Edit message' }));
    fireEvent.change(screen.getByRole('textbox', { name: 'Edit previous user message' }), {
      target: { value: 'Revised prompt' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Save and submit' }));
    await waitFor(() => expect(branchMocks.fork).toHaveBeenCalledOnce());

    view.rerender(
      <SynonBiomedUserMessageActions
        message={{ ...message, id: 'message-2', msg_id: 'message-2', conversation_id: 'conversation-2' }}
      />
    );
    expect(screen.queryByRole('textbox', { name: 'Edit previous user message' })).not.toBeInTheDocument();
    await act(async () => {
      fork.resolve({ rootFrameId: 'conversation-1', branchId: 'branch-stale', status: 'accepted' });
      await fork.promise;
    });

    expect(branchMocks.select).not.toHaveBeenCalled();
    expect(branchMocks.ensureRuntime).toHaveBeenCalledWith('conversation-1');
    expect(branchMocks.runtime.markSendAccepted).toHaveBeenCalledWith(
      'branch-edited',
      expect.objectContaining({ state: 'running' })
    );
    expect(branchMocks.messageSuccess).not.toHaveBeenCalled();
  });

  it('reuses the same mutation identity when a response may have been lost', async () => {
    branchMocks.fork
      .mockRejectedValueOnce(new Error('network response unavailable'))
      .mockResolvedValueOnce({ rootFrameId: 'conversation-1', branchId: 'br_00000002', status: 'accepted' });
    await renderWithI18n(<SynonBiomedUserMessageActions message={message} />, 'en-US');
    fireEvent.click(screen.getByRole('button', { name: 'Edit message' }));
    fireEvent.change(screen.getByRole('textbox', { name: 'Edit previous user message' }), {
      target: { value: 'Revised prompt' },
    });

    const createBranch = screen.getByRole('button', { name: 'Save and submit' });
    fireEvent.click(createBranch);
    await waitFor(() => expect(branchMocks.fork).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(createBranch).toBeEnabled());
    fireEvent.click(createBranch);
    await waitFor(() => expect(branchMocks.fork).toHaveBeenCalledTimes(2));

    expect(branchMocks.fork.mock.calls[0][0].clientMutationId).toBe('branch-mutation-1');
    expect(branchMocks.fork.mock.calls[1][0].clientMutationId).toBe('branch-mutation-1');
    expect(branchMocks.createMutation).toHaveBeenCalledTimes(1);
  });

  it('uses the v1.1 inline keyboard contract and allows resubmitting unchanged non-empty text', async () => {
    await renderWithI18n(
      <SynonBiomedUserMessageActions message={message}>
        <span>Original prompt</span>
      </SynonBiomedUserMessageActions>,
      'en-US'
    );

    fireEvent.click(screen.getByRole('button', { name: 'Edit message' }));
    const editor = screen.getByRole('textbox', { name: 'Edit previous user message' });
    fireEvent.keyDown(editor, { key: 'Enter', shiftKey: true });
    expect(branchMocks.fork).not.toHaveBeenCalled();

    fireEvent.keyDown(editor, { key: 'Enter' });
    await waitFor(() => expect(branchMocks.fork).toHaveBeenCalledOnce());
    expect(branchMocks.fork.mock.calls[0][0].editedContent).toBe('Original prompt');
  });

  it('cancels inline editing with Escape and restores the original message', async () => {
    await renderWithI18n(
      <SynonBiomedUserMessageActions message={message}>
        <span>Original prompt</span>
      </SynonBiomedUserMessageActions>,
      'en-US'
    );

    fireEvent.click(screen.getByRole('button', { name: 'Edit message' }));
    const editor = screen.getByRole('textbox', { name: 'Edit previous user message' });
    fireEvent.change(editor, { target: { value: 'Discard me' } });
    fireEvent.keyDown(editor, { key: 'Escape' });

    expect(screen.queryByRole('textbox', { name: 'Edit previous user message' })).not.toBeInTheDocument();
    expect(screen.getByText('Original prompt')).toBeInTheDocument();
    expect(branchMocks.fork).not.toHaveBeenCalled();
  });

  it('shows previous and next controls only at a real fork point', async () => {
    await renderWithI18n(
      <SynonBiomedUserMessageActions
        message={message}
        branchState={{
          activeBranchId: 'br_00000003',
          selectedBranchId: 'br_00000001',
          generation: 3,
          branches: [
            {
              id: 'br_00000001',
              parentId: null,
              forkPoint: null,
              createdAt: '2026-01-01T00:00:00Z',
              updatedAt: null,
              active: false,
            },
            {
              id: 'br_00000002',
              parentId: 'br_00000001',
              forkPoint: 4,
              createdAt: '2026-01-02T00:00:00Z',
              updatedAt: null,
              active: false,
            },
            {
              id: 'br_00000003',
              parentId: 'br_00000001',
              forkPoint: 4,
              createdAt: '2026-01-03T00:00:00Z',
              updatedAt: null,
              active: true,
            },
          ],
        }}
      >
        <span>Original prompt</span>
      </SynonBiomedUserMessageActions>,
      'en-US'
    );

    expect(screen.getByText('1 / 3')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Previous branch' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: 'Next branch' }));
    expect(branchMocks.select).toHaveBeenCalledWith('conversation-1', 'br_00000002');
  });

  it('keeps same-index forks from different ancestors in separate navigation groups', async () => {
    await renderWithI18n(
      <SynonBiomedUserMessageActions
        message={{
          ...message,
          content: {
            ...message.content,
            synonBiomed: { messageIndex: 1, blockIndex: 0, branchId: 'br_00000004' },
          },
        }}
        branchState={{
          activeBranchId: 'br_00000004',
          selectedBranchId: 'br_00000004',
          generation: 5,
          branches: [
            {
              id: 'br_00000001',
              parentId: null,
              forkPoint: null,
              createdAt: '2026-01-01T00:00:00Z',
              updatedAt: null,
              active: false,
            },
            {
              id: 'br_00000002',
              parentId: 'br_00000001',
              forkPoint: 0,
              createdAt: '2026-01-02T00:00:00Z',
              updatedAt: null,
              active: false,
            },
            {
              id: 'br_00000003',
              parentId: 'br_00000001',
              forkPoint: 0,
              createdAt: '2026-01-03T00:00:00Z',
              updatedAt: null,
              active: false,
            },
            {
              id: 'br_00000004',
              parentId: 'br_00000002',
              forkPoint: 1,
              createdAt: '2026-01-04T00:00:00Z',
              updatedAt: null,
              active: true,
            },
            {
              id: 'br_00000005',
              parentId: 'br_00000003',
              forkPoint: 1,
              createdAt: '2026-01-05T00:00:00Z',
              updatedAt: null,
              active: false,
            },
          ],
        }}
      >
        <span>Branch A message</span>
      </SynonBiomedUserMessageActions>,
      'en-US'
    );

    expect(screen.getByText('2 / 2')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Next branch' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: 'Previous branch' }));
    expect(branchMocks.select).toHaveBeenCalledWith('conversation-1', 'br_00000002');
    expect(branchMocks.select).not.toHaveBeenCalledWith('conversation-1', 'br_00000003');
    expect(branchMocks.select).not.toHaveBeenCalledWith('conversation-1', 'br_00000005');
  });

  it('uses the matching ancestor as the current choice for an inherited fork point', async () => {
    await renderWithI18n(
      <SynonBiomedUserMessageActions
        message={{
          ...message,
          content: {
            ...message.content,
            synonBiomed: { messageIndex: 0, blockIndex: 0, branchId: 'br_00000004' },
          },
        }}
        branchState={{
          activeBranchId: 'br_00000004',
          selectedBranchId: 'br_00000004',
          generation: 4,
          branches: [
            {
              id: 'br_00000001',
              parentId: null,
              forkPoint: null,
              createdAt: '2026-01-01T00:00:00Z',
              updatedAt: null,
              active: false,
            },
            {
              id: 'br_00000002',
              parentId: 'br_00000001',
              forkPoint: 0,
              createdAt: '2026-01-02T00:00:00Z',
              updatedAt: null,
              active: false,
            },
            {
              id: 'br_00000003',
              parentId: 'br_00000001',
              forkPoint: 0,
              createdAt: '2026-01-03T00:00:00Z',
              updatedAt: null,
              active: false,
            },
            {
              id: 'br_00000004',
              parentId: 'br_00000002',
              forkPoint: 1,
              createdAt: '2026-01-04T00:00:00Z',
              updatedAt: null,
              active: true,
            },
          ],
        }}
      >
        <span>Inherited branch A prompt</span>
      </SynonBiomedUserMessageActions>,
      'en-US'
    );

    expect(screen.getByText('2 / 3')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Previous branch' }));
    fireEvent.click(screen.getByRole('button', { name: 'Next branch' }));
    expect(branchMocks.select).toHaveBeenNthCalledWith(1, 'conversation-1', 'br_00000001');
    expect(branchMocks.select).toHaveBeenNthCalledWith(2, 'conversation-1', 'br_00000003');
    expect(branchMocks.select).not.toHaveBeenCalledWith('conversation-1', 'br_00000004');
  });

  it('does not expose an edit-and-branch action while the canonical Transcript authority is unavailable', async () => {
    branchMocks.capability.state = 'unsupported';
    branchMocks.capability.reason = 'Transcript branch authority is unavailable.';
    await renderWithI18n(<SynonBiomedUserMessageActions message={message} />, 'en-US');

    expect(screen.queryByRole('button', { name: 'Edit message' })).not.toBeInTheDocument();
    expect(branchMocks.fork).not.toHaveBeenCalled();
  });
});
