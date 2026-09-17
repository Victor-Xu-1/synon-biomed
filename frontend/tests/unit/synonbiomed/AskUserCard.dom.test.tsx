import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const { renderMoleculeSvg } = vi.hoisted(() => ({
  renderMoleculeSvg: vi.fn(async () => '<svg><path d="M0 0" /></svg>' as string | null),
}));

vi.mock('@/renderer/services/rdkitBrowser', () => ({
  renderMoleculeSvg,
}));

import AskUserCard from '@/renderer/components/synonBiomed/runtime/AskUserCard';
import { renderWithI18n, type TestLanguage } from '../i18nTestUtils';

const question = {
  header: 'Lead selection',
  question: 'Which compound should be prioritized?',
  multiSelect: true,
  options: [
    {
      label: 'Compound A',
      description: 'Best potency profile',
      pros: 'Low nanomolar activity',
      cons: 'Moderate clearance',
      smiles: 'CCO',
      recommended: true,
      readinessStatus: 'verified_ready',
    },
    {
      label: 'Compound B',
      description: 'Best exposure profile',
      pros: null,
      cons: null,
      smiles: null,
      recommended: false,
      readinessStatus: 'unverified',
    },
  ],
};

async function renderAskUser(
  ui: React.ReactElement,
  language: TestLanguage = 'zh-CN'
): Promise<Awaited<ReturnType<typeof renderWithI18n>>> {
  let result: Awaited<ReturnType<typeof renderWithI18n>> | undefined;
  await act(async () => {
    result = await renderWithI18n(ui, language);
  });
  if (!result) throw new Error('AskUser test render did not complete');
  return result;
}

describe('AskUserCard', () => {
  it('retains answers and reports a sanitized submission failure before a manual retry', async () => {
    const onResolve = vi
      .fn()
      .mockRejectedValueOnce(new Error('private transport data'))
      .mockResolvedValueOnce(undefined);
    await renderAskUser(<AskUserCard question={question} onResolve={onResolve} />);
    fireEvent.click(screen.getByRole('checkbox', { name: /Compound A/ }));
    fireEvent.click(screen.getByRole('button', { name: '提交' }));
    expect(await screen.findByRole('alert')).not.toHaveTextContent('private transport data');
    expect(screen.getByRole('checkbox', { name: /Compound A/ })).toHaveAttribute('aria-checked', 'true');
    fireEvent.click(screen.getByRole('button', { name: '提交' }));
    await waitFor(() => expect(onResolve).toHaveBeenCalledTimes(2));
    expect(onResolve.mock.calls[0][0]).toEqual(onResolve.mock.calls[1][0]);
  });
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('collects a multi-question planning intake before resolving once', async () => {
    const onResolve = vi.fn();
    const second = {
      ...question,
      header: 'Docking budget',
      question: 'Which docking budget should be used?',
      multiSelect: false,
    };
    await renderAskUser(
      <AskUserCard
        question={{ ...question, multiSelect: false }}
        questions={[{ ...question, multiSelect: false }, second]}
        onResolve={onResolve}
      />
    );

    fireEvent.click(screen.getByRole('radio', { name: /Compound A/ }));
    expect(onResolve).not.toHaveBeenCalled();
    expect(screen.getByRole('heading', { name: second.question })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('radio', { name: /Compound B/ }));
    await waitFor(() =>
      expect(onResolve).toHaveBeenCalledWith({
        action: 'answer',
        answers: { [question.question]: 'Compound A', [second.question]: 'Compound B' },
      })
    );
  });

  it('waits for an explicit user response instead of auto-delegating', async () => {
    vi.useFakeTimers();
    const onResolve = vi.fn();
    await renderAskUser(<AskUserCard question={question} onResolve={onResolve} />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(onResolve).not.toHaveBeenCalled();
    vi.useRealTimers();
  });

  it('renders v1.1 option details and submits multi-select answers', async () => {
    const onResolve = vi.fn();
    await renderAskUser(<AskUserCard question={question} onResolve={onResolve} />);

    expect(screen.getByRole('heading', { name: question.question })).toBeInTheDocument();
    expect(screen.getByText('优点：Low nanomolar activity')).toBeInTheDocument();
    expect(screen.getByText('注意：Moderate clearance')).toBeInTheDocument();
    expect(screen.getByText('推荐')).toBeInTheDocument();
    expect(screen.getByText('已验证可运行')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('checkbox', { name: /Compound A/ }));
    fireEvent.click(screen.getByRole('checkbox', { name: /Compound B/ }));
    fireEvent.click(screen.getByRole('button', { name: '提交' }));

    expect(onResolve).toHaveBeenCalledWith({
      action: 'answer',
      answers: { [question.question]: 'Compound A, Compound B' },
    });
    await waitFor(() => expect(screen.getByAltText('分子结构 CCO')).toBeInTheDocument());
    expect(renderMoleculeSvg).toHaveBeenCalledWith('CCO', 88, 64);
  });

  it('auto-submits single choices and supports custom answers, agent decision, discussion, and cancellation', async () => {
    const onResolve = vi.fn();
    const questionWithoutMolecule = {
      ...question,
      multiSelect: false,
      options: question.options.map((option) => ({ ...option, smiles: null })),
    };
    const { rerender } = await renderAskUser(<AskUserCard question={questionWithoutMolecule} onResolve={onResolve} />);

    fireEvent.change(screen.getByRole('textbox', { name: '自定义回答' }), { target: { value: 'Prioritize safety' } });
    fireEvent.click(screen.getByRole('button', { name: '发送回答' }));
    await waitFor(() =>
      expect(onResolve).toHaveBeenLastCalledWith({
        action: 'answer',
        answers: { [question.question]: 'Prioritize safety' },
      })
    );

    rerender(<AskUserCard question={questionWithoutMolecule} onResolve={onResolve} />);
    const agentChoice = screen.getByRole('radio', { name: /让 Synon Biomed 决定/ });
    await waitFor(() => expect(agentChoice).toBeEnabled());
    fireEvent.click(agentChoice);
    await waitFor(() => expect(onResolve).toHaveBeenLastCalledWith({ action: 'decide_for_me' }));
    expect(screen.queryByRole('button', { name: '提交' })).not.toBeInTheDocument();

    const discuss = screen.getByRole('button', { name: '继续讨论' });
    await waitFor(() => expect(discuss).toBeEnabled());
    fireEvent.click(discuss);
    await waitFor(() =>
      expect(onResolve).toHaveBeenLastCalledWith({
        action: 'discuss',
        message: '我想先进一步讨论这个问题。',
      })
    );
    const cancel = screen.getByRole('button', { name: '跳过' });
    await waitFor(() => expect(cancel).toBeEnabled());
    fireEvent.click(cancel);
    await waitFor(() => expect(onResolve).toHaveBeenLastCalledWith({ action: 'cancel' }));
  });

  it('uses one quiet response surface instead of nested framed controls', async () => {
    await renderAskUser(<AskUserCard question={question} onResolve={vi.fn()} onBack={vi.fn()} />);

    const card = screen.getByTestId('synon-biomed-ask-user-card');
    expect(card).toHaveClass('synon-ask-user-card');
    expect(card.querySelector('.synon-ask-user-card__options')).toBeInTheDocument();
    expect(card.querySelector('.synon-ask-user-card__composer')).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: '自定义回答' })).toHaveClass('synon-ask-user-card__input');
    expect(screen.getByRole('button', { name: '返回' })).toHaveClass('synon-ask-user-card__action--quiet');
    expect(screen.getByRole('button', { name: '继续讨论' })).toHaveClass('synon-ask-user-card__action--quiet');
    expect(screen.getByRole('button', { name: '跳过' })).toHaveClass('synon-ask-user-card__action--quiet');
    expect(screen.getByRole('button', { name: '提交' })).toHaveClass('synon-ask-user-card__submit');
  });

  it('filters duplicate agent-choice options and stages custom answers in multi-select mode', async () => {
    const onResolve = vi.fn();
    await renderAskUser(
      <AskUserCard
        question={{
          ...question,
          options: [
            ...question.options,
            { label: 'You decide for me', description: null, pros: null, cons: null, smiles: null },
            { label: 'Skip this question', description: null, pros: null, cons: null, smiles: null },
          ],
        }}
        onResolve={onResolve}
      />
    );

    expect(screen.queryByRole('checkbox', { name: /You decide for me/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: /Skip this question/ })).not.toBeInTheDocument();
    fireEvent.change(screen.getByRole('textbox', { name: '自定义回答' }), {
      target: { value: 'Compound C' },
    });
    fireEvent.click(screen.getByRole('button', { name: '发送回答' }));
    expect(onResolve).not.toHaveBeenCalled();
    expect(screen.getByLabelText('自定义选择')).toHaveTextContent('Compound C');

    fireEvent.click(screen.getByRole('checkbox', { name: /Compound A/ }));
    fireEvent.click(screen.getByRole('button', { name: '提交' }));
    await waitFor(() =>
      expect(onResolve).toHaveBeenCalledWith({
        action: 'answer',
        answers: { [question.question]: 'Compound C, Compound A' },
      })
    );
    await waitFor(() => expect(screen.getByAltText('分子结构 CCO')).toBeInTheDocument());
  });

  it('shows a stable SMILES fallback instead of an endless loader when the RDKit runtime rejects', async () => {
    renderMoleculeSvg.mockRejectedValueOnce(new Error('sensitive runtime implementation detail'));
    await renderAskUser(<AskUserCard question={question} onResolve={vi.fn()} />);

    expect(await screen.findByLabelText('无法绘制分子结构 CCO')).toBeInTheDocument();
    expect(screen.queryByLabelText('正在绘制分子结构 CCO')).not.toBeInTheDocument();
  });

  it('renders the complete interaction copy in English', async () => {
    await renderAskUser(<AskUserCard question={question} onResolve={vi.fn()} />, 'en-US');

    expect(screen.getByText('Pros: Low nanomolar activity')).toBeInTheDocument();
    expect(screen.getByText('Note: Moderate clearance')).toBeInTheDocument();
    expect(screen.getByText('Recommended')).toBeInTheDocument();
    expect(screen.getByText('Verified ready')).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: /Let Synon Biomed decide/ })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Custom answer' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Discuss' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Skip' })).toBeInTheDocument();
  });
});
