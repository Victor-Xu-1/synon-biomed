/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import ArchiveViewer from '@/renderer/pages/conversation/Preview/components/viewers/ArchiveViewer';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { I18nextProvider } from 'react-i18next';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createTestI18n } from '../i18nTestUtils';

const previewMocks = vi.hoisted(() => ({
  openPreview: vi.fn(),
}));

vi.mock('@/renderer/pages/conversation/Preview/context/PreviewContext', () => ({
  usePreviewContext: () => ({ openPreview: previewMocks.openPreview }),
}));

const listing = {
  filename: 'binding_modes.zip',
  containers: [],
  entries: [
    { path: 'images', name: 'images', size: 0, directory: true, archive: false },
    { path: 'images/ligand-1_2D.png', name: 'ligand-1_2D.png', size: 75_000, directory: false, archive: false },
    { path: 'images/notes.md', name: 'notes.md', size: 42, directory: false, archive: false },
  ],
};

describe('ArchiveViewer', () => {
  beforeEach(() => {
    previewMocks.openPreview.mockReset();
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes('/archive/content')) return new Response('# Archive note', { status: 200 });
        return Response.json(listing);
      })
    );
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it('adds archive entries to the file board instead of replacing the archive tile', async () => {
    const i18n = await createTestI18n('en-US');
    render(
      <I18nextProvider i18n={i18n}>
        <ArchiveViewer filename='binding_modes.zip' contentUrl='/api/artifacts/archive-version/content' />
      </I18nextProvider>
    );

    await waitFor(() => expect(screen.getByRole('button', { name: /images/i })).toBeInTheDocument());
    fireEvent.click(screen.getByRole('button', { name: /images/i }));

    fireEvent.click(screen.getByRole('button', { name: /ligand-1_2D\.png/i }));
    await waitFor(() => expect(previewMocks.openPreview).toHaveBeenCalledTimes(1));
    expect(previewMocks.openPreview).toHaveBeenLastCalledWith(
      expect.stringContaining('/archive/content'),
      'image',
      expect.objectContaining({ file_name: 'ligand-1_2D.png' }),
      { presentation: 'board' }
    );

    fireEvent.click(screen.getByRole('button', { name: /notes\.md/i }));
    await waitFor(() => expect(previewMocks.openPreview).toHaveBeenCalledTimes(2));
    expect(previewMocks.openPreview).toHaveBeenLastCalledWith(
      '# Archive note',
      'markdown',
      expect.objectContaining({ file_name: 'notes.md' }),
      { presentation: 'board' }
    );
  });
});
