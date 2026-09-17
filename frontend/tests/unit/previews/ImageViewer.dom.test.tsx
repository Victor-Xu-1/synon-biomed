/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import ImageViewer from '@/renderer/pages/conversation/Preview/components/viewers/ImageViewer';
import { act, cleanup, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@/common', () => ({
  ipcBridge: {
    fs: {
      getImageBase64: { invoke: vi.fn() },
    },
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('ImageViewer', () => {
  it('renders a localized semantic error when no image source is available', async () => {
    const view = await renderWithI18n(<ImageViewer />, 'en-US');

    expect(await screen.findByText('Failed to load image')).toBeTruthy();
    await act(async () => {
      await view.i18n.changeLanguage('zh-CN');
    });
    expect(screen.getByText('图片加载失败')).toBeTruthy();
  });

  it('uses a localized accessible name for an in-memory image', async () => {
    await renderWithI18n(<ImageViewer content='data:image/png;base64,AAAA' file_name='figure.png' />, 'zh-CN');

    expect(await screen.findByAltText('figure.png 的图片预览')).toBeTruthy();
  });
});
