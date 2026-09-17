import CodeBlock from '@/renderer/components/Markdown/CodeBlock';
import MarkdownView from '@/renderer/components/Markdown';
import { Spin } from '@arco-design/web-react';
import DOMPurify from 'dompurify';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  normalizeNotebookText,
  parseNotebookDocument,
  sliceNotebookCells,
  stripNotebookAnsi,
  type NotebookCell,
  type NotebookDocument,
  type NotebookOutput,
} from './notebookModel';
import {
  logScientificPreviewError,
  resolveScientificPreviewError,
  ScientificPreviewError,
  scientificPreviewErrorKey,
} from './scientificPreviewError';

type SynonBiomedNotebookViewerProps = {
  filename: string;
  contentUrl?: string;
  content?: string;
};

const MAX_NOTEBOOK_BYTES = 10 * 1024 * 1024;
const MAX_NOTEBOOK_CELLS = 200;

const SynonBiomedNotebookViewer: React.FC<SynonBiomedNotebookViewerProps> = ({ filename, contentUrl, content }) => {
  const { i18n, t } = useTranslation();
  const [document, setDocument] = useState<NotebookDocument | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<ScientificPreviewError | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    setLoading(true);
    setError(null);
    setDocument(null);

    void loadNotebookContent({ content, contentUrl, signal: controller.signal })
      .then(parseNotebookDocument)
      .then((nextDocument) => {
        if (active) setDocument(nextDocument);
      })
      .catch((reason: unknown) => {
        if (!active || controller.signal.aborted) return;
        logScientificPreviewError('[SynonBiomedNotebookViewer] Failed to load notebook', reason, 'parse-failed');
        setError(resolveScientificPreviewError(reason, 'parse-failed'));
      })
      .finally(() => {
        if (active) setLoading(false);
      });

    return () => {
      active = false;
      controller.abort();
    };
  }, [content, contentUrl]);

  const cells = useMemo(() => (document ? sliceNotebookCells(document.cells, MAX_NOTEBOOK_CELLS) : null), [document]);

  return (
    <section className='size-full min-h-500px flex flex-col bg-1' aria-label={t('preview.scientific.notebook.preview')}>
      <div className='min-h-46px px-14px py-7px flex flex-wrap items-center gap-10px border-b border-solid border-[var(--color-border-2)] bg-fill-1'>
        <span className='truncate text-12px font-[600] text-t-primary'>{filename}</span>
        {document ? (
          <>
            <span className='px-7px py-3px rounded-3px border border-solid border-[var(--color-border-2)] bg-1 text-10px text-t-secondary'>
              {document.kernelLabel}
            </span>
            <span className='ml-auto text-11px text-t-tertiary'>
              {t('preview.scientific.notebook.summary', {
                count: document.cells.length,
                formattedCount: document.cells.length.toLocaleString(i18n.resolvedLanguage),
                format: `${document.nbformat}${document.nbformatMinor == null ? '' : `.${document.nbformatMinor}`}`,
              })}
            </span>
          </>
        ) : null}
      </div>

      <div className='relative min-h-0 flex-1 overflow-auto bg-1'>
        {document && cells ? (
          <div className='max-w-1120px mx-auto bg-1' data-testid='synon-biomed-notebook-cells'>
            {cells.head.map((cell, index) => (
              <NotebookCellView key={`head-${index}`} cell={cell} language={document.language} index={index} />
            ))}
            {cells.hidden > 0 ? (
              <div className='px-18px py-12px text-center text-11px text-t-tertiary border-b border-solid border-[var(--color-border-2)] bg-fill-1'>
                {t('preview.scientific.notebook.hiddenCells', {
                  count: cells.hidden,
                  formattedCount: cells.hidden.toLocaleString(i18n.resolvedLanguage),
                })}
              </div>
            ) : null}
            {cells.tail.map((cell, index) => (
              <NotebookCellView
                key={`tail-${index}`}
                cell={cell}
                language={document.language}
                index={document.cells.length - cells.tail.length + index}
              />
            ))}
          </div>
        ) : null}
        {loading ? (
          <div className='absolute inset-0 flex-center bg-1/90'>
            <Spin tip={t('preview.scientific.notebook.loading')} />
          </div>
        ) : null}
        {error ? (
          <div className='absolute inset-0 flex-center px-24px bg-1'>
            <div className='max-w-560px text-center text-13px text-danger-6'>
              {t(scientificPreviewErrorKey(error), {
                kind: t('preview.scientific.notebook.kind'),
                ...error.details,
              })}
            </div>
          </div>
        ) : null}
      </div>
    </section>
  );
};

const NotebookCellView: React.FC<{ cell: NotebookCell; language: string; index: number }> = ({
  cell,
  language,
  index,
}) => {
  const { t } = useTranslation();
  const source = normalizeNotebookText(cell.source);
  return (
    <article
      className='grid grid-cols-[44px_minmax(0,1fr)] border-b border-solid border-[var(--color-border-2)] bg-1'
      data-cell-index={index}
    >
      <div className='px-5px py-14px text-right font-mono text-10px text-t-tertiary bg-fill-1 select-none'>
        {cell.cell_type === 'code' ? `[${cell.execution_count ?? ' '}]` : ''}
      </div>
      <div className='min-w-0 px-16px py-14px'>
        {cell.cell_type === 'markdown' ? (
          <MarkdownView className='text-13px leading-21px'>{source}</MarkdownView>
        ) : cell.cell_type === 'code' ? (
          <>
            <CodeBlock className={`language-${language}`} hiddenCodeCopyButton>
              {source.endsWith('\n') ? source : `${source}\n`}
            </CodeBlock>
            {cell.outputs?.length ? (
              <div
                className='mt-10px flex flex-col gap-8px'
                aria-label={t('preview.scientific.notebook.cellOutput', { number: index + 1 })}
              >
                {cell.outputs.map((output, outputIndex) => (
                  <NotebookOutputView key={outputIndex} output={output} />
                ))}
              </div>
            ) : null}
          </>
        ) : (
          <pre className='m-0 whitespace-pre-wrap break-words font-mono text-12px leading-19px text-t-secondary'>
            {source}
          </pre>
        )}
      </div>
    </article>
  );
};

const NotebookOutputView: React.FC<{ output: NotebookOutput }> = ({ output }) => {
  const { t } = useTranslation();
  if (output.output_type === 'stream') {
    const text = stripNotebookAnsi(normalizeNotebookText(output.text));
    return <OutputText text={text} tone={output.name === 'stderr' ? 'error' : 'normal'} />;
  }
  if (output.output_type === 'error') {
    const traceback = output.traceback?.length
      ? output.traceback.map(stripNotebookAnsi).join('\n')
      : `${output.ename ?? 'Error'}${output.evalue ? `: ${output.evalue}` : ''}`;
    return <OutputText text={traceback} tone='error' />;
  }

  const data = output.data;
  if (!data) return null;
  const image = resolveNotebookImage(data);
  if (image) {
    return (
      <img
        src={image.src}
        alt={t('preview.scientific.notebook.outputImage')}
        className='max-w-full h-auto object-contain'
      />
    );
  }
  const html = normalizeNotebookText(data['text/html']);
  if (html) {
    return (
      <div
        className='notebook-html-output overflow-auto text-12px leading-19px'
        dangerouslySetInnerHTML={{ __html: sanitizeNotebookHtml(html) }}
      />
    );
  }
  const text = normalizeNotebookText(data['text/plain']);
  return text ? <OutputText text={text} tone='normal' /> : null;
};

const OutputText: React.FC<{ text: string; tone: 'normal' | 'error' }> = ({ text, tone }) => (
  <pre
    className={`m-0 overflow-auto whitespace-pre-wrap break-words font-mono text-11px leading-18px ${
      tone === 'error' ? 'text-danger-6' : 'text-t-primary'
    }`}
  >
    {text}
  </pre>
);

function resolveNotebookImage(data: Record<string, string | string[]>): { src: string } | null {
  for (const mime of ['image/png', 'image/jpeg', 'image/gif', 'image/webp']) {
    const encoded = normalizeNotebookText(data[mime]);
    if (encoded) return { src: `data:${mime};base64,${encoded.replace(/\s/g, '')}` };
  }
  return null;
}

export function sanitizeNotebookHtml(source: string): string {
  const sanitized = String(
    DOMPurify.sanitize(source, {
      ALLOWED_TAGS: [
        'a',
        'b',
        'blockquote',
        'br',
        'code',
        'div',
        'em',
        'h1',
        'h2',
        'h3',
        'h4',
        'h5',
        'h6',
        'hr',
        'i',
        'img',
        'li',
        'ol',
        'p',
        'pre',
        'span',
        'strong',
        'table',
        'tbody',
        'td',
        'th',
        'thead',
        'tr',
        'u',
        'ul',
      ],
      ALLOWED_ATTR: ['alt', 'class', 'colspan', 'data-result', 'height', 'href', 'rowspan', 'src', 'title', 'width'],
      ALLOW_DATA_ATTR: true,
    })
  );
  const template = window.document.createElement('template');
  template.innerHTML = sanitized;
  template.content.querySelectorAll('a').forEach((anchor) => {
    anchor.setAttribute('target', '_blank');
    anchor.setAttribute('rel', 'noreferrer');
  });
  return template.innerHTML;
}

async function loadNotebookContent({
  content,
  contentUrl,
  signal,
}: {
  content?: string;
  contentUrl?: string;
  signal: AbortSignal;
}): Promise<string> {
  if (typeof content === 'string' && content.length > 0) {
    assertNotebookSize(new Blob([content]).size);
    return content;
  }
  if (!contentUrl) throw new ScientificPreviewError('missing-content');
  const response = await fetch(contentUrl, {
    credentials: 'include',
    headers: { Accept: 'application/x-ipynb+json, application/json, text/plain' },
    signal,
  });
  if (!response.ok) throw new ScientificPreviewError('request-failed', { status: response.status });
  const declaredLength = Number(response.headers.get('content-length'));
  if (Number.isFinite(declaredLength)) assertNotebookSize(declaredLength);
  const source = await response.text();
  assertNotebookSize(new Blob([source]).size);
  return source;
}

function assertNotebookSize(size: number): void {
  if (size > MAX_NOTEBOOK_BYTES) throw new ScientificPreviewError('too-large', { limit: '10 MB' });
}

export default SynonBiomedNotebookViewer;
