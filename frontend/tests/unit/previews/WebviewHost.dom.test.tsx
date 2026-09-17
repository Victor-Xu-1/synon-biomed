/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import WebviewHost from '@/renderer/components/media/WebviewHost';
import { act, cleanup, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe('WebviewHost', () => {
  it('localizes navigation controls and the frame title when language changes', async () => {
    const view = await renderWithI18n(<WebviewHost url='https://example.test' showNavBar />, 'en-US');

    expect(screen.getByLabelText('Back')).toBeTruthy();
    expect(screen.getByLabelText('Forward')).toBeTruthy();
    expect(screen.getByLabelText('Refresh')).toBeTruthy();
    expect(screen.getByLabelText('Preview URL')).toBeTruthy();
    expect(screen.getByTitle('Web preview')).toBeTruthy();

    await act(async () => {
      await view.i18n.changeLanguage('zh-CN');
    });
    expect(screen.getByLabelText('后退')).toBeTruthy();
    expect(screen.getByLabelText('预览地址')).toBeTruthy();
    expect(screen.getByTitle('网页预览')).toBeTruthy();
  });

  it('shows a localized timeout recovery state and reports the semantic message', async () => {
    vi.useFakeTimers();
    const onDidFailLoad = vi.fn();
    await renderWithI18n(<WebviewHost url='https://timeout.test' onDidFailLoad={onDidFailLoad} />, 'zh-CN');

    act(() => {
      vi.advanceTimersByTime(20_000);
    });

    expect(screen.getByText('预览加载超时，请重试或打开原始地址。')).toBeTruthy();
    expect(screen.getByRole('button', { name: '重试' })).toBeTruthy();
    expect(screen.getByText('打开原始地址')).toBeTruthy();
    expect(onDidFailLoad).toHaveBeenCalledWith(-2, '预览加载超时，请重试或打开原始地址。');
  });
});
