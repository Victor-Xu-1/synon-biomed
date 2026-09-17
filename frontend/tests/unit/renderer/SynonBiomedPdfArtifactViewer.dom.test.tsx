import { ConfigProvider } from '@arco-design/web-react';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import React, { useEffect } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { SynonBiomedPdfArtifactViewer } from '@/renderer/pages/artifact/SynonBiomedPdfArtifactViewer';
import { renderWithI18n } from '../i18nTestUtils';

const textLayerCallbacks = new Map<number, () => void>();

vi.mock('react-pdf', () => ({
  pdfjs: { GlobalWorkerOptions: { workerSrc: '' } },
  Document: ({ children, onLoadSuccess }: { children: React.ReactNode; onLoadSuccess: (value: unknown) => void }) => {
    useEffect(() => onLoadSuccess({ numPages: 2 }), [onLoadSuccess]);
    return <div>{children}</div>;
  },
  Page: ({ pageNumber, onRenderTextLayerSuccess }: { pageNumber: number; onRenderTextLayerSuccess?: () => void }) => {
    useEffect(() => {
      if (!onRenderTextLayerSuccess) return;
      textLayerCallbacks.set(pageNumber, onRenderTextLayerSuccess);
      return () => {
        if (textLayerCallbacks.get(pageNumber) === onRenderTextLayerSuccess) textLayerCallbacks.delete(pageNumber);
      };
    }, [onRenderTextLayerSuccess, pageNumber]);
    return (
      <div className='react-pdf__Page'>
        <div className='react-pdf__Page__textContent'>
          <span>Page {pageNumber} assay result</span>
        </div>
      </div>
    );
  },
}));

class TestResizeObserver {
  observe() {}
  disconnect() {}
}

describe('Synon Biomed PDF artifact viewer', () => {
  beforeEach(() => {
    textLayerCallbacks.clear();
    vi.stubGlobal('ResizeObserver', TestResizeObserver);
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        arrayBuffer: async () => new Uint8Array([37, 80, 68, 70]).buffer,
      })
    );
    Object.defineProperty(Range.prototype, 'getClientRects', {
      configurable: true,
      value: () => [{ left: 220, top: 130, right: 340, bottom: 148, width: 120, height: 18 }],
    });
  });

  it('renders a controlled paged canvas and stores point coordinates relative to the selected page', async () => {
    const onSelectionChange = vi.fn();
    const onAnnotationClick = vi.fn();
    await renderWithI18n(
      <ConfigProvider>
        <SynonBiomedPdfArtifactViewer
          filename='report.pdf'
          contentUrl='/api/artifacts/pdf-1'
          annotations={[
            {
              id: 'pdf-point',
              artifactId: 'artifact-1',
              targetKey: 'av:version-1',
              label: '①',
              contentChecksum: 'checksum',
              type: 'point',
              text: 'Review figure',
              xPercent: 75,
              yPercent: 25,
              startLine: null,
              startColumn: null,
              endLine: null,
              endColumn: null,
              selectionText: null,
              pageNumber: 2,
              selectionPrefix: null,
              screenshotArtifactId: null,
              elementSelector: null,
              elementDescriptor: null,
              addressedAt: null,
              addressedInFrameId: null,
              createdAt: '2026-07-13T00:00:00.000Z',
            },
            {
              id: 'pdf-text',
              artifactId: 'artifact-1',
              targetKey: 'av:version-1',
              label: '②',
              contentChecksum: 'checksum',
              type: 'text_selection',
              text: 'Check assay wording',
              xPercent: null,
              yPercent: null,
              startLine: 1,
              startColumn: null,
              endLine: 1,
              endColumn: null,
              selectionText: 'assay result',
              pageNumber: 2,
              selectionPrefix: 'Page 2',
              screenshotArtifactId: null,
              elementSelector: null,
              elementDescriptor: null,
              addressedAt: null,
              addressedInFrameId: null,
              createdAt: '2026-07-13T00:00:00.000Z',
            },
          ]}
          onSelectionChange={onSelectionChange}
          onAnnotationClick={onAnnotationClick}
        />
      </ConfigProvider>,
      'en-US'
    );

    const page = await screen.findByRole('region', { name: 'PDF page 2' });
    Object.defineProperty(page, 'getBoundingClientRect', {
      configurable: true,
      value: () => ({ left: 200, top: 100, width: 600, height: 800, right: 800, bottom: 900 }),
    });
    await waitFor(() => expect(textLayerCallbacks.has(2)).toBe(true));
    await act(async () => {
      textLayerCallbacks.get(2)!();
    });
    fireEvent.click(page, { clientX: 350, clientY: 700 });
    await new Promise((resolve) => window.setTimeout(resolve, 40));

    expect(onSelectionChange).toHaveBeenLastCalledWith({
      type: 'point',
      text: 'PDF page 2 · 25.0%, 75.0%',
      x: 350,
      y: 700,
      xPercent: 25,
      yPercent: 75,
      pageNumber: 2,
    });
    expect(screen.getByRole('button', { name: 'View annotation ①' })).toHaveStyle({ left: '75%', top: '25%' });
    const textBadge = await screen.findByRole('button', { name: 'View annotation ②' });
    await waitFor(() =>
      expect(screen.getByTestId('pdf-text-highlight-pdf-text')).toHaveStyle({
        left: '20px',
        top: '30px',
        width: '120px',
        height: '18px',
      })
    );
    fireEvent.click(textBadge);
    expect(onAnnotationClick).toHaveBeenCalledWith(expect.objectContaining({ id: 'pdf-text' }));
    await waitFor(() => expect(screen.getByText('2 pages')).toBeInTheDocument());
  });
});
