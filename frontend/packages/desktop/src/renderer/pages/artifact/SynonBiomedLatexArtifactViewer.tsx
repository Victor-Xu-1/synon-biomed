import type { SynonBiomedArtifactAnnotation } from '@/renderer/services/synonBiomedAnnotations';
import {
  logScientificPreviewError,
  resolveScientificPreviewError,
  ScientificPreviewError,
  scientificPreviewErrorKey,
} from '@/renderer/pages/conversation/Preview/components/viewers/scientificPreviewError';
import { Button, Radio, Result, Spin, Tooltip } from '@arco-design/web-react';
import { Code, Minus, Plus, Refresh, Text } from '@icon-park/react';
import DOMPurify from 'dompurify';
import katex from 'katex';
import React, { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import 'katex/dist/katex.min.css';
import type { SynonBiomedArtifactCanvasSelection } from './artifactCanvasSelection';
import { locateRenderedTextSelection } from './artifactTextSelection';
import { findPdfTextAnnotationRange, normalizePdfTextAnnotationRects } from './pdfTextAnnotation';
import {
  convertLatexDocumentToHtml,
  resolveLatexArtifactResources,
  type LatexArtifactResourceUrls,
} from './latexDocument';
import './SynonBiomedLatexArtifactViewer.css';

const MIN_SCALE = 0.7;
const MAX_SCALE = 1.6;

function renderLatexMathPlaceholders(html: string): string {
  if (!html || typeof DOMParser === 'undefined') return html;
  const document = new DOMParser().parseFromString(html, 'text/html');
  for (const element of document.querySelectorAll<HTMLElement>('.inline-math, .display-math')) {
    const source = element.textContent ?? '';
    try {
      element.innerHTML = katex.renderToString(source, {
        displayMode: element.classList.contains('display-math'),
        throwOnError: true,
        strict: 'warn',
        trust: false,
      });
    } catch {
      element.textContent = source;
      element.classList.add('latex-render-error');
    }
  }
  return document.body.innerHTML;
}

type LatexMode = 'preview' | 'source';

export const SynonBiomedLatexArtifactViewer: React.FC<{
  filename: string;
  contentUrl: string;
  annotations?: SynonBiomedArtifactAnnotation[];
  resourceUrls?: LatexArtifactResourceUrls;
  onSelectionChange: (selection: SynonBiomedArtifactCanvasSelection | null) => void;
  onAnnotationClick?: (annotation: SynonBiomedArtifactAnnotation) => void;
}> = ({ filename, contentUrl, annotations = [], resourceUrls = {}, onSelectionChange, onAnnotationClick }) => {
  const { t } = useTranslation();
  const articleRef = useRef<HTMLElement>(null);
  const [content, setContent] = useState('');
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<ScientificPreviewError | null>(null);
  const [generation, setGeneration] = useState(0);
  const [mode, setMode] = useState<LatexMode>('preview');
  const [scale, setScale] = useState(1);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setLoadError(null);
    setContent('');
    setMode('preview');
    setScale(1);
    onSelectionChange(null);
    void fetch(contentUrl, {
      credentials: 'same-origin',
      signal: controller.signal,
      headers: { accept: 'application/x-tex, text/x-tex, text/plain' },
    })
      .then(async (response) => {
        if (!response.ok) throw new ScientificPreviewError('request-failed', { status: response.status });
        return response.text();
      })
      .then((source) => {
        if (!source.trim()) throw new ScientificPreviewError('empty-content');
        setContent(source);
      })
      .catch((reason: unknown) => {
        if (controller.signal.aborted) return;
        logScientificPreviewError('[SynonBiomedLatexArtifactViewer] Failed to load LaTeX', reason, 'request-failed');
        setLoadError(resolveScientificPreviewError(reason, 'request-failed'));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [contentUrl, generation, onSelectionChange]);

  const conversion = useMemo(() => {
    if (!content) return { html: '', warnings: [] as string[], error: false };
    try {
      const result = convertLatexDocumentToHtml(content);
      const resourceHtml = resolveLatexArtifactResources(result.html, resourceUrls);
      const renderedHtml = renderLatexMathPlaceholders(resourceHtml);
      return {
        html: DOMPurify.sanitize(renderedHtml, {
          USE_PROFILES: { html: true },
          FORBID_TAGS: ['script', 'style', 'iframe', 'object', 'embed', 'form'],
          FORBID_ATTR: ['style', 'srcdoc'],
        }),
        warnings: result.warnings,
        error: false,
      };
    } catch (reason) {
      logScientificPreviewError('[SynonBiomedLatexArtifactViewer] Failed to parse LaTeX', reason, 'parse-failed');
      return { html: '', warnings: [], error: true };
    }
  }, [content, resourceUrls]);

  const handleSelection = useCallback(() => {
    window.setTimeout(() => {
      const selection = window.getSelection();
      const selectedText = selection && !selection.isCollapsed ? selection.toString().trim() : '';
      if (!selection || !selectedText || selection.rangeCount === 0 || !articleRef.current) return;
      const range = selection.getRangeAt(0);
      if (!articleRef.current.contains(range.commonAncestorContainer)) return;
      const rect = range.getBoundingClientRect();
      onSelectionChange(locateRenderedTextSelection(content, selectedText.slice(0, 2000), rect.right, rect.bottom));
    }, 20);
  }, [content, onSelectionChange]);

  return (
    <div className='relative size-full min-h-520px overflow-auto bg-fill-2'>
      <div className='sticky top-0 z-20 flex min-h-44px items-center justify-between gap-8px border-x-0 border-t-0 border-b border-solid border-[var(--color-border-2)] bg-1 px-12px py-6px shadow-sm'>
        <Radio.Group
          name='latex-view-mode'
          type='button'
          size='small'
          value={mode}
          onChange={(value: LatexMode) => {
            setMode(value as LatexMode);
            onSelectionChange(null);
          }}
        >
          <Radio value='preview'>
            <span className='inline-flex items-center gap-5px'>
              <Text theme='outline' size={14} />
              {t('preview.scientific.preview')}
            </span>
          </Radio>
          <Radio value='source'>
            <span className='inline-flex items-center gap-5px'>
              <Code theme='outline' size={14} />
              {t('preview.scientific.source')}
            </span>
          </Radio>
        </Radio.Group>
        <div className='flex items-center gap-2px'>
          <Tooltip content={t('preview.scientific.zoomOut')}>
            <Button
              type='text'
              size='mini'
              aria-label={t('preview.scientific.latex.zoomOut')}
              icon={<Minus theme='outline' size={14} />}
              disabled={scale <= MIN_SCALE}
              onClick={() => setScale((value) => Math.max(MIN_SCALE, Number((value - 0.1).toFixed(2))))}
            />
          </Tooltip>
          <Button
            type='text'
            size='mini'
            aria-label={t('preview.scientific.latex.resetZoom')}
            icon={<Refresh theme='outline' size={14} />}
            onClick={() => setScale(1)}
          >
            {Math.round(scale * 100)}%
          </Button>
          <Tooltip content={t('preview.scientific.zoomIn')}>
            <Button
              type='text'
              size='mini'
              aria-label={t('preview.scientific.latex.zoomIn')}
              icon={<Plus theme='outline' size={14} />}
              disabled={scale >= MAX_SCALE}
              onClick={() => setScale((value) => Math.min(MAX_SCALE, Number((value + 0.1).toFixed(2))))}
            />
          </Tooltip>
        </div>
      </div>

      {loading ? (
        <div className='h-420px flex-center' role='status' aria-label={t('preview.scientific.latex.loading')}>
          <Spin />
        </div>
      ) : loadError ? (
        <div className='h-420px flex-center px-24px'>
          <Result
            status='error'
            title={t('preview.scientific.latex.loadFailed')}
            subTitle={t(scientificPreviewErrorKey(loadError), {
              kind: t('preview.scientific.latex.kind'),
              ...loadError.details,
            })}
            extra={<Button onClick={() => setGeneration((value) => value + 1)}>{t('preview.scientific.retry')}</Button>}
          />
        </div>
      ) : mode === 'source' ? (
        <pre
          className='m-0 min-h-full overflow-auto whitespace-pre-wrap break-words px-20px py-18px text-12px leading-19px text-t-primary font-mono'
          style={{ fontSize: `${scale * 12}px`, lineHeight: `${scale * 19}px` }}
          aria-label={t('preview.scientific.latex.sourceNamed', { name: filename })}
        >
          {content}
        </pre>
      ) : conversion.error ? (
        <div className='h-420px flex-center px-24px'>
          <Result
            status='error'
            title={t('preview.scientific.latex.parseFailed')}
            subTitle={t('preview.scientific.errors.parseFailed', {
              kind: t('preview.scientific.latex.kind'),
            })}
          />
        </div>
      ) : (
        <div className='mx-auto w-full max-w-980px px-16px py-20px md:px-28px md:py-28px'>
          <div className='relative'>
            <article
              ref={articleRef}
              className='synon-latex-paper relative min-h-420px border border-solid border-[var(--color-border-2)] bg-1 px-20px py-24px shadow-sm md:px-48px md:py-44px'
              style={{ fontSize: `${scale * 15}px` }}
              aria-label={t('preview.scientific.latex.documentNamed', { name: filename })}
              onMouseUp={handleSelection}
              dangerouslySetInnerHTML={{ __html: conversion.html }}
            />
            <LatexTextAnnotationOverlay
              articleRef={articleRef}
              annotations={annotations.filter((annotation) => annotation.type === 'text_selection')}
              layoutRevision={`${content.length}:${scale}:${mode}`}
              onAnnotationClick={onAnnotationClick}
            />
          </div>
        </div>
      )}
    </div>
  );
};

const LatexTextAnnotationOverlay: React.FC<{
  articleRef: React.RefObject<HTMLElement | null>;
  annotations: SynonBiomedArtifactAnnotation[];
  layoutRevision: string;
  onAnnotationClick?: (annotation: SynonBiomedArtifactAnnotation) => void;
}> = ({ articleRef, annotations, layoutRevision, onAnnotationClick }) => {
  const { t } = useTranslation();
  const [rects, setRects] = useState<Map<string, ReturnType<typeof normalizePdfTextAnnotationRects>>>(new Map());

  useLayoutEffect(() => {
    const article = articleRef.current;
    if (!article || annotations.length === 0) {
      setRects((current) => (current.size === 0 ? current : new Map()));
      return;
    }
    const articleRect = article.getBoundingClientRect();
    const next = new Map<string, ReturnType<typeof normalizePdfTextAnnotationRects>>();
    for (const annotation of annotations) {
      if (!annotation.selectionText) continue;
      const range = findPdfTextAnnotationRange(article, annotation.selectionText, annotation.selectionPrefix);
      if (!range) continue;
      const annotationRects = normalizePdfTextAnnotationRects(range.getClientRects(), articleRect, 1);
      if (annotationRects.length > 0) next.set(annotation.id, annotationRects);
    }
    setRects(next);
  }, [annotations, articleRef, layoutRevision]);

  return annotations.map((annotation) => {
    const annotationRects = rects.get(annotation.id);
    if (!annotationRects?.length) return null;
    const anchor = annotationRects.at(-1)!;
    return (
      <React.Fragment key={annotation.id}>
        {annotationRects.map((rect, index) => (
          <span
            key={`${annotation.id}-${index}`}
            data-testid={`latex-text-highlight-${annotation.id}`}
            className='pointer-events-none absolute z-5 bg-[rgba(var(--primary-6),0.25)]'
            style={{
              top: rect.top,
              left: rect.left,
              width: rect.width,
              height: rect.height,
            }}
          />
        ))}
        <button
          type='button'
          className='absolute z-10 size-20px rounded-full border-2 border-solid border-white bg-[rgb(var(--primary-6))] text-10px leading-16px font-[700] text-white shadow-md hover:scale-110'
          style={{ top: anchor.top + anchor.height, left: anchor.left + anchor.width + 2 }}
          title={annotation.text}
          aria-label={t('preview.artifactAnnotations.viewNamed', { label: annotation.label })}
          onClick={() => onAnnotationClick?.(annotation)}
        >
          {annotation.label || '\u2022'}
        </button>
      </React.Fragment>
    );
  });
};

export default SynonBiomedLatexArtifactViewer;
