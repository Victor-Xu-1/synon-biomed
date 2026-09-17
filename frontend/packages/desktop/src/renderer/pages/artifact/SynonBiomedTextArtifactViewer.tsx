import { SYNON_BIOMED_TEXT_ACCEPT_HEADER } from '@/renderer/services/synonBiomedArtifactPreview';
import type { SynonBiomedArtifactAnnotation } from '@/renderer/services/synonBiomedAnnotations';
import {
  logScientificPreviewError,
  resolveScientificPreviewError,
  ScientificPreviewError,
  scientificPreviewErrorKey,
} from '@/renderer/pages/conversation/Preview/components/viewers/scientificPreviewError';
import CodeEditor, {
  type CodeEditorSelection,
} from '@/renderer/pages/conversation/Preview/components/editors/CodeEditor';
import MarkdownViewer, {
  type MarkdownTextSelection,
} from '@/renderer/pages/conversation/Preview/components/viewers/MarkdownViewer';
import HTMLRenderer, {
  type HtmlTextSelection,
  type InspectedElement,
} from '@/renderer/pages/conversation/Preview/components/renderers/HTMLRenderer';
import type { SynonBiomedArtifactCanvasSelection } from './artifactCanvasSelection';
import SynonBiomedJsonViewer from '@/renderer/pages/conversation/Preview/components/viewers/SynonBiomedJsonViewer';
import { Button, Result, Spin } from '@arco-design/web-react';
import React, { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { locateRenderedTextSelection } from './artifactTextSelection';

type TextArtifactKind = 'code' | 'markdown' | 'html';

type SynonBiomedTextArtifactViewerProps = {
  filename: string;
  contentUrl: string;
  kind: TextArtifactKind;
  language?: string;
  annotations?: SynonBiomedArtifactAnnotation[];
  onSelectionChange?: (selection: SynonBiomedArtifactCanvasSelection | null) => void;
  onAnnotationClick?: (annotation: SynonBiomedArtifactAnnotation) => void;
};

const SynonBiomedTextArtifactViewer: React.FC<SynonBiomedTextArtifactViewerProps> = ({
  filename,
  contentUrl,
  kind,
  language,
  annotations = [],
  onSelectionChange,
  onAnnotationClick,
}) => {
  const { t } = useTranslation();
  const [content, setContent] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<ScientificPreviewError | null>(null);
  const [generation, setGeneration] = useState(0);
  const [inspectMode, setInspectMode] = useState(false);

  const retry = useCallback(() => setGeneration((value) => value + 1), []);

  const handleMarkdownSelection = useCallback(
    (selection: MarkdownTextSelection | null) => {
      onSelectionChange?.(
        selection
          ? locateRenderedTextSelection(content, selection.text, selection.position.x, selection.position.y)
          : null
      );
    },
    [content, onSelectionChange]
  );

  const handleHtmlSelection = useCallback(
    (selection: HtmlTextSelection | null) => {
      onSelectionChange?.(
        selection ? locateRenderedTextSelection(content, selection.text, selection.x, selection.y) : null
      );
    },
    [content, onSelectionChange]
  );

  const handleCodeSelection = useCallback(
    (selection: CodeEditorSelection | null) => {
      onSelectionChange?.(
        selection
          ? {
              type: 'text_selection',
              text: selection.text,
              x: selection.x,
              y: selection.y,
              startLine: selection.startLine,
              startColumn: selection.startColumn,
              endLine: selection.endLine,
              endColumn: selection.endColumn,
              selectionPrefix: content.slice(Math.max(0, selection.startOffset - 80), selection.startOffset) || null,
              pageNumber: null,
            }
          : null
      );
    },
    [content, onSelectionChange]
  );

  const handleHtmlElementSelection = useCallback(
    (element: InspectedElement) => {
      if (!element.selector || element.anchorX == null || element.anchorY == null) return;
      onSelectionChange?.({
        type: 'html_element',
        text: element.text || element.descriptor || element.tag,
        x: element.anchorX,
        y: element.anchorY,
        xPercent: element.xPercent ?? 50,
        yPercent: element.yPercent ?? 50,
        elementSelector: element.selector,
        elementDescriptor: element.descriptor || element.tag,
      });
      setInspectMode(false);
    },
    [onSelectionChange]
  );

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);

    void fetch(contentUrl, {
      headers: { Accept: SYNON_BIOMED_TEXT_ACCEPT_HEADER },
      credentials: 'same-origin',
      signal: controller.signal,
    })
      .then(async (response) => {
        if (!response.ok) throw new ScientificPreviewError('request-failed', { status: response.status });
        const rawContent = await response.text();
        return language === 'json' ? formatJsonContent(rawContent) : rawContent;
      })
      .then(setContent)
      .catch((requestError: unknown) => {
        if (controller.signal.aborted) return;
        setContent('');
        logScientificPreviewError(
          '[SynonBiomedTextArtifactViewer] Failed to load text artifact',
          requestError,
          'request-failed'
        );
        setError(resolveScientificPreviewError(requestError, 'request-failed'));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });

    return () => controller.abort();
  }, [contentUrl, generation, language]);

  useEffect(() => {
    onSelectionChange?.(null);
    setInspectMode(false);
  }, [contentUrl, onSelectionChange]);

  if (loading) {
    return (
      <div
        className='size-full min-h-360px flex-center'
        role='status'
        aria-label={t('preview.scientific.text.loading')}
      >
        <Spin />
      </div>
    );
  }

  if (error) {
    return (
      <div className='size-full min-h-360px flex-center px-24px'>
        <Result
          status='error'
          title={t('preview.scientific.text.loadFailed')}
          subTitle={t(scientificPreviewErrorKey(error), {
            kind: t('preview.scientific.text.kind'),
            ...error.details,
          })}
          extra={<Button onClick={retry}>{t('preview.scientific.retry')}</Button>}
        />
      </div>
    );
  }

  if (kind === 'markdown') {
    return <MarkdownViewer content={content} onTextSelection={handleMarkdownSelection} />;
  }

  if (kind === 'html') {
    return (
      <div className='relative size-full min-h-360px overflow-hidden'>
        <HTMLRenderer
          content={content}
          inspectMode={inspectMode}
          copySuccessMessage={t('preview.scientific.text.htmlElementSelected')}
          elementAnnotations={annotations
            .filter((annotation) => annotation.type === 'html_element' && annotation.elementSelector)
            .map((annotation) => ({
              id: annotation.id,
              label: annotation.label,
              text: annotation.text,
              selector: annotation.elementSelector!,
            }))}
          onElementAnnotationClick={(annotationId) => {
            const annotation = annotations.find((item) => item.id === annotationId);
            if (annotation) onAnnotationClick?.(annotation);
          }}
          onElementSelected={handleHtmlElementSelection}
          onTextSelection={inspectMode ? undefined : handleHtmlSelection}
        />
        <Button
          size='small'
          type={inspectMode ? 'primary' : 'secondary'}
          className='absolute top-8px right-8px z-20 shadow-sm'
          aria-pressed={inspectMode}
          onClick={() => {
            setInspectMode((active) => !active);
            onSelectionChange?.(null);
          }}
        >
          {t(inspectMode ? 'preview.scientific.text.selectingElement' : 'preview.scientific.text.annotateElement')}
        </Button>
      </div>
    );
  }

  if (language === 'json') {
    return <SynonBiomedJsonViewer filename={filename} content={content} onCodeSelection={handleCodeSelection} />;
  }

  return (
    <div
      className='size-full min-h-360px overflow-hidden'
      aria-label={t('preview.scientific.contentNamed', { name: filename })}
    >
      <CodeEditor
        value={content}
        onChange={() => undefined}
        language={language}
        fileName={filename}
        readOnly
        onSelectionChange={handleCodeSelection}
      />
    </div>
  );
};

export function formatJsonContent(content: string): string {
  try {
    return JSON.stringify(JSON.parse(content), null, 2);
  } catch {
    return content;
  }
}

export default SynonBiomedTextArtifactViewer;
