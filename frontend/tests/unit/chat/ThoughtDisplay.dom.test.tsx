import { act, cleanup, render, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ThoughtDisplay from '@/renderer/components/chat/ThoughtDisplay';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? key,
  }),
}));

describe('ThoughtDisplay', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it('renders a stable, non-overlapping running row and advances elapsed time', () => {
    render(<ThoughtDisplay running />);

    const status = screen.getByTestId('thought-running-status');
    expect(status).toHaveClass('thought-display', 'thought-display--default');
    expect(status.className).not.toContain('mb--20px');
    expect(status.className).not.toContain('pb-30px');
    expect(status).toHaveAttribute('aria-live', 'polite');
    expect(status).toHaveTextContent('0');

    act(() => vi.advanceTimersByTime(2_000));
    expect(status).toHaveTextContent('2');
  });

  it('keeps long thought details in separately constrained fields', () => {
    render(
      <ThoughtDisplay
        running
        style='compact'
        thought={{ subject: '分析实验结果', description: '正在聚合多个并行子任务返回的长结果描述' }}
      />
    );

    const status = screen.getByRole('status');
    expect(status).toHaveClass('thought-display--compact');
    expect(status.querySelector('.thought-display__subject')).toHaveTextContent('分析实验结果');
    expect(status.querySelector('.thought-display__description')).toHaveTextContent(
      '正在聚合多个并行子任务返回的长结果描述'
    );
  });
});
