import { ConfigProvider } from '@arco-design/web-react';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { ArtifactSelectionAnnotationModal } from '@/renderer/pages/artifact/ArtifactSelectionAnnotationModal';
import { locateRenderedTextSelection } from '@/renderer/pages/artifact/artifactTextSelection';
import { SynonBiomedImageArtifactViewer } from '@/renderer/pages/artifact/SynonBiomedImageArtifactViewer';
import { renderWithI18n } from '../i18nTestUtils';

const createAnnotation = vi.hoisted(() => vi.fn());

vi.mock('@/renderer/services/synonBiomedAnnotations', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/renderer/services/synonBiomedAnnotations')>();
  return { ...actual, createSynonBiomedArtifactAnnotation: createAnnotation };
});

describe('Artifact selection annotation', () => {
  it('maps an exact rendered selection back to one-based source coordinates and prefix', () => {
    const content = '# Result\n\nSTAT6 inhibition changed viability.\nConclusion.';
    expect(locateRenderedTextSelection(content, 'STAT6 inhibition changed viability.', 240, 180)).toEqual({
      type: 'text_selection',
      text: 'STAT6 inhibition changed viability.',
      x: 240,
      y: 180,
      startLine: 3,
      startColumn: 1,
      endLine: 3,
      endColumn: 36,
      selectionPrefix: '# Result\n\n',
      pageNumber: null,
    });
  });

  it('retains the selected text when rendered HTML cannot be mapped byte-for-byte to its source', () => {
    expect(locateRenderedTextSelection('<strong>STAT6</strong>', 'STAT6 result', 90, 110)).toEqual({
      type: 'text_selection',
      text: 'STAT6 result',
      x: 90,
      y: 110,
      startLine: null,
      startColumn: null,
      endLine: null,
      endColumn: null,
      selectionPrefix: null,
      pageNumber: null,
    });
  });

  it('creates a real text-selection annotation payload without dropping source anchors', async () => {
    createAnnotation.mockResolvedValueOnce({ id: 'annotation-1', text: 'Review this claim' });
    const onCreated = vi.fn();
    await renderWithI18n(
      <ConfigProvider>
        <ArtifactSelectionAnnotationModal
          artifactId='artifact-1'
          versionId='version-1'
          selection={{
            type: 'text_selection',
            text: 'STAT6 inhibition changed viability.',
            x: 240,
            y: 180,
            startLine: 3,
            startColumn: 1,
            endLine: 3,
            endColumn: 36,
            selectionPrefix: '# Result\n\n',
            pageNumber: null,
          }}
          onCancel={vi.fn()}
          onCreated={onCreated}
        />
      </ConfigProvider>
    );

    fireEvent.change(screen.getByRole('textbox', { name: '选区批注内容' }), {
      target: { value: 'Review this claim' },
    });
    fireEvent.click(screen.getByRole('button', { name: '添加' }));

    await waitFor(() =>
      expect(createAnnotation).toHaveBeenCalledWith('artifact-1', 'version-1', {
        type: 'text_selection',
        text: 'Review this claim',
        selectionText: 'STAT6 inhibition changed viability.',
        selectionPrefix: '# Result\n\n',
        startLine: 3,
        startColumn: 1,
        endLine: 3,
        endColumn: 36,
        xPercent: null,
        yPercent: null,
        pageNumber: null,
        elementSelector: null,
        elementDescriptor: null,
      })
    );
    expect(onCreated).toHaveBeenCalledWith(expect.objectContaining({ id: 'annotation-1' }));
  });

  it('normalizes image clicks against the rendered image and restores existing point markers', async () => {
    const onSelectionChange = vi.fn();
    await renderWithI18n(
      <SynonBiomedImageArtifactViewer
        filename='assay.png'
        contentUrl='/api/artifacts/image-1'
        annotations={[
          {
            id: 'point-1',
            artifactId: 'artifact-1',
            targetKey: 'av:version-1',
            label: '①',
            contentChecksum: 'checksum',
            type: 'point',
            text: 'Inspect this band',
            xPercent: 25,
            yPercent: 75,
            startLine: null,
            startColumn: null,
            endLine: null,
            endColumn: null,
            selectionText: null,
            pageNumber: null,
            selectionPrefix: null,
            screenshotArtifactId: null,
            elementSelector: null,
            elementDescriptor: null,
            addressedAt: null,
            addressedInFrameId: null,
            createdAt: '2026-07-13T00:00:00.000Z',
          },
        ]}
        onSelectionChange={onSelectionChange}
      />
    );
    const image = screen.getByRole('img', { name: 'assay.png' });
    Object.defineProperty(image, 'getBoundingClientRect', {
      value: () => ({ left: 100, top: 50, width: 400, height: 200, right: 500, bottom: 250 }),
    });
    fireEvent.load(image);
    fireEvent.click(image, { clientX: 200, clientY: 200 });

    expect(onSelectionChange).toHaveBeenLastCalledWith({
      type: 'point',
      text: '图片位置 25.0%, 75.0%',
      x: 200,
      y: 200,
      xPercent: 25,
      yPercent: 75,
      pageNumber: null,
    });
    expect(screen.getByRole('button', { name: '查看批注 ①' })).toHaveStyle({ left: '25%', top: '75%' });
  });

  it('creates point and HTML-element payloads without losing their anchors', async () => {
    createAnnotation.mockResolvedValue({ id: 'annotation-canvas', text: 'Canvas note' });
    const { rerender } = await renderWithI18n(
      <ConfigProvider>
        <ArtifactSelectionAnnotationModal
          artifactId='artifact-1'
          versionId='version-1'
          selection={{
            type: 'point',
            text: 'PDF 第 3 页 · 20.0%, 40.0%',
            x: 320,
            y: 240,
            xPercent: 20,
            yPercent: 40,
            pageNumber: 3,
          }}
          onCancel={vi.fn()}
          onCreated={vi.fn()}
        />
      </ConfigProvider>
    );
    fireEvent.change(screen.getByRole('textbox', { name: '选区批注内容' }), { target: { value: 'PDF point' } });
    fireEvent.click(screen.getByRole('button', { name: '添加' }));
    await waitFor(() =>
      expect(createAnnotation).toHaveBeenCalledWith(
        'artifact-1',
        'version-1',
        expect.objectContaining({ type: 'point', xPercent: 20, yPercent: 40, pageNumber: 3 })
      )
    );

    rerender(
      <ConfigProvider>
        <ArtifactSelectionAnnotationModal
          key='html-element'
          artifactId='artifact-1'
          versionId='version-1'
          selection={{
            type: 'html_element',
            text: 'STAT6 result',
            x: 500,
            y: 300,
            xPercent: 60,
            yPercent: 35,
            elementSelector: '#result-table > tbody > tr:nth-of-type(2)',
            elementDescriptor: 'tr — STAT6 result',
          }}
          onCancel={vi.fn()}
          onCreated={vi.fn()}
        />
      </ConfigProvider>
    );
    fireEvent.change(screen.getByRole('textbox', { name: '选区批注内容' }), {
      target: { value: 'HTML element' },
    });
    fireEvent.click(screen.getByRole('button', { name: '添加' }));
    await waitFor(() =>
      expect(createAnnotation).toHaveBeenCalledWith(
        'artifact-1',
        'version-1',
        expect.objectContaining({
          type: 'html_element',
          selectionText: 'STAT6 result',
          xPercent: 60,
          yPercent: 35,
          elementSelector: '#result-table > tbody > tr:nth-of-type(2)',
          elementDescriptor: 'tr — STAT6 result',
        })
      )
    );
  });
});
