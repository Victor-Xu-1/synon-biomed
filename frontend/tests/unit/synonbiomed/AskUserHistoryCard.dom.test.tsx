import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const {
  navigate,
  loadSnapshot,
  forkAtAnswer,
  selectBranch,
  messageSuccess,
  messageError,
  createMutation,
  branchCapability,
  ensureRuntime,
  runtimeView,
} = vi.hoisted(() => ({
  navigate: vi.fn(),
  loadSnapshot: vi.fn(),
  forkAtAnswer: vi.fn(),
  selectBranch: vi.fn(),
  messageSuccess: vi.fn(),
  messageError: vi.fn(),
  createMutation: vi.fn(() => 'ask-mutation-1'),
  branchCapability: { state: 'supported' as 'supported' | 'unsupported', reason: '' },
  ensureRuntime: vi.fn(),
  runtimeView: {
    isProcessing: false,
    markSendStarted: vi.fn(),
    markSendAccepted: vi.fn(),
    markSendFailed: vi.fn(),
  },
}));

vi.mock('react-router', async () => {
  const actual = await vi.importActual<typeof import('react-router')>('react-router');
  return { ...actual, useNavigate: () => navigate };
});
vi.mock('@arco-design/web-react', async () => {
  const actual = await vi.importActual<typeof import('@arco-design/web-react')>('@arco-design/web-react');
  return {
    ...actual,
    Message: {
      ...actual.Message,
      useMessage: () => [{ success: messageSuccess, error: messageError }, null],
    },
  };
});
vi.mock('@/renderer/services/synonBiomedRuntimeOperations', () => ({
  loadSynonBiomedRuntimeSnapshot: loadSnapshot,
  forkSynonBiomedAtAskUserAnswer: forkAtAnswer,
}));
vi.mock('@/renderer/services/synonBiomedConversationBranches', () => ({
  SYNON_BIOMED_SESSION_WORKFLOW_CAPABILITIES: {
    askUserAnswerFork: branchCapability,
  },
  selectSynonBiomedBranch: selectBranch,
  createSynonBiomedBranchMutationId: createMutation,
}));
vi.mock('@/renderer/pages/conversation/runtime/useConversationRuntimeView', () => ({
  useConversationRuntimeView: () => runtimeView,
}));
vi.mock('@/renderer/pages/conversation/utils/ensureConversationRuntime', () => ({
  ensureConversationRuntime: ensureRuntime,
}));
vi.mock('@rdkit/rdkit/dist/RDKit_minimal.js', () => ({
  default: vi.fn(async () => ({ get_mol: () => null })),
}));
vi.mock('@rdkit/rdkit/dist/RDKit_minimal.wasm?url', () => ({ default: '/rdkit/RDKit_minimal.wasm' }));

import AskUserHistoryCard from '@/renderer/components/synonBiomed/runtime/AskUserHistoryCard';
import { renderWithI18n } from '../i18nTestUtils';

const input = {
  header: 'Assay endpoint',
  question: 'Which endpoint should be primary?',
  options: [{ label: 'pIC50' }, { label: 'IC50' }],
  multi_select: false,
};

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
};

describe('AskUserHistoryCard', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    branchCapability.state = 'supported';
    branchCapability.reason = '';
    runtimeView.isProcessing = false;
    ensureRuntime.mockResolvedValue({ runtime: { state: 'running', is_processing: true, turn_id: 'turn-branch' } });
  });

  it('shows the historical answer and forks from the original tool use when changed', async () => {
    loadSnapshot.mockResolvedValue({ rootFrameId: 'frame-root' });
    forkAtAnswer.mockResolvedValue({ rootFrameId: 'frame-root', branchId: 'br_00000002', generation: 2 });
    await renderWithI18n(
      <AskUserHistoryCard
        conversationId='frame-source'
        sourceBranchId='br_00000001'
        toolUseId='toolu_ask_1'
        input={input}
        output={JSON.stringify({ status: 'answered', answers: { [input.question]: 'pIC50' } })}
      />
    );

    expect(screen.getByRole('region', { name: '历史回答' })).toHaveTextContent('pIC50');
    fireEvent.click(screen.getByRole('button', { name: '修改答案' }));
    expect(screen.getByText(/重新运行并创建新的会话分支/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('radio', { name: 'IC50' }));

    await waitFor(() =>
      expect(forkAtAnswer).toHaveBeenCalledWith({
        rootFrameId: 'frame-root',
        sourceFrameId: 'frame-source',
        sourceBranchId: 'br_00000001',
        toolUseId: 'toolu_ask_1',
        response: { action: 'answer', answers: { [input.question]: 'IC50' } },
        clientMutationId: 'ask-mutation-1',
      })
    );
    expect(selectBranch).toHaveBeenCalledWith('frame-source', 'br_00000002');
    expect(runtimeView.markSendStarted).toHaveBeenCalledOnce();
    expect(ensureRuntime).toHaveBeenCalledWith('frame-source');
    expect(runtimeView.markSendAccepted).toHaveBeenCalledWith('turn-branch', {
      state: 'running',
      is_processing: true,
      turn_id: 'turn-branch',
    });
    expect(runtimeView.markSendStarted.mock.invocationCallOrder[0]).toBeLessThan(
      forkAtAnswer.mock.invocationCallOrder[0] ?? 0
    );
    expect(forkAtAnswer.mock.invocationCallOrder[0]).toBeLessThan(ensureRuntime.mock.invocationCallOrder[0] ?? 0);
    expect(ensureRuntime.mock.invocationCallOrder[0]).toBeLessThan(
      runtimeView.markSendAccepted.mock.invocationCallOrder[0] ?? 0
    );
    expect(runtimeView.markSendAccepted.mock.invocationCallOrder[0]).toBeLessThan(
      selectBranch.mock.invocationCallOrder[0] ?? 0
    );
    expect(navigate).not.toHaveBeenCalled();
  });

  it('does not render pending history while the live request card owns the interaction', async () => {
    const { container } = await renderWithI18n(
      <AskUserHistoryCard
        conversationId='frame-source'
        sourceBranchId='br_00000001'
        toolUseId='toolu_ask_1'
        input={input}
        output={JSON.stringify({ status: 'awaiting_user_response' })}
      />
    );
    expect(container).toBeEmptyDOMElement();
  });

  it('does not select or notify when an old owner fork completes after a conversation switch', async () => {
    const fork = deferred<{ rootFrameId: string; branchId: string; generation: number }>();
    loadSnapshot.mockResolvedValue({ rootFrameId: 'frame-root' });
    forkAtAnswer.mockReturnValue(fork.promise);
    const view = await renderWithI18n(
      <AskUserHistoryCard
        conversationId='frame-source'
        sourceBranchId='br_00000001'
        toolUseId='toolu_ask_1'
        input={input}
        output={JSON.stringify({ status: 'answered', answers: { [input.question]: 'pIC50' } })}
      />
    );
    fireEvent.click(screen.getByRole('button', { name: '修改答案' }));
    fireEvent.click(screen.getByRole('radio', { name: 'IC50' }));
    await waitFor(() => expect(forkAtAnswer).toHaveBeenCalledOnce());

    view.rerender(
      <AskUserHistoryCard
        conversationId='frame-source'
        sourceBranchId='br_00000003'
        toolUseId='toolu_ask_1'
        input={input}
        output={JSON.stringify({ status: 'answered', answers: { [input.question]: 'pIC50' } })}
      />
    );
    expect(screen.queryByTestId('synon-biomed-ask-user-history-editing')).not.toBeInTheDocument();
    await act(async () => {
      fork.resolve({ rootFrameId: 'frame-root', branchId: 'br_00000003', generation: 3 });
      await fork.promise;
    });

    expect(navigate).not.toHaveBeenCalled();
    expect(selectBranch).not.toHaveBeenCalled();
    expect(runtimeView.markSendAccepted).toHaveBeenCalledOnce();
    expect(messageSuccess).not.toHaveBeenCalled();
    expect(messageError).not.toHaveBeenCalled();
  });

  it('does not start a branch mutation after snapshot loading loses owner authority', async () => {
    const snapshot = deferred<{ rootFrameId: string }>();
    loadSnapshot.mockReturnValue(snapshot.promise);
    const view = await renderWithI18n(
      <AskUserHistoryCard
        conversationId='frame-source'
        sourceBranchId='br_00000001'
        toolUseId='toolu_ask_1'
        input={input}
        output={JSON.stringify({ status: 'answered', answers: { [input.question]: 'pIC50' } })}
      />
    );
    fireEvent.click(screen.getByRole('button', { name: '修改答案' }));
    fireEvent.click(screen.getByRole('radio', { name: 'IC50' }));
    await waitFor(() => expect(loadSnapshot).toHaveBeenCalledWith('frame-source'));

    view.rerender(
      <AskUserHistoryCard
        conversationId='frame-replacement'
        sourceBranchId='br_00000003'
        toolUseId='toolu_ask_2'
        input={input}
        output={JSON.stringify({ status: 'answered', answers: { [input.question]: 'pIC50' } })}
      />
    );
    await act(async () => {
      snapshot.resolve({ rootFrameId: 'frame-root' });
      await snapshot.promise;
    });

    expect(forkAtAnswer).not.toHaveBeenCalled();
    expect(navigate).not.toHaveBeenCalled();
    expect(selectBranch).not.toHaveBeenCalled();
  });

  it('does not expose answer editing without an immutable canonical source branch', async () => {
    await renderWithI18n(
      <AskUserHistoryCard
        conversationId='frame-source'
        sourceBranchId={null}
        toolUseId='toolu_ask_1'
        input={input}
        output={JSON.stringify({ status: 'answered', answers: { [input.question]: 'pIC50' } })}
      />
    );

    expect(screen.getByRole('region', { name: '历史回答' })).toBeVisible();
    expect(screen.queryByRole('button', { name: '修改答案' })).not.toBeInTheDocument();
  });

  it('uses the compact borderless history surface shared by the conversation stream', async () => {
    await renderWithI18n(
      <AskUserHistoryCard
        conversationId='frame-source'
        sourceBranchId='br_00000001'
        toolUseId='toolu_ask_1'
        input={input}
        output={JSON.stringify({ version: 1, status: 'deferred', action: 'decide_for_me' })}
      />
    );

    const card = screen.getByRole('region', { name: '历史回答' });
    expect(card).toHaveClass('synon-biomed-ask-user-history');
    expect(card.className).not.toMatch(/(?:^|\s)border(?:\s|$)/);
    expect(card.className).not.toMatch(/(?:^|\s)border-solid(?:\s|$)/);
    expect(within(card).getByTestId('synon-biomed-ask-user-history-answer')).toHaveTextContent(
      '已交由 Synon Biomed 决定'
    );
  });

  it('redacts branch failure diagnostics while keeping the editor recoverable', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    loadSnapshot.mockResolvedValue({ rootFrameId: 'frame-root' });
    forkAtAnswer.mockRejectedValue(
      new Error('fork rejected sk-live-secret-value for alice@example.com token=private-token-value')
    );
    try {
      await renderWithI18n(
        <AskUserHistoryCard
          conversationId='frame-source'
          sourceBranchId='br_00000001'
          toolUseId='toolu_ask_1'
          input={input}
          output={JSON.stringify({ status: 'answered', answers: { [input.question]: 'pIC50' } })}
        />
      );
      fireEvent.click(screen.getByRole('button', { name: '修改答案' }));
      fireEvent.click(screen.getByRole('radio', { name: 'IC50' }));
      await waitFor(() => expect(forkAtAnswer).toHaveBeenCalledOnce());

      const diagnostic = JSON.stringify(consoleError.mock.calls);
      expect(diagnostic).toContain('[REDACTED_KEY]');
      expect(diagnostic).toContain('[email]');
      expect(diagnostic).not.toContain('live-secret-value');
      expect(diagnostic).not.toContain('alice@example.com');
      expect(diagnostic).not.toContain('private-token-value');
      expect(screen.getByTestId('synon-biomed-ask-user-history-editing')).toBeInTheDocument();
      expect(runtimeView.markSendFailed).toHaveBeenCalledWith('ask_user_branch_create_failed');
    } finally {
      consoleError.mockRestore();
    }
  });

  it.each([
    [
      'answered',
      JSON.stringify({ version: 1, status: 'answered', action: 'answer', answers: { [input.question]: 'pIC50' } }),
      'pIC50',
    ],
    [
      'deferred',
      JSON.stringify({ version: 1, status: 'deferred', action: 'decide_for_me' }),
      '已交由 Synon Biomed 决定',
    ],
    ['unavailable', JSON.stringify({ version: 2, status: 'cancelled', action: 'cancel' }), '回复不可用'],
  ])(
    'keeps %s history readable without mouse or keyboard fork entry when authority is unavailable',
    async (_status, output, expectedText) => {
      branchCapability.state = 'unsupported';
      branchCapability.reason = 'Canonical Transcript answer-fork authority is unavailable.';
      await renderWithI18n(
        <AskUserHistoryCard
          conversationId='frame-source'
          sourceBranchId='br_00000001'
          toolUseId='toolu_ask_1'
          input={input}
          output={output}
        />
      );

      const card = screen.getByRole('region', { name: '历史回答' });
      expect(card).toBeVisible();
      expect(within(card).getByText(expectedText)).toBeVisible();
      expect(within(card).queryByRole('button', { name: '修改答案' })).not.toBeInTheDocument();
      fireEvent.keyDown(card, { key: 'Enter' });
      fireEvent.keyDown(card, { key: ' ' });
      expect(forkAtAnswer).not.toHaveBeenCalled();
      expect(loadSnapshot).not.toHaveBeenCalled();
    }
  );
});
