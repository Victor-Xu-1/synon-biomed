/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import PDFViewer from '@/renderer/pages/conversation/Preview/components/viewers/PDFViewer';
import { buildNativePdfPreviewSrc } from '@/renderer/pages/conversation/Preview/previewUrls';
import { act, cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@/common', () => ({
  ipcBridge: {
    shell: {
      openFile: { invoke: vi.fn() },
    },
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('PDFViewer', () => {
  it('renders a localized missing-source error', async () => {
    const view = await renderWithI18n(<PDFViewer />, 'en-US');

    expect(await screen.findByText('PDF file path is missing')).toBeTruthy();
    await act(async () => {
      await view.i18n.changeLanguage('zh-CN');
    });
    expect(screen.getByText('PDF 文件路径为空')).toBeTruthy();
  });

  it('mounts the browser-native iframe before completion and clears loading on its real load event', async () => {
    await renderWithI18n(<PDFViewer content='data:application/pdf;base64,JVBERi0xLjQ=' hideToolbar />, 'en-US');
    const iframe = screen.getByTestId('pdf-browser-preview');

    expect(iframe.tagName).toBe('IFRAME');
    expect(iframe.getAttribute('src')).toBe(buildNativePdfPreviewSrc('data:application/pdf;base64,JVBERi0xLjQ='));
    expect(screen.getByText('Loading PDF')).toBeTruthy();
    fireEvent.load(iframe);
    await waitFor(() => expect(screen.queryByText('Loading PDF')).toBeNull());
  });

  it('loads a remote project PDF from its authenticated content endpoint', async () => {
    await renderWithI18n(
      <PDFViewer
        file_path='synonbiomed://project/proj_123/project-files/report.pdf'
        content='/api/projects/proj_123/artifacts/artifact_123/content'
        hideToolbar
      />,
      'en-US'
    );

    const iframe = screen.getByTestId('pdf-browser-preview');
    expect(iframe.getAttribute('src')).toBe(
      buildNativePdfPreviewSrc('/api/projects/proj_123/artifacts/artifact_123/content')
    );
  });
});
