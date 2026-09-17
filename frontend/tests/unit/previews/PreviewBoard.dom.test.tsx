/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { PreviewProvider, usePreviewContext } from '@/renderer/pages/conversation/Preview/context/PreviewContext';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import React, { useEffect, useRef } from 'react';
import { I18nextProvider } from 'react-i18next';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createTestI18n } from '../i18nTestUtils';

const commonMocks = vi.hoisted(() => ({
  writeFileInvoke: vi.fn(),
  createArtifactVersion: vi.fn(),
}));

let PreviewBoardComponent: React.ComponentType;

vi.mock('@/common', () => ({
  ipcBridge: {
    fileStream: { contentUpdate: { on: vi.fn(() => vi.fn()) } },
    preview: { open: { on: vi.fn(() => vi.fn()) } },
    fs: {
      writeFile: { invoke: commonMocks.writeFileInvoke },
      getFileMetadata: { invoke: vi.fn() },
      readFile: { invoke: vi.fn() },
      getImageBase64: { invoke: vi.fn() },
    },
    theme: {
      setActive: { invoke: vi.fn().mockResolvedValue(undefined) },
      changed: { on: vi.fn(() => vi.fn()) },
    },
  },
}));

vi.mock('@/renderer/pages/conversation/Preview/components/editors/DocumentWysiwygEditor', () => ({
  default: ({ value, onChange }: { value: string; onChange: (value: string) => void }) => (
    <textarea aria-label='Board file editor' value={value} onChange={(event) => onChange(event.target.value)} />
  ),
}));

vi.mock('@/renderer/services/synonBiomedArtifacts', () => ({
  createSynonBiomedTextArtifactVersion: commonMocks.createArtifactVersion,
  getSynonBiomedArtifactVersionContentUrl: (versionId: string) => `/api/artifact-versions/${versionId}/content`,
}));

vi.mock('@/renderer/utils/emitter', () => ({
  emitter: { on: vi.fn(), off: vi.fn() },
}));

const BoardHarness: React.FC = () => {
  const context = usePreviewContext();
  const initialized = useRef(false);

  useEffect(() => {
    if (initialized.current) return;
    initialized.current = true;
    context.openPreview(
      '',
      'code',
      {
        file_name: 'analysis.py',
        file_path: '/project/analysis.py',
        missingFile: true,
      },
      {
        presentation: 'board',
      }
    );
    context.openPreview(
      '',
      'image',
      {
        file_name: 'result.png',
        file_path: '/project/result.png',
        missingFile: true,
      },
      {
        presentation: 'board',
      }
    );
  }, [context]);

  const PreviewBoard = PreviewBoardComponent;
  return <PreviewBoard />;
};

const EditableBoardHarness: React.FC = () => {
  const context = usePreviewContext();
  const initialized = useRef(false);

  useEffect(() => {
    if (initialized.current) return;
    initialized.current = true;
    context.openPreview(
      '# Initial note',
      'markdown',
      {
        file_name: 'notes.md',
        file_path: 'notes.md',
        workspace: '/project',
        editable: true,
      },
      { presentation: 'board' }
    );
  }, [context]);

  const PreviewBoard = PreviewBoardComponent;
  return <PreviewBoard />;
};

const RemoteEditableBoardHarness: React.FC = () => {
  const context = usePreviewContext();
  const initialized = useRef(false);

  useEffect(() => {
    if (initialized.current) return;
    initialized.current = true;
    context.openPreview(
      '# Published note',
      'markdown',
      {
        file_name: 'published.md',
        artifactId: 'artifact-1',
        versionId: 'version-1',
        contentUrl: '/api/artifact-versions/version-1/content',
        editable: true,
      },
      { presentation: 'board' }
    );
  }, [context]);

  const PreviewBoard = PreviewBoardComponent;
  return <PreviewBoard />;
};

describe('PreviewBoard', () => {
  beforeEach(async () => {
    localStorage.clear();
    commonMocks.writeFileInvoke.mockReset().mockResolvedValue(true);
    commonMocks.createArtifactVersion.mockReset().mockResolvedValue({ versionId: 'version-2' });
    Element.prototype.scrollIntoView = vi.fn();
    vi.stubGlobal(
      'matchMedia',
      vi.fn(() => ({ matches: false }))
    );
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(JSON.stringify({ data: {} }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          })
      )
    );
    if (!PreviewBoardComponent) {
      PreviewBoardComponent = (
        await import('@/renderer/pages/conversation/Preview/components/PreviewPanel/PreviewBoard')
      ).default;
    }
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it('promotes one project file to fullscreen without reflowing the other tile', async () => {
    const i18n = await createTestI18n('en-US');
    render(
      <I18nextProvider i18n={i18n}>
        <PreviewProvider>
          <BoardHarness />
        </PreviewProvider>
      </I18nextProvider>
    );

    await waitFor(() => expect(screen.getAllByTestId('preview-board-tile')).toHaveLength(2), { timeout: 3000 });
    expect(screen.getByText('analysis.py')).toBeInTheDocument();
    expect(screen.getByText('result.png')).toBeInTheDocument();
    expect(screen.getByText('2 files open')).toBeInTheDocument();

    fireEvent.click(screen.getAllByRole('button', { name: 'Expand preview' })[0]);
    expect(screen.getAllByTestId('preview-board-tile')).toHaveLength(2);
    expect(screen.getByTestId('preview-board-fullscreen-layer')).toBeInTheDocument();
    expect(screen.getByTestId('preview-board').querySelectorAll('[data-testid="preview-board-tile"]')).toHaveLength(1);
    expect(document.body.style.overflow).toBe('hidden');

    fireEvent.click(screen.getByRole('button', { name: 'Return to file board' }));
    expect(screen.queryByTestId('preview-board-fullscreen-layer')).not.toBeInTheDocument();
    expect(screen.getByTestId('preview-board').querySelectorAll('[data-testid="preview-board-tile"]')).toHaveLength(2);
    expect(document.body.style.overflow).toBe('');

    fireEvent.click(screen.getAllByRole('button', { name: 'Expand preview' })[0]);
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(screen.queryByTestId('preview-board-fullscreen-layer')).not.toBeInTheDocument();
    expect(document.body.style.overflow).toBe('');

    fireEvent.click(screen.getAllByRole('button', { name: 'Close file' })[0]);
    expect(screen.getAllByTestId('preview-board-tile')).toHaveLength(1);
    expect(screen.getByText('1 files open')).toBeInTheDocument();
  });
  it('opens the complete file board in fullscreen without collapsing its tiles', async () => {
    const i18n = await createTestI18n('en-US');
    render(
      <I18nextProvider i18n={i18n}>
        <PreviewProvider>
          <BoardHarness />
        </PreviewProvider>
      </I18nextProvider>
    );

    await waitFor(() => expect(screen.getAllByTestId('preview-board-tile')).toHaveLength(2));
    expect(screen.queryByTestId('preview-board-fullscreen-layer')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Open file board fullscreen' }));
    const layer = screen.getByTestId('preview-board-fullscreen-layer');
    expect(layer).toContainElement(screen.getByTestId('preview-board'));
    expect(layer.querySelectorAll('[data-testid="preview-board-tile"]')).toHaveLength(2);
    expect(document.body.style.overflow).toBe('hidden');

    fireEvent.click(screen.getByRole('button', { name: 'Exit file board fullscreen' }));
    expect(screen.queryByTestId('preview-board-fullscreen-layer')).not.toBeInTheDocument();
    expect(screen.getAllByTestId('preview-board-tile')).toHaveLength(2);
    expect(document.body.style.overflow).toBe('');

    fireEvent.click(screen.getByRole('button', { name: 'Open file board fullscreen' }));
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(screen.queryByTestId('preview-board-fullscreen-layer')).not.toBeInTheDocument();
    expect(document.body.style.overflow).toBe('');
  });

  it('persists the selected canvas column count across remounts', async () => {
    const i18n = await createTestI18n('en-US');
    const renderBoard = () =>
      render(
        <I18nextProvider i18n={i18n}>
          <PreviewProvider>
            <BoardHarness />
          </PreviewProvider>
        </I18nextProvider>
      );

    const firstRender = renderBoard();
    await waitFor(() => expect(screen.getAllByTestId('preview-board-tile')).toHaveLength(2));
    expect(screen.getByTestId('preview-board')).toHaveAttribute('data-columns', '2');
    expect(screen.getByTestId('preview-board')).toHaveStyle({ '--preview-board-columns': '2' });

    fireEvent.click(screen.getByRole('button', { name: 'Single column' }));
    expect(screen.getByTestId('preview-board')).toHaveAttribute('data-columns', '1');
    expect(screen.getByTestId('preview-board')).toHaveStyle({ '--preview-board-columns': '1' });
    expect(screen.getByRole('button', { name: 'Single column' })).toHaveAttribute('aria-pressed', 'true');

    fireEvent.click(screen.getByRole('button', { name: 'Three columns' }));
    expect(screen.getByTestId('preview-board')).toHaveAttribute('data-columns', '3');
    expect(screen.getByTestId('preview-board')).toHaveStyle({ '--preview-board-columns': '3' });
    expect(screen.getByRole('button', { name: 'Three columns' })).toHaveAttribute('aria-pressed', 'true');

    fireEvent.click(screen.getByRole('button', { name: 'Single column' }));

    firstRender.unmount();
    renderBoard();
    await waitFor(() => expect(screen.getAllByTestId('preview-board-tile')).toHaveLength(2));
    expect(screen.getByTestId('preview-board')).toHaveAttribute('data-columns', '1');
  });

  it('avoids smooth auto-scrolling when reduced motion is requested', async () => {
    vi.stubGlobal(
      'matchMedia',
      vi.fn(() => ({ matches: true }))
    );
    const i18n = await createTestI18n('en-US');
    render(
      <I18nextProvider i18n={i18n}>
        <PreviewProvider>
          <BoardHarness />
        </PreviewProvider>
      </I18nextProvider>
    );

    await waitFor(() => expect(screen.getAllByTestId('preview-board-tile')).toHaveLength(2));
    await waitFor(() =>
      expect(Element.prototype.scrollIntoView).toHaveBeenCalledWith({
        block: 'nearest',
        inline: 'nearest',
        behavior: 'auto',
      })
    );
  });

  it('edits one text tile and writes the saved content back to its workspace file', async () => {
    const i18n = await createTestI18n('en-US');
    render(
      <I18nextProvider i18n={i18n}>
        <PreviewProvider>
          <EditableBoardHarness />
        </PreviewProvider>
      </I18nextProvider>
    );

    await waitFor(() => expect(screen.getByText('notes.md')).toBeInTheDocument());
    fireEvent.click(screen.getByRole('button', { name: 'Edit file' }));
    await waitFor(() => expect(screen.getByRole('textbox', { name: 'Board file editor' })).toBeInTheDocument());
    fireEvent.change(screen.getByRole('textbox', { name: 'Board file editor' }), {
      target: { value: '# Updated note' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));

    await waitFor(() =>
      expect(commonMocks.writeFileInvoke).toHaveBeenCalledWith({
        path: 'notes.md',
        data: '# Updated note',
        workspace: '/project',
      })
    );
    expect(screen.queryByRole('textbox', { name: 'Board file editor' })).not.toBeInTheDocument();
  });

  it('saves remote text edits as a child artifact version instead of overwriting history', async () => {
    const i18n = await createTestI18n('en-US');
    render(
      <I18nextProvider i18n={i18n}>
        <PreviewProvider>
          <RemoteEditableBoardHarness />
        </PreviewProvider>
      </I18nextProvider>
    );

    await waitFor(() => expect(screen.getByText('published.md')).toBeInTheDocument());
    fireEvent.click(screen.getByRole('button', { name: 'Edit file' }));
    await waitFor(() => expect(screen.getByRole('textbox', { name: 'Board file editor' })).toBeInTheDocument());
    fireEvent.change(screen.getByRole('textbox', { name: 'Board file editor' }), {
      target: { value: '# Revised published note' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));

    await waitFor(() =>
      expect(commonMocks.createArtifactVersion).toHaveBeenCalledWith({
        artifactId: 'artifact-1',
        content: '# Revised published note',
        contentType: 'text/markdown',
        parentVersionId: 'version-1',
      })
    );
    expect(commonMocks.writeFileInvoke).not.toHaveBeenCalled();
    expect(screen.queryByRole('textbox', { name: 'Board file editor' })).not.toBeInTheDocument();
  });
});
