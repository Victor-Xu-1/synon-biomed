/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { PreviewSnapshotInfo } from '@/common/types/office/preview';
import PreviewHistoryDropdown from '@/renderer/pages/conversation/Preview/components/PreviewPanel/PreviewHistoryDropdown';
import { act, cleanup, fireEvent, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const snapshot: PreviewSnapshotInfo = {
  id: 'snapshot-1',
  label: 'Version 1',
  created_at: Date.UTC(2026, 0, 2, 3, 4),
  size: 1536,
  contentType: 'markdown',
};

afterEach(cleanup);

describe('PreviewHistoryDropdown', () => {
  it('renders history metadata and switches all interface copy between English and Chinese', async () => {
    const onSnapshotSelect = vi.fn();
    const view = await renderWithI18n(
      <PreviewHistoryDropdown
        historyVersions={[snapshot]}
        historyLoading={false}
        historyError={false}
        historyTarget={null}
        currentTheme='light'
        onSnapshotSelect={onSnapshotSelect}
      />,
      'en-US'
    );

    expect(screen.getByText('History Versions')).toBeTruthy();
    expect(screen.getByText('Current File')).toBeTruthy();
    expect(screen.getByText('1.5 KB')).toBeTruthy();
    fireEvent.click(screen.getByText('1.5 KB').closest('[class*="cursor-pointer"]')!);
    expect(onSnapshotSelect).toHaveBeenCalledWith(snapshot);

    await act(async () => {
      await view.i18n.changeLanguage('zh-CN');
    });
    expect(screen.getByText('历史版本')).toBeTruthy();
    expect(screen.getByText('当前文件')).toBeTruthy();
  });

  it('renders a localized semantic error instead of a backend error payload', async () => {
    const view = await renderWithI18n(
      <PreviewHistoryDropdown
        historyVersions={[]}
        historyLoading={false}
        historyError
        historyTarget={null}
        currentTheme='dark'
        onSnapshotSelect={vi.fn()}
      />,
      'en-US'
    );

    expect(screen.getByText('Failed to load history')).toBeTruthy();
    await act(async () => {
      await view.i18n.changeLanguage('zh-CN');
    });
    expect(screen.getByText('加载历史版本失败')).toBeTruthy();
  });
});
