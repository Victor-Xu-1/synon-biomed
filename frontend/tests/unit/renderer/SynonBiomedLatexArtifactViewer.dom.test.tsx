import { ConfigProvider } from '@arco-design/web-react';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { SynonBiomedLatexArtifactViewer } from '@/renderer/pages/artifact/SynonBiomedLatexArtifactViewer';
import { renderWithI18n } from '../i18nTestUtils';

const source = String.raw`\documentclass{article}
\begin{document}
\section{Assay results}
The response follows $E = mc^2$.
\includegraphics{figures/assay-plot}
\end{document}`;

describe('Synon Biomed LaTeX artifact viewer', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, text: async () => source }));
    Object.defineProperty(Range.prototype, 'getClientRects', {
      configurable: true,
      value: () => [{ left: 40, top: 60, right: 160, bottom: 80, width: 120, height: 20 }],
    });
  });

  it('renders a full document with KaTeX, restores saved highlights and exposes the source mode', async () => {
    const onSelectionChange = vi.fn();
    const onAnnotationClick = vi.fn();
    await renderWithI18n(
      <ConfigProvider>
        <SynonBiomedLatexArtifactViewer
          filename='report.tex'
          contentUrl='/api/artifacts/latex-1'
          annotations={[
            {
              id: 'latex-note',
              artifactId: 'artifact-1',
              targetKey: 'av:version-1',
              label: '①',
              contentChecksum: 'checksum',
              type: 'text_selection',
              text: 'Check response wording',
              xPercent: null,
              yPercent: null,
              startLine: 4,
              startColumn: null,
              endLine: 4,
              endColumn: null,
              selectionText: 'response',
              pageNumber: null,
              selectionPrefix: 'The ',
              screenshotArtifactId: null,
              elementSelector: null,
              elementDescriptor: null,
              addressedAt: null,
              addressedInFrameId: null,
              createdAt: '2026-07-13T00:00:00.000Z',
            },
          ]}
          resourceUrls={{
            'figures/assay-plot.png': '/api/artifacts/assay-plot',
            'assay-plot': '/api/artifacts/assay-plot',
          }}
          onSelectionChange={onSelectionChange}
          onAnnotationClick={onAnnotationClick}
        />
      </ConfigProvider>,
      'en-US'
    );

    expect(await screen.findByRole('heading', { name: 'Assay results' })).toBeInTheDocument();
    await waitFor(() => expect(document.querySelector('.katex')).not.toBeNull());
    expect(document.querySelector('img.includegraphics')).toHaveAttribute('src', '/api/artifacts/assay-plot');
    const latexToolbar = document.querySelector<HTMLElement>('.sticky');
    const latexControls = latexToolbar!.querySelectorAll<HTMLButtonElement>('button');
    fireEvent.click(latexControls[2]!);
    await waitFor(() => expect(document.querySelector('.katex')).not.toBeNull());
    expect(latexControls[1]).toHaveTextContent('110%');
    expect(await screen.findByTestId('latex-text-highlight-latex-note')).toBeInTheDocument();
    const badge = screen.getByRole('button', { name: 'View annotation ①' });
    fireEvent.click(badge);
    expect(onAnnotationClick).toHaveBeenCalledWith(expect.objectContaining({ id: 'latex-note' }));

    fireEvent.click(screen.getByText('Source', { exact: true }));
    expect(screen.getByLabelText('report.tex LaTeX source')).toHaveTextContent('\\section{Assay results}');
  });

  it('keeps remote failures recoverable without falling back to a raw download link', async () => {
    vi.mocked(fetch).mockResolvedValueOnce({ ok: false, status: 503 } as Response);
    await renderWithI18n(
      <ConfigProvider>
        <SynonBiomedLatexArtifactViewer
          filename='broken.tex'
          contentUrl='/api/artifacts/latex-failed'
          onSelectionChange={vi.fn()}
        />
      </ConfigProvider>,
      'en-US'
    );
    expect(await screen.findByText('Failed to load LaTeX preview')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument();
    expect(screen.queryByText('Open original file')).toBeNull();
  });
});
