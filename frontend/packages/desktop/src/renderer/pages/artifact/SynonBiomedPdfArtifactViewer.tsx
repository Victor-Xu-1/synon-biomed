import type { SynonBiomedArtifactAnnotation } from '@/renderer/services/synonBiomedAnnotations';
import {
  logScientificPreviewError,
  resolveScientificPreviewError,
  ScientificPreviewError,
  scientificPreviewErrorKey,
} from '@/renderer/pages/conversation/Preview/components/viewers/scientificPreviewError';
import { Button, Result, Spin } from '@arco-design/web-react';
import { Minus, Plus, Refresh } from '@icon-park/react';
import React, { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Document, Page, pdfjs } from 'react-pdf';
import pdfWorkerUrl from 'pdfjs-dist/build/pdf.worker.min.mjs?url';
import 'react-pdf/dist/Page/AnnotationLayer.css';
import 'react-pdf/dist/Page/TextLayer.css';
import type { SynonBiomedArtifactCanvasSelection } from './artifactCanvasSelection';
import { clampPercent } from './artifactCanvasSelection';
import {
  findPdfTextAnnotationRange,
  normalizePdfTextAnnotationRects,
  type PdfTextAnnotationRect,
} from './pdfTextAnnotation';

pdfjs.GlobalWorkerOptions.workerSrc = pdfWorkerUrl;

const MIN_SCALE = 0.6;
const MAX_SCALE = 2;

export const SynonBiomedPdfArtifactViewer: React.FC<{
  filename: string;
  contentUrl: string;
  annotations?: SynonBiomedArtifactAnnotation[];
  onSelectionChange: (selection: SynonBiomedArtifactCanvasSelection | null) => void;
  onAnnotationClick?: (annotation: SynonBiomedArtifactAnnotation) => void;
}> = ({ filename, contentUrl, annotations = [], onSelectionChange, onAnnotationClick }) => {
  const { i18n, t } = useTranslation();
  const viewportRef = useRef<HTMLDivElement>(null);
  const [file, setFile] = useState<Uint8Array | null>(null);
  const [numPages, setNumPages] = useState(0);
  const [containerWidth, setContainerWidth] = useState(800);
  const [scale, setScale] = useState(1);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<ScientificPreviewError | null>(null);
  const [generation, setGeneration] = useState(0);

  useEffect(() => {
    const container = viewportRef.current;
    if (!container) return;
    const updateWidth = () => setContainerWidth(Math.max(320, container.clientWidth - 48));
    updateWidth();
    const observer = new ResizeObserver(updateWidth);
    observer.observe(container);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    setFile(null);
    setNumPages(0);
    setScale(1);
    onSelectionChange(null);
    void fetch(contentUrl, { credentials: 'same-origin', signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) throw new ScientificPreviewError('request-failed', { status: response.status });
        return new Uint8Array(await response.arrayBuffer());
      })
      .then((bytes) => {
        if (bytes.byteLength === 0) throw new ScientificPreviewError('empty-content');
        setFile(bytes);
      })
      .catch((reason: unknown) => {
        if (controller.signal.aborted) return;
        logScientificPreviewError('[SynonBiomedPdfArtifactViewer] Failed to load PDF', reason, 'request-failed');
        setError(resolveScientificPreviewError(reason, 'request-failed'));
        setLoading(false);
      });
    return () => controller.abort();
  }, [contentUrl, generation, onSelectionChange]);

  const pageWidth = Math.min(960, containerWidth) * scale;
  const documentFile = useMemo(() => (file ? { data: file } : null), [file]);

  const handleTextSelection = useCallback(() => {
    window.setTimeout(() => {
      const selection = window.getSelection();
      const text = selection && !selection.isCollapsed ? selection.toString().trim() : '';
      if (!selection || !text || selection.rangeCount === 0) {
        return;
      }
      const range = selection.getRangeAt(0);
      const startElement = elementForNode(range.startContainer);
      const endElement = elementForNode(range.endContainer);
      const startPage = startElement?.closest<HTMLElement>('[data-pdf-page]');
      const endPage = endElement?.closest<HTMLElement>('[data-pdf-page]');
      if (!startPage || startPage !== endPage) return;
      const pageNumber = Number(startPage.dataset.pdfPage);
      if (!Number.isSafeInteger(pageNumber) || pageNumber < 1) return;
      const rect = range.getBoundingClientRect();
      const lines = locatePdfLineRange(startPage, range);
      onSelectionChange({
        type: 'text_selection',
        text: text.slice(0, 2000),
        x: rect.right,
        y: rect.bottom,
        startLine: lines?.start ?? null,
        startColumn: null,
        endLine: lines?.end ?? null,
        endColumn: null,
        selectionPrefix: locatePdfSelectionPrefix(startPage, range),
        pageNumber,
      });
    }, 20);
  }, [onSelectionChange]);

  const selectPoint = useCallback(
    (event: React.MouseEvent<HTMLElement>, pageNumber: number) => {
      const nativeSelection = window.getSelection();
      if (nativeSelection && !nativeSelection.isCollapsed && nativeSelection.toString().trim()) return;
      const rect = event.currentTarget.getBoundingClientRect();
      if (rect.width <= 0 || rect.height <= 0) return;
      const xPercent = clampPercent(((event.clientX - rect.left) / rect.width) * 100);
      const yPercent = clampPercent(((event.clientY - rect.top) / rect.height) * 100);
      onSelectionChange({
        type: 'point',
        text: t('preview.scientific.pdf.pointSelection', {
          page: pageNumber,
          x: xPercent.toFixed(1),
          y: yPercent.toFixed(1),
        }),
        x: event.clientX,
        y: event.clientY,
        xPercent,
        yPercent,
        pageNumber,
      });
    },
    [onSelectionChange, t]
  );

  return (
    <div
      ref={viewportRef}
      className='relative size-full min-h-640px overflow-auto bg-fill-2'
      onMouseUp={handleTextSelection}
    >
      <div className='sticky top-8px z-20 mx-auto mb-8px flex w-fit items-center gap-4px border border-solid border-[var(--color-border-2)] bg-1 p-4px shadow-sm'>
        <Button
          type='text'
          size='mini'
          aria-label={t('preview.scientific.pdf.zoomOut')}
          title={t('preview.scientific.zoomOut')}
          icon={<Minus theme='outline' size={14} />}
          disabled={scale <= MIN_SCALE}
          onClick={() => setScale((value) => Math.max(MIN_SCALE, Number((value - 0.2).toFixed(2))))}
        />
        <Button
          type='text'
          size='mini'
          aria-label={t('preview.scientific.pdf.resetZoom')}
          title={t('preview.scientific.resetZoom')}
          icon={<Refresh theme='outline' size={14} />}
          onClick={() => setScale(1)}
        >
          {Math.round(scale * 100)}%
        </Button>
        <Button
          type='text'
          size='mini'
          aria-label={t('preview.scientific.pdf.zoomIn')}
          title={t('preview.scientific.zoomIn')}
          icon={<Plus theme='outline' size={14} />}
          disabled={scale >= MAX_SCALE}
          onClick={() => setScale((value) => Math.min(MAX_SCALE, Number((value + 0.2).toFixed(2))))}
        />
        {numPages > 0 && (
          <span className='px-4px text-11px text-t-tertiary'>
            {t('preview.scientific.pdf.pageCount', {
              count: numPages,
              formattedCount: numPages.toLocaleString(i18n.resolvedLanguage),
            })}
          </span>
        )}
      </div>

      {error ? (
        <div className='h-420px flex-center px-24px'>
          <Result
            status='error'
            title={t('preview.scientific.pdf.loadFailed')}
            subTitle={t(scientificPreviewErrorKey(error), {
              kind: t('preview.scientific.pdf.kind'),
              ...error.details,
            })}
            extra={<Button onClick={() => setGeneration((value) => value + 1)}>{t('preview.scientific.retry')}</Button>}
          />
        </div>
      ) : !documentFile ? (
        <div className='h-420px flex-center' role='status' aria-label={t('preview.scientific.pdf.loading')}>
          <Spin />
        </div>
      ) : (
        <Document
          file={documentFile}
          loading={null}
          onLoadSuccess={({ numPages: nextNumPages }) => {
            setNumPages(nextNumPages);
            setLoading(false);
          }}
          onLoadError={(reason) => {
            logScientificPreviewError('[SynonBiomedPdfArtifactViewer] Failed to parse PDF', reason, 'parse-failed');
            setError(resolveScientificPreviewError(reason, 'parse-failed'));
            setLoading(false);
          }}
          className='flex flex-col items-center gap-16px px-24px pb-24px'
          aria-label={t('preview.scientific.pdf.documentNamed', { name: filename })}
        >
          {Array.from({ length: numPages }, (_, index) => {
            const pageNumber = index + 1;
            return (
              <PdfArtifactPage
                key={pageNumber}
                pageNumber={pageNumber}
                pageWidth={pageWidth}
                annotations={annotations}
                onSelectPoint={selectPoint}
                onAnnotationClick={onAnnotationClick}
              />
            );
          })}
        </Document>
      )}
      {loading && documentFile && (
        <div className='pointer-events-none absolute inset-0 flex-center bg-[rgba(255,255,255,0.35)]'>
          <Spin />
        </div>
      )}
    </div>
  );
};

const PdfArtifactPage: React.FC<{
  pageNumber: number;
  pageWidth: number;
  annotations: SynonBiomedArtifactAnnotation[];
  onSelectPoint: (event: React.MouseEvent<HTMLElement>, pageNumber: number) => void;
  onAnnotationClick?: (annotation: SynonBiomedArtifactAnnotation) => void;
}> = ({ pageNumber, pageWidth, annotations, onSelectPoint, onAnnotationClick }) => {
  const { t } = useTranslation();
  const pageRef = useRef<HTMLElement>(null);
  const [textLayerRevision, setTextLayerRevision] = useState(0);
  const handleTextLayerRendered = useCallback(() => setTextLayerRevision((value) => value + 1), []);
  const pageAnnotations = useMemo(
    () => annotations.filter((annotation) => annotation.pageNumber === pageNumber),
    [annotations, pageNumber]
  );

  return (
    <section
      ref={pageRef}
      data-pdf-page={pageNumber}
      aria-label={t('preview.scientific.pdf.pageNumber', { page: pageNumber })}
      className='relative shrink-0 overflow-hidden bg-white shadow-md cursor-crosshair'
      onClick={(event) => onSelectPoint(event, pageNumber)}
    >
      <Page
        pageNumber={pageNumber}
        width={pageWidth}
        renderTextLayer
        renderAnnotationLayer
        loading={<div style={{ width: pageWidth, height: pageWidth * 1.3 }} />}
        onRenderTextLayerSuccess={handleTextLayerRendered}
      />
      <PdfTextAnnotationOverlay
        pageRef={pageRef}
        annotations={pageAnnotations.filter((annotation) => annotation.type === 'text_selection')}
        layoutRevision={textLayerRevision}
        onAnnotationClick={onAnnotationClick}
      />
      {pageAnnotations
        .filter(
          (annotation) => annotation.type === 'point' && annotation.xPercent != null && annotation.yPercent != null
        )
        .map((annotation) => (
          <button
            type='button'
            key={annotation.id}
            className='absolute z-10 size-20px -translate-x-1/2 -translate-y-1/2 rounded-full border-2 border-solid border-white bg-[rgb(var(--primary-6))] text-10px leading-16px font-[700] text-white shadow-md hover:scale-110'
            style={{ left: `${annotation.xPercent}%`, top: `${annotation.yPercent}%` }}
            title={annotation.text}
            aria-label={t('preview.artifactAnnotations.viewNamed', { label: annotation.label })}
            onMouseDown={(event) => event.stopPropagation()}
            onClick={(event) => {
              event.stopPropagation();
              onAnnotationClick?.(annotation);
            }}
          >
            {annotation.label || '•'}
          </button>
        ))}
    </section>
  );
};

const PdfTextAnnotationOverlay: React.FC<{
  pageRef: React.RefObject<HTMLElement | null>;
  annotations: SynonBiomedArtifactAnnotation[];
  layoutRevision: number;
  onAnnotationClick?: (annotation: SynonBiomedArtifactAnnotation) => void;
}> = ({ pageRef, annotations, layoutRevision, onAnnotationClick }) => {
  const { t } = useTranslation();
  const [rectsByAnnotation, setRectsByAnnotation] = useState<Map<string, PdfTextAnnotationRect[]>>(new Map());

  useLayoutEffect(() => {
    if (annotations.length === 0) {
      setRectsByAnnotation((current) => (current.size === 0 ? current : new Map()));
      return;
    }
    let animationFrame = 0;
    let attempts = 0;
    const locate = () => {
      const page = pageRef.current;
      const textLayer = page?.querySelector('.react-pdf__Page__textContent');
      if (!page || !textLayer || textLayer.childElementCount === 0) {
        if (attempts < 30) {
          attempts += 1;
          animationFrame = window.requestAnimationFrame(locate);
        }
        return;
      }
      const pageRect = page.getBoundingClientRect();
      const renderScale = page.offsetWidth > 0 ? pageRect.width / page.offsetWidth : 1;
      const next = new Map<string, PdfTextAnnotationRect[]>();
      for (const annotation of annotations) {
        if (!annotation.selectionText) continue;
        const range = findPdfTextAnnotationRange(textLayer, annotation.selectionText, annotation.selectionPrefix);
        if (!range) continue;
        const rects = normalizePdfTextAnnotationRects(range.getClientRects(), pageRect, renderScale);
        if (rects.length > 0) next.set(annotation.id, rects);
      }
      setRectsByAnnotation(next);
    };
    locate();
    return () => window.cancelAnimationFrame(animationFrame);
  }, [annotations, layoutRevision, pageRef]);

  return annotations.map((annotation) => {
    const rects = rectsByAnnotation.get(annotation.id);
    if (!rects?.length) return null;
    const anchor = rects.at(-1)!;
    return (
      <React.Fragment key={annotation.id}>
        {rects.map((rect, index) => (
          <span
            key={`${annotation.id}-${index}`}
            data-testid={`pdf-text-highlight-${annotation.id}`}
            className='pointer-events-none absolute z-5 bg-[rgba(var(--primary-6),0.28)]'
            style={rect}
          />
        ))}
        <button
          type='button'
          className='absolute z-10 size-20px rounded-full border-2 border-solid border-white bg-[rgb(var(--primary-6))] text-10px leading-16px font-[700] text-white shadow-md hover:scale-110'
          style={{ top: anchor.top + 2, left: anchor.left + anchor.width + 2 }}
          title={annotation.text}
          aria-label={t('preview.artifactAnnotations.viewNamed', { label: annotation.label })}
          onMouseDown={(event) => event.stopPropagation()}
          onClick={(event) => {
            event.stopPropagation();
            onAnnotationClick?.(annotation);
          }}
        >
          {annotation.label || '•'}
        </button>
      </React.Fragment>
    );
  });
};

function elementForNode(node: Node): Element | null {
  return node.nodeType === Node.ELEMENT_NODE ? (node as Element) : node.parentElement;
}

function locatePdfLineRange(page: HTMLElement, range: Range): { start: number; end: number } | null {
  const textLayer = page.querySelector('.react-pdf__Page__textContent');
  if (!textLayer) return null;
  const spans = Array.from(textLayer.querySelectorAll('span'))
    .map((span) => ({ span, rect: span.getBoundingClientRect() }))
    .filter(({ span, rect }) => Boolean(span.textContent?.trim()) && rect.height > 0)
    .toSorted((left, right) => left.rect.top - right.rect.top || left.rect.left - right.rect.left);
  let line = 0;
  let previousTop = Number.NEGATIVE_INFINITY;
  let start: number | null = null;
  let end: number | null = null;
  for (const { span, rect } of spans) {
    if (rect.top - previousTop > Math.max(2, rect.height * 0.5)) {
      line += 1;
      previousTop = rect.top;
    }
    if (range.intersectsNode(span)) {
      start ??= line;
      end = line;
    }
  }
  return start == null || end == null ? null : { start, end };
}

function locatePdfSelectionPrefix(page: HTMLElement, range: Range): string | null {
  const textLayer = page.querySelector('.react-pdf__Page__textContent');
  if (!textLayer || !textLayer.contains(range.startContainer)) return null;
  try {
    const prefixRange = document.createRange();
    prefixRange.selectNodeContents(textLayer);
    prefixRange.setEnd(range.startContainer, range.startOffset);
    return prefixRange.toString().slice(-80) || null;
  } catch {
    return null;
  }
}

export default SynonBiomedPdfArtifactViewer;
