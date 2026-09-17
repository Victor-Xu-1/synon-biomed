import { Button, Spin } from '@arco-design/web-react';
import { Minus, Plus, Redo } from '@icon-park/react';
import React, { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  parseScientificAlignment,
  type ScientificAlignmentModel,
  type ScientificAlignmentSequence,
} from './scientificMsaModel';
import {
  logScientificPreviewError,
  resolveScientificPreviewError,
  ScientificPreviewError,
  scientificPreviewErrorKey,
} from './scientificPreviewError';

type SynonBiomedMsaViewerProps = {
  filename: string;
  contentUrl?: string;
  content?: string;
};

type NightingaleMsaElement = HTMLElement & {
  data?: ScientificAlignmentSequence[];
};

const COLOR_SCHEMES = ['clustal2', 'clustal', 'nucleotide', 'conservation', 'hydro', 'taylor', 'zappo'] as const;

const DEFAULT_ZOOM = 1;
const MIN_ZOOM = 0.6;
const MAX_ZOOM = 1.8;

const SynonBiomedMsaViewer: React.FC<SynonBiomedMsaViewerProps> = ({ filename, contentUrl, content }) => {
  const { i18n, t } = useTranslation();
  const hostRef = useRef<HTMLDivElement>(null);
  const viewerRef = useRef<NightingaleMsaElement | null>(null);
  const [model, setModel] = useState<ScientificAlignmentModel | null>(null);
  const [loading, setLoading] = useState(true);
  const [viewerReady, setViewerReady] = useState(false);
  const [error, setError] = useState<ScientificPreviewError | null>(null);
  const [colorScheme, setColorScheme] = useState('clustal2');
  const [zoom, setZoom] = useState(DEFAULT_ZOOM);

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    setLoading(true);
    setError(null);
    setModel(null);

    void loadAlignmentContent({ filename, content, contentUrl, signal: controller.signal })
      .then((source) =>
        parseScientificAlignment(source, filename, (index) =>
          t('preview.scientific.msa.defaultSequenceName', { index })
        )
      )
      .then((nextModel) => {
        if (!active) return;
        setModel(nextModel);
        setColorScheme(nextModel.isNucleotide ? 'nucleotide' : 'clustal2');
      })
      .catch((reason: unknown) => {
        if (!active || controller.signal.aborted) return;
        logScientificPreviewError('[SynonBiomedMsaViewer] Failed to load alignment', reason, 'parse-failed');
        setError(resolveScientificPreviewError(reason, 'parse-failed'));
      })
      .finally(() => {
        if (active) setLoading(false);
      });

    return () => {
      active = false;
      controller.abort();
    };
  }, [content, contentUrl, filename, i18n.resolvedLanguage, t]);

  useEffect(() => {
    const host = hostRef.current;
    if (!host || !model) return;
    let active = true;
    setViewerReady(false);

    void import('@nightingale-elements/nightingale-msa')
      .then(
        () =>
          new Promise<void>((resolve) => {
            window.requestAnimationFrame(() => resolve());
          })
      )
      .then(() => {
        if (!active) return;
        const viewer = document.createElement('nightingale-msa') as NightingaleMsaElement;
        viewer.setAttribute('data-testid', 'synon-biomed-msa-canvas');
        viewer.setAttribute('color-scheme', model.isNucleotide ? 'nucleotide' : 'clustal2');
        viewer.setAttribute('label-width', '180');
        viewer.setAttribute('tile-height', '20');
        viewer.style.display = 'block';
        host.replaceChildren(viewer);
        viewerRef.current = viewer;
        return new Promise<void>((resolve) => {
          window.requestAnimationFrame(() => resolve());
        });
      })
      .then(() => {
        if (!active || !viewerRef.current) return;
        viewerRef.current.data = model.sequences;
        setViewerReady(true);
      })
      .catch((reason: unknown) => {
        if (!active) return;
        logScientificPreviewError('[SynonBiomedMsaViewer] Failed to initialize viewer', reason, 'initialize-failed');
        setError(resolveScientificPreviewError(reason, 'initialize-failed'));
      });

    return () => {
      active = false;
      viewerRef.current = null;
      host.replaceChildren();
    };
  }, [model]);

  const viewerSize = useMemo(() => {
    if (!model) return { width: 0, height: 0 };
    return {
      width: Math.max(720, Math.round(200 + model.positionCount * 15 * zoom)),
      height: Math.max(360, Math.min(960, model.sequences.length * 20 + 36)),
    };
  }, [model, zoom]);

  useEffect(() => {
    const viewer = viewerRef.current;
    if (!viewer || !model) return;
    viewer.setAttribute('color-scheme', colorScheme);
    viewer.setAttribute('width', String(viewerSize.width));
    viewer.setAttribute('height', String(viewerSize.height));
  }, [colorScheme, model, viewerReady, viewerSize]);

  return (
    <section className='size-full min-h-360px flex flex-col bg-1' aria-label={t('preview.scientific.msa.preview')}>
      <div className='h-44px px-12px flex items-center gap-8px border-b border-solid border-[var(--color-border-2)] bg-fill-1'>
        <label className='flex items-center gap-6px text-11px text-t-secondary'>
          {t('preview.scientific.msa.color')}
          <select
            aria-label={t('preview.scientific.msa.colorScheme')}
            value={colorScheme}
            className='h-28px w-124px px-8px border border-solid border-[var(--color-border-2)] rounded-4px bg-1 text-11px text-t-primary'
            onChange={(event) => setColorScheme(event.target.value)}
          >
            {COLOR_SCHEMES.map((scheme) => (
              <option key={scheme} value={scheme}>
                {t(`preview.scientific.msa.schemes.${scheme}`)}
              </option>
            ))}
          </select>
        </label>
        <div className='h-18px border-l border-solid border-[var(--color-border-2)]' />
        <Button
          type='text'
          size='mini'
          aria-label={t('preview.scientific.msa.zoomOut')}
          title={t('preview.scientific.msa.zoomOut')}
          icon={<Minus theme='outline' size={14} />}
          disabled={zoom <= MIN_ZOOM}
          onClick={() => setZoom((current) => Math.max(MIN_ZOOM, Number((current - 0.2).toFixed(1))))}
        />
        <Button
          type='text'
          size='mini'
          aria-label={t('preview.scientific.resetZoom')}
          title={t('preview.scientific.resetZoom')}
          icon={<Redo theme='outline' size={14} />}
          onClick={() => setZoom(DEFAULT_ZOOM)}
        />
        <Button
          type='text'
          size='mini'
          aria-label={t('preview.scientific.msa.zoomIn')}
          title={t('preview.scientific.msa.zoomIn')}
          icon={<Plus theme='outline' size={14} />}
          disabled={zoom >= MAX_ZOOM}
          onClick={() => setZoom((current) => Math.min(MAX_ZOOM, Number((current + 0.2).toFixed(1))))}
        />
        {model ? (
          <span className='ml-auto text-11px text-t-tertiary'>
            {t('preview.scientific.msa.stats', {
              sequences: model.sequences.length.toLocaleString(i18n.resolvedLanguage),
              positions: model.positionCount.toLocaleString(i18n.resolvedLanguage),
            })}
          </span>
        ) : null}
      </div>

      <div className='relative min-h-0 flex-1 overflow-auto bg-white'>
        <div ref={hostRef} style={{ width: viewerSize.width, minHeight: viewerSize.height }} />
        {loading || (model && !viewerReady) ? (
          <div className='absolute inset-0 flex-center bg-1/90'>
            <Spin tip={t('preview.scientific.msa.loading')} />
          </div>
        ) : null}
        {error ? (
          <div className='absolute inset-0 flex-center px-24px bg-1'>
            <div className='max-w-520px text-center text-13px text-danger-6'>
              {t(scientificPreviewErrorKey(error), {
                kind: t('preview.scientific.msa.kind'),
                ...error.details,
              })}
            </div>
          </div>
        ) : null}
      </div>
    </section>
  );
};

async function loadAlignmentContent({
  filename,
  content,
  contentUrl,
  signal,
}: {
  filename: string;
  content?: string;
  contentUrl?: string;
  signal: AbortSignal;
}): Promise<string> {
  if (typeof content === 'string' && content.length > 0) return content;
  if (!contentUrl) throw new ScientificPreviewError('missing-content');
  const response = await fetch(contentUrl, {
    credentials: 'include',
    headers: { Accept: 'text/plain, text/x-fasta, application/octet-stream' },
    signal,
  });
  if (!response.ok) throw new ScientificPreviewError('request-failed', { status: response.status });
  const transportDecoded = response.headers.get('content-encoding')?.toLowerCase() === 'gzip';
  if (filename.toLowerCase().endsWith('.gz') && !transportDecoded) {
    const compressed = await response.arrayBuffer();
    const decompressed = await decompressGzip(compressed);
    return new TextDecoder('utf-8', { fatal: true }).decode(decompressed);
  }
  return response.text();
}

async function decompressGzip(data: ArrayBuffer): Promise<ArrayBuffer> {
  if (typeof DecompressionStream === 'undefined') {
    throw new ScientificPreviewError('initialize-failed');
  }
  const stream = new Blob([data]).stream().pipeThrough(new DecompressionStream('gzip'));
  return new Response(stream).arrayBuffer();
}

export default SynonBiomedMsaViewer;
