/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { act, screen, type RenderResult } from '@testing-library/react';
import { Message } from '@arco-design/web-react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import React from 'react';
import OptimizePromptAction, {
  type OptimizePromptActionProps,
} from '@/renderer/components/synonBiomed/runtime/OptimizePromptAction';
import { optimizeSynonBiomedPrompt } from '@/renderer/services/synonBiomedLlm';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@arco-design/web-react', () => ({
  Button: ({ children, onClick }: { children?: React.ReactNode; onClick?: () => void }) => (
    <button type='button' onClick={onClick}>
      {children}
    </button>
  ),
  Message: { success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() },
  Spin: ({ children }: { children?: React.ReactNode }) => <span>{children}</span>,
}));

vi.mock('@/renderer/services/synonBiomedLlm', () => ({
  optimizeSynonBiomedPrompt: vi.fn(),
}));

const optimizeMock = vi.mocked(optimizeSynonBiomedPrompt);
const messageSuccessMock = vi.mocked(Message.success);

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((settle) => {
    resolve = settle;
  });
  return { promise, resolve };
}

const renderAction = async (props: Partial<OptimizePromptActionProps> = {}): Promise<RenderResult> => {
  let view!: RenderResult;
  await act(async () => {
    view = await renderWithI18n(
      <OptimizePromptAction
        draft='帮我写一封请假条'
        conversationId='conversation-a'
        ownerId='owner-a'
        replaceIfCurrent={vi.fn(() => true)}
        {...props}
      />,
      'zh-CN'
    );
  });
  return view;
};

describe('OptimizePromptAction', () => {
  beforeEach(() => {
    optimizeMock.mockReset();
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('rewrites the draft through the service and reports success', async () => {
    const replaceIfCurrent = vi.fn(() => true);
    optimizeMock.mockResolvedValue({ text: '优化后的提示词', model: 'optimize-model' });
    await renderAction({ replaceIfCurrent });

    await act(async () => {
      screen.getByTestId('synon-biomed-optimize-prompt-trigger').click();
      await Promise.resolve();
    });

    expect(optimizeMock).toHaveBeenCalledWith(
      expect.objectContaining({ text: '帮我写一封请假条', conversationId: 'conversation-a' })
    );
    expect(replaceIfCurrent).toHaveBeenCalledWith('帮我写一封请假条', '优化后的提示词');
    expect(screen.getByTestId('synon-biomed-optimize-prompt-revert')).toBeInTheDocument();
    expect(screen.queryByTestId('synon-biomed-optimize-prompt-spinner')).not.toBeInTheDocument();
  });

  it('does nothing when the draft is empty', async () => {
    const replaceIfCurrent = vi.fn(() => true);
    await renderAction({ draft: '   ', replaceIfCurrent });

    await act(async () => {
      screen.getByTestId('synon-biomed-optimize-prompt-trigger').click();
      await Promise.resolve();
    });

    expect(optimizeMock).not.toHaveBeenCalled();
    expect(replaceIfCurrent).not.toHaveBeenCalled();
  });

  it('keeps the draft and reports an error when the service fails', async () => {
    const replaceIfCurrent = vi.fn(() => true);
    optimizeMock.mockRejectedValue(new Error('provider unavailable'));
    await renderAction({ replaceIfCurrent });

    await act(async () => {
      screen.getByTestId('synon-biomed-optimize-prompt-trigger').click();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(replaceIfCurrent).not.toHaveBeenCalled();
    expect(screen.queryByTestId('synon-biomed-optimize-prompt-revert')).not.toBeInTheDocument();
    expect(screen.queryByTestId('synon-biomed-optimize-prompt-spinner')).not.toBeInTheDocument();
  });

  it('hides the revert button until the first optimization succeeds', async () => {
    await renderAction({});

    expect(screen.queryByTestId('synon-biomed-optimize-prompt-revert')).not.toBeInTheDocument();
  });

  it('restores the previous draft when the revert button is pressed', async () => {
    const replaceIfCurrent = vi.fn(() => true);
    optimizeMock.mockResolvedValue({ text: '优化后的提示词', model: 'optimize-model' });
    await renderAction({ replaceIfCurrent });

    await act(async () => {
      screen.getByTestId('synon-biomed-optimize-prompt-trigger').click();
      await Promise.resolve();
    });
    replaceIfCurrent.mockClear();

    await act(async () => {
      screen.getByTestId('synon-biomed-optimize-prompt-revert').click();
    });

    expect(replaceIfCurrent).toHaveBeenCalledWith('优化后的提示词', '帮我写一封请假条');
    expect(screen.queryByTestId('synon-biomed-optimize-prompt-revert')).not.toBeInTheDocument();
  });

  it('keeps revert outside the success toast', async () => {
    const replaceIfCurrent = vi.fn(() => true);
    optimizeMock.mockResolvedValue({ text: '优化后的提示词', model: 'optimize-model' });
    await renderAction({ replaceIfCurrent });

    await act(async () => {
      screen.getByTestId('synon-biomed-optimize-prompt-trigger').click();
      await Promise.resolve();
    });
    replaceIfCurrent.mockClear();

    expect(messageSuccessMock).not.toHaveBeenCalledWith(expect.objectContaining({ btn: expect.anything() }));
    expect(replaceIfCurrent).not.toHaveBeenCalled();
  });

  it('does not replace a draft edited while optimization is in flight', async () => {
    const pending = deferred<{ text: string; model: string }>();
    const replaceIfCurrent = vi.fn(() => false);
    optimizeMock.mockReturnValue(pending.promise);
    await renderAction({ replaceIfCurrent });

    act(() => screen.getByTestId('synon-biomed-optimize-prompt-trigger').click());
    await act(async () => {
      pending.resolve({ text: '旧请求的结果', model: 'optimize-model' });
      await pending.promise;
    });

    expect(replaceIfCurrent).toHaveBeenCalledWith('帮我写一封请假条', '旧请求的结果');
    expect(messageSuccessMock).not.toHaveBeenCalled();
    expect(screen.queryByTestId('synon-biomed-optimize-prompt-revert')).not.toBeInTheDocument();
  });

  it('ignores a response after switching conversations', async () => {
    const pending = deferred<{ text: string; model: string }>();
    const replaceIfCurrent = vi.fn(() => true);
    optimizeMock.mockReturnValue(pending.promise);
    const view = await renderAction({ replaceIfCurrent });

    act(() => screen.getByTestId('synon-biomed-optimize-prompt-trigger').click());
    act(() => {
      view.rerender(
        <OptimizePromptAction
          draft='另一会话草稿'
          conversationId='conversation-b'
          ownerId='owner-a'
          replaceIfCurrent={replaceIfCurrent}
        />
      );
    });
    await act(async () => {
      pending.resolve({ text: '旧会话结果', model: 'optimize-model' });
      await pending.promise;
    });

    expect(replaceIfCurrent).not.toHaveBeenCalled();
    expect(messageSuccessMock).not.toHaveBeenCalled();
  });

  it('aborts an in-flight request on unmount without replacing the draft', async () => {
    const pending = deferred<{ text: string; model: string }>();
    const replaceIfCurrent = vi.fn(() => true);
    optimizeMock.mockReturnValue(pending.promise);
    const view = await renderAction({ replaceIfCurrent });

    act(() => screen.getByTestId('synon-biomed-optimize-prompt-trigger').click());
    const request = optimizeMock.mock.calls[0]?.[0];
    expect(request?.signal?.aborted).toBe(false);
    act(() => view.unmount());
    expect(request?.signal?.aborted).toBe(true);
    await act(async () => {
      pending.resolve({ text: '过期结果', model: 'optimize-model' });
      await pending.promise;
    });
    expect(replaceIfCurrent).not.toHaveBeenCalled();
    expect(messageSuccessMock).not.toHaveBeenCalled();
  });

  it('does not apply a previous account response after owner changes', async () => {
    const pending = deferred<{ text: string; model: string }>();
    const replaceIfCurrent = vi.fn(() => true);
    optimizeMock.mockReturnValue(pending.promise);
    const view = await renderAction({ replaceIfCurrent });

    act(() => screen.getByTestId('synon-biomed-optimize-prompt-trigger').click());
    act(() => {
      view.rerender(
        <OptimizePromptAction
          draft='新用户草稿'
          conversationId='conversation-a'
          ownerId='owner-b'
          replaceIfCurrent={replaceIfCurrent}
        />
      );
    });
    await act(async () => {
      pending.resolve({ text: '旧用户结果', model: 'optimize-model' });
      await pending.promise;
    });
    expect(replaceIfCurrent).not.toHaveBeenCalled();
  });

  it('prevents a same-tick double click from starting two requests', async () => {
    const pending = deferred<{ text: string; model: string }>();
    optimizeMock.mockReturnValue(pending.promise);
    await renderAction();

    act(() => {
      const trigger = screen.getByTestId('synon-biomed-optimize-prompt-trigger');
      trigger.click();
      trigger.click();
    });
    expect(optimizeMock).toHaveBeenCalledTimes(1);
    await act(async () => {
      pending.resolve({ text: '优化后的提示词', model: 'optimize-model' });
      await pending.promise;
    });
  });

  it('does not let revert overwrite text edited after optimization', async () => {
    const replaceIfCurrent = vi.fn().mockReturnValueOnce(true).mockReturnValueOnce(false);
    optimizeMock.mockResolvedValue({ text: '优化后的提示词', model: 'optimize-model' });
    await renderAction({ replaceIfCurrent });
    await act(async () => {
      screen.getByTestId('synon-biomed-optimize-prompt-trigger').click();
      await Promise.resolve();
    });

    act(() => screen.getByTestId('synon-biomed-optimize-prompt-revert').click());
    expect(replaceIfCurrent).toHaveBeenNthCalledWith(2, '优化后的提示词', '帮我写一封请假条');
    expect(screen.queryByTestId('synon-biomed-optimize-prompt-revert')).not.toBeInTheDocument();
  });
});
