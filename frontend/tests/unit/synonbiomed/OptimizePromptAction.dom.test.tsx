/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { act, render, screen, type RenderResult } from '@testing-library/react';
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

const renderAction = async (props: Partial<OptimizePromptActionProps> = {}): Promise<RenderResult> => {
  let view!: RenderResult;
  await act(async () => {
    view = await renderWithI18n(
      <OptimizePromptAction draft='帮我写一封请假条' onReplace={vi.fn()} {...props} />,
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
    const onReplace = vi.fn();
    optimizeMock.mockResolvedValue({ text: '优化后的提示词', model: 'optimize-model' });
    await renderAction({ onReplace });

    await act(async () => {
      screen.getByTestId('synon-biomed-optimize-prompt-trigger').click();
      await Promise.resolve();
    });

    expect(optimizeMock).toHaveBeenCalledWith('帮我写一封请假条');
    expect(onReplace).toHaveBeenCalledWith('优化后的提示词');
    expect(screen.queryByTestId('synon-biomed-optimize-prompt-spinner')).not.toBeInTheDocument();
  });

  it('does nothing when the draft is empty', async () => {
    const onReplace = vi.fn();
    await renderAction({ draft: '   ', onReplace });

    await act(async () => {
      screen.getByTestId('synon-biomed-optimize-prompt-trigger').click();
      await Promise.resolve();
    });

    expect(optimizeMock).not.toHaveBeenCalled();
    expect(onReplace).not.toHaveBeenCalled();
  });

  it('keeps the draft and reports an error when the service fails', async () => {
    const onReplace = vi.fn();
    optimizeMock.mockRejectedValue(new Error('provider unavailable'));
    await renderAction({ onReplace });

    await act(async () => {
      screen.getByTestId('synon-biomed-optimize-prompt-trigger').click();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(onReplace).not.toHaveBeenCalled();
    expect(screen.queryByTestId('synon-biomed-optimize-prompt-spinner')).not.toBeInTheDocument();
  });

  it('restores the previous draft when the success toast undo is pressed', async () => {
    const onReplace = vi.fn();
    optimizeMock.mockResolvedValue({ text: '优化后的提示词', model: 'optimize-model' });
    await renderAction({ onReplace });

    await act(async () => {
      screen.getByTestId('synon-biomed-optimize-prompt-trigger').click();
      await Promise.resolve();
    });
    onReplace.mockClear();

    const successArg = messageSuccessMock.mock.calls[0]?.[0] as { content?: React.ReactNode } | undefined;
    expect(successArg?.content).toBeDefined();

    const contentView = render(<>{successArg?.content}</>);
    await act(async () => {
      contentView.getByRole('button', { name: '撤销' }).click();
    });
    expect(onReplace).toHaveBeenCalledWith('帮我写一封请假条');
    contentView.unmount();
  });
});
