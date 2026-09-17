import PreviewLoadingState from '@/renderer/components/media/PreviewLoadingState';
import { Button } from '@arco-design/web-react';
import { Download, FileText, FolderClose, Refresh } from '@icon-park/react';
import React, { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type {
  AnnDataDistribution,
  AnnDataEmbedding,
  AnnDataOverview,
  Hdf5DatasetDetail,
  Hdf5TreeNode,
  Hdf5WorkerErrorCode,
  Hdf5WorkerResponse,
} from './synonBiomedHdf5Model';
import {
  logScientificPreviewError,
  resolveScientificPreviewError,
  ScientificPreviewError,
  scientificPreviewErrorKey,
} from './scientificPreviewError';

const MAX_BROWSER_HDF5_BYTES = 512 * 1024 * 1024;

type Props = { filename: string; contentUrl?: string };
type LoadState =
  | { status: 'loading'; phase: 'reading' | 'parsing'; progress: number | null }
  | { status: 'ready'; root: Hdf5TreeNode; nodeCount: number; truncated: boolean; overview?: AnnDataOverview }
  | { status: 'error'; error: ScientificPreviewError };

const SynonBiomedHdf5Viewer: React.FC<Props> = ({ filename, contentUrl }) => {
  const { t } = useTranslation();
  const workerRef = useRef<Worker | null>(null);
  const requestIdRef = useRef(0);
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<LoadState>(() =>
    contentUrl
      ? { status: 'loading', phase: 'reading', progress: null }
      : { status: 'error', error: new ScientificPreviewError('missing-content') }
  );
  const [activeView, setActiveView] = useState<'overview' | 'structure'>('overview');
  const [selected, setSelected] = useState<Hdf5TreeNode | null>(null);
  const [detail, setDetail] = useState<Hdf5DatasetDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState<Hdf5WorkerErrorCode | null>(null);

  useEffect(() => {
    if (!contentUrl) {
      setState({ status: 'error', error: new ScientificPreviewError('missing-content') });
      return;
    }

    const controller = new AbortController();
    const worker = new Worker(new URL('./SynonBiomedHdf5.worker.ts', import.meta.url), { type: 'module' });
    workerRef.current = worker;
    setActiveView('overview');
    setSelected(null);
    setDetail(null);
    setDetailError(null);
    setState({ status: 'loading', phase: 'reading', progress: null });

    const handleWorkerMessage = (event: MessageEvent<Hdf5WorkerResponse>) => {
      const message = event.data;
      if (message.type === 'ready') {
        setState({
          status: 'ready',
          root: message.root,
          nodeCount: message.nodeCount,
          truncated: message.truncated,
          overview: message.overview,
        });
        setSelected(message.root);
        return;
      }
      if (message.type === 'detail') {
        if (message.requestId !== requestIdRef.current) return;
        setDetail(message.detail);
        setDetailLoading(false);
        return;
      }
      if (message.requestId != null) {
        if (message.requestId !== requestIdRef.current) return;
        setDetailError(message.code);
        setDetailLoading(false);
      } else {
        logScientificPreviewError(
          '[SynonBiomedHdf5Viewer] Worker failed to parse HDF5 data',
          new ScientificPreviewError('parse-failed'),
          'parse-failed'
        );
        setState({ status: 'error', error: new ScientificPreviewError('parse-failed') });
      }
    };
    const handleWorkerError = () => {
      logScientificPreviewError(
        '[SynonBiomedHdf5Viewer] HDF5 worker failed to start',
        new ScientificPreviewError('initialize-failed'),
        'initialize-failed'
      );
      setState({ status: 'error', error: new ScientificPreviewError('initialize-failed') });
    };
    worker.addEventListener('message', handleWorkerMessage);
    worker.addEventListener('error', handleWorkerError);

    void loadHdf5Buffer(contentUrl, controller.signal, (progress) => {
      setState((current) =>
        current.status === 'loading' ? { status: 'loading', phase: 'reading', progress } : current
      );
    })
      .then((buffer) => {
        setState({ status: 'loading', phase: 'parsing', progress: null });
        worker.postMessage({ type: 'open', filename, buffer }, [buffer]);
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        logScientificPreviewError('[SynonBiomedHdf5Viewer] Failed to load HDF5 data', error, 'request-failed');
        setState({ status: 'error', error: resolveScientificPreviewError(error, 'request-failed') });
      });

    return () => {
      controller.abort();
      worker.removeEventListener('message', handleWorkerMessage);
      worker.removeEventListener('error', handleWorkerError);
      worker.terminate();
      if (workerRef.current === worker) workerRef.current = null;
    };
  }, [attempt, contentUrl, filename]);

  const selectNode = (node: Hdf5TreeNode) => {
    setSelected(node);
    setDetail(null);
    setDetailError(null);
    if (node.kind !== 'dataset') {
      setDetailLoading(false);
      return;
    }
    const requestId = requestIdRef.current + 1;
    requestIdRef.current = requestId;
    setDetailLoading(true);
    workerRef.current?.postMessage({ type: 'read', path: node.path, requestId });
  };

  if (state.status === 'loading') {
    return (
      <PreviewLoadingState
        label={t(
          state.phase === 'reading' ? 'preview.scientific.hdf5.readingNamed' : 'preview.scientific.hdf5.parsing',
          { name: filename }
        )}
        progress={state.progress}
      />
    );
  }
  if (state.status === 'error') {
    return (
      <Hdf5Failure
        filename={filename}
        contentUrl={contentUrl}
        error={state.error}
        onRetry={() => setAttempt((value) => value + 1)}
      />
    );
  }

  return (
    <div className='size-full min-h-0 flex flex-col bg-1' data-hdf5-viewer='ready'>
      <header className='shrink-0 border-b border-solid border-[var(--color-border-2)] px-16px pt-12px'>
        <div className='truncate text-13px font-600 text-t-primary' title={filename}>
          {filename}
        </div>
        <div className='mt-2px text-11px text-t-tertiary'>
          {t(state.overview ? 'preview.scientific.hdf5.dataKinds.annData' : 'preview.scientific.hdf5.dataKinds.hdf5')}
          {' · '}
          {t('preview.scientific.hdf5.nodeCount', { count: state.nodeCount })}
          {state.truncated ? ` · ${t('preview.scientific.hdf5.truncated')}` : ''}
        </div>
        <div
          className='mt-9px flex h-32px items-end gap-18px'
          role='tablist'
          aria-label={t('preview.scientific.hdf5.viewMode')}
        >
          <ViewTab active={activeView === 'overview'} onClick={() => setActiveView('overview')}>
            {t('preview.scientific.hdf5.overview')}
          </ViewTab>
          <ViewTab active={activeView === 'structure'} onClick={() => setActiveView('structure')}>
            {t('preview.scientific.hdf5.structure')}
          </ViewTab>
        </div>
      </header>
      {activeView === 'overview' ? (
        <main className='min-h-0 flex-1 overflow-auto' aria-label={t('preview.scientific.hdf5.annDataOverview')}>
          {state.overview ? (
            <AnnDataOverviewView overview={state.overview} />
          ) : (
            <GenericHdf5Overview filename={filename} nodeCount={state.nodeCount} />
          )}
        </main>
      ) : (
        <div className='min-h-0 flex-1 grid grid-cols-[minmax(180px,34%)_minmax(0,1fr)]'>
          <aside
            className='min-h-0 overflow-auto border-r border-solid border-[var(--color-border-2)]'
            aria-label={t('preview.scientific.hdf5.tree')}
          >
            <div className='py-6px'>
              <Hdf5Tree node={state.root} depth={0} selectedPath={selected?.path} onSelect={selectNode} />
            </div>
          </aside>
          <section className='min-h-0 overflow-auto p-16px' aria-label={t('preview.scientific.hdf5.nodeDetails')}>
            {selected ? (
              <Hdf5NodeDetails node={selected} detail={detail} loading={detailLoading} error={detailError} />
            ) : (
              <p className='m-0 text-12px text-t-secondary'>{t('preview.scientific.hdf5.selectNode')}</p>
            )}
          </section>
        </div>
      )}
    </div>
  );
};

const ViewTab: React.FC<{ active: boolean; onClick: () => void; children: React.ReactNode }> = ({
  active,
  onClick,
  children,
}) => (
  <button
    type='button'
    role='tab'
    aria-selected={active}
    className={`h-32px border-0 border-b-2 border-solid bg-transparent px-1px text-12px cursor-pointer ${
      active ? 'border-t-primary text-t-primary font-600' : 'border-transparent text-t-tertiary hover:text-t-secondary'
    }`}
    onClick={onClick}
  >
    {children}
  </button>
);

const AnnDataOverviewView: React.FC<{ overview: AnnDataOverview }> = ({ overview }) => {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage || i18n.language;
  const embeddingLabel = overview.embedding
    ? overview.embedding.labelKey
      ? t(`preview.scientific.hdf5.embeddingLabels.${overview.embedding.labelKey}`)
      : overview.embedding.label
    : '';

  return (
    <div className='mx-auto w-full max-w-960px px-18px py-18px'>
      <div className='grid grid-cols-3 divide-x divide-[var(--color-border-2)] border-y border-solid border-x-0 border-[var(--color-border-2)] py-12px'>
        <SummaryMetric
          label={t('preview.scientific.hdf5.metrics.cells')}
          value={formatInteger(overview.cells, locale)}
        />
        <SummaryMetric
          label={t('preview.scientific.hdf5.metrics.features')}
          value={formatInteger(overview.features, locale)}
        />
        <SummaryMetric
          label={t('preview.scientific.hdf5.metrics.expressionMatrix')}
          value={`${formatInteger(overview.cells, locale)} × ${formatInteger(overview.features, locale)}`}
        />
      </div>
      <section className='mt-22px' aria-label={t('preview.scientific.hdf5.dimensionality')}>
        <SectionHeading
          title={t('preview.scientific.hdf5.cellMap')}
          detail={
            overview.embedding
              ? t('preview.scientific.hdf5.reductionNamed', { name: embeddingLabel })
              : t('preview.scientific.hdf5.coordinates')
          }
        />
        {overview.embedding ? <EmbeddingPlot embedding={overview.embedding} /> : <MissingEmbedding />}
      </section>
      {overview.distributions.length > 0 && (
        <section className='mt-24px pb-16px' aria-label={t('preview.scientific.hdf5.sampleComposition')}>
          <SectionHeading
            title={t('preview.scientific.hdf5.sampleComposition')}
            detail={t('preview.scientific.hdf5.fromObs')}
          />
          <div className='mt-12px grid grid-cols-1 gap-22px xl:grid-cols-2'>
            {overview.distributions.map((distribution) => (
              <DistributionChart key={distribution.key} distribution={distribution} />
            ))}
          </div>
        </section>
      )}
    </div>
  );
};

const SummaryMetric: React.FC<{ label: string; value: string }> = ({ label, value }) => (
  <div className='min-w-0 px-12px first:pl-0 last:pr-0'>
    <div className='text-10px uppercase text-t-tertiary'>{label}</div>
    <div className='mt-3px truncate text-16px font-600 text-t-primary' title={value}>
      {value}
    </div>
  </div>
);

const SectionHeading: React.FC<{ title: string; detail: string }> = ({ title, detail }) => (
  <div className='flex items-baseline justify-between gap-12px'>
    <h3 className='m-0 text-13px font-600 text-t-primary'>{title}</h3>
    <span className='text-10px text-t-tertiary'>{detail}</span>
  </div>
);

const MissingEmbedding = () => {
  const { t } = useTranslation();
  return (
    <div className='mt-10px min-h-150px flex flex-col items-center justify-center border-y border-solid border-x-0 border-[var(--color-border-2)] px-18px text-center'>
      <div className='text-12px font-600 text-t-primary'>{t('preview.scientific.hdf5.missingEmbeddingTitle')}</div>
      <p className='m-0 mt-6px max-w-520px text-11px leading-18px text-t-secondary'>
        {t('preview.scientific.hdf5.missingEmbeddingDetail')}
      </p>
    </div>
  );
};

const DistributionChart: React.FC<{ distribution: AnnDataDistribution }> = ({ distribution }) => {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage || i18n.language;
  const max = Math.max(...distribution.items.map((item) => item.count), 1);
  const distributionLabel = distribution.labelKey
    ? t(`preview.scientific.hdf5.distributionLabels.${distribution.labelKey}`)
    : distribution.label;
  return (
    <div className='min-w-0'>
      <div className='flex items-baseline justify-between gap-8px'>
        <h4 className='m-0 text-12px font-600 text-t-primary'>{distributionLabel}</h4>
        <span className='text-10px text-t-tertiary'>
          {t('preview.scientific.hdf5.cellCount', {
            count: distribution.total,
            formattedCount: formatInteger(distribution.total, locale),
          })}
        </span>
      </div>
      <div className='mt-8px space-y-7px'>
        {distribution.items.map((item, index) => {
          const label = item.labelKey ? t(`preview.scientific.hdf5.categories.${item.labelKey}`) : item.label;
          return (
            <div
              key={`${item.labelKey ?? item.label}:${index}`}
              className='grid grid-cols-[minmax(72px,36%)_minmax(80px,1fr)_52px] items-center gap-7px'
            >
              <span className='truncate text-10px text-t-secondary' title={label}>
                {label}
              </span>
              <div className='h-6px overflow-hidden bg-fill-2'>
                <div
                  className='h-full bg-[var(--color-text-2)]'
                  style={{ width: `${Math.max(2, (item.count / max) * 100)}%` }}
                />
              </div>
              <span className='text-right text-10px tabular-nums text-t-tertiary'>
                {formatPercent(item.count, distribution.total, locale)}
              </span>
            </div>
          );
        })}
      </div>
    </div>
  );
};

const EmbeddingPlot: React.FC<{ embedding: AnnDataEmbedding }> = ({ embedding }) => {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage || i18n.language;
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    return renderEmbedding(canvas, embedding.points);
  }, [embedding]);
  return (
    <div className='mt-10px'>
      <canvas
        ref={canvasRef}
        className='block h-280px w-full border-y border-solid border-x-0 border-[var(--color-border-2)]'
      />
      <div className='mt-6px text-10px text-t-tertiary'>
        {t('preview.scientific.hdf5.displayedCells', {
          sampled: formatInteger(embedding.sampledPoints, locale),
          total: formatInteger(embedding.totalPoints, locale),
        })}
        {embedding.source === 'derived' ? ` · ${t('preview.scientific.hdf5.derivedPreview')}` : ''}
      </div>
    </div>
  );
};

const GenericHdf5Overview: React.FC<{ filename: string; nodeCount: number }> = ({ filename, nodeCount }) => {
  const { t, i18n } = useTranslation();
  return (
    <div className='size-full min-h-240px flex flex-col items-center justify-center px-24px text-center'>
      <h3 className='m-0 text-13px font-600 text-t-primary'>{filename}</h3>
      <p className='m-0 mt-6px text-11px text-t-secondary'>
        {t('preview.scientific.hdf5.genericOverview', {
          count: nodeCount,
          formattedCount: formatInteger(nodeCount, i18n.resolvedLanguage || i18n.language),
        })}
      </p>
    </div>
  );
};

const Hdf5Tree: React.FC<{
  node: Hdf5TreeNode;
  depth: number;
  selectedPath?: string;
  onSelect: (node: Hdf5TreeNode) => void;
}> = ({ node, depth, selectedPath, onSelect }) => {
  const [expanded, setExpanded] = useState(depth < 1);
  const hasChildren = Boolean(node.children?.length);
  const selected = node.path === selectedPath;
  return (
    <div role='treeitem' aria-expanded={hasChildren ? expanded : undefined} aria-selected={selected}>
      <button
        type='button'
        className={`w-full min-w-0 h-30px flex items-center gap-6px border-0 px-8px text-left text-12px cursor-pointer ${selected ? 'bg-fill-2 text-t-primary' : 'bg-transparent text-t-secondary hover:bg-fill-1'}`}
        style={{ paddingLeft: 8 + depth * 14 }}
        title={node.path}
        onClick={() => {
          onSelect(node);
          if (hasChildren) setExpanded((value) => !value);
        }}
      >
        {node.kind === 'group' ? <FolderClose theme='outline' size={14} /> : <FileText theme='outline' size={14} />}
        <span className='min-w-0 flex-1 truncate'>{node.name}</span>
        {node.shape && <span className='shrink-0 text-10px text-t-tertiary'>{formatShape(node.shape)}</span>}
      </button>
      {hasChildren && expanded && (
        <div role='group'>
          {node.children?.map((child) => (
            <Hdf5Tree
              key={`${child.path}:${child.name}`}
              node={child}
              depth={depth + 1}
              selectedPath={selectedPath}
              onSelect={onSelect}
            />
          ))}
        </div>
      )}
    </div>
  );
};

const Hdf5NodeDetails: React.FC<{
  node: Hdf5TreeNode;
  detail: Hdf5DatasetDetail | null;
  loading: boolean;
  error: Hdf5WorkerErrorCode | null;
}> = ({ node, detail, loading, error }) => {
  const { t } = useTranslation();
  return (
    <div className='max-w-920px'>
      <div className='text-11px uppercase text-t-tertiary'>{t(`preview.scientific.hdf5.nodeKinds.${node.kind}`)}</div>
      <h3 className='m-0 mt-4px break-all text-15px font-600 text-t-primary'>{node.path}</h3>
      <dl className='mt-14px grid grid-cols-[72px_minmax(0,1fr)] gap-x-12px gap-y-8px text-12px'>
        {node.shape && (
          <>
            <dt className='text-t-tertiary'>{t('preview.scientific.hdf5.shape')}</dt>
            <dd className='m-0 font-mono text-t-primary'>{formatShape(node.shape)}</dd>
          </>
        )}
        {node.dtype && (
          <>
            <dt className='text-t-tertiary'>{t('preview.scientific.hdf5.dtype')}</dt>
            <dd className='m-0 break-all font-mono text-t-primary'>{node.dtype}</dd>
          </>
        )}
      </dl>
      {node.attributes.length > 0 && (
        <div className='mt-18px'>
          <h4 className='m-0 text-12px font-600 text-t-primary'>{t('preview.scientific.hdf5.attributes')}</h4>
          <div className='mt-8px divide-y divide-[var(--color-border-2)] border-y border-solid border-x-0 border-[var(--color-border-2)]'>
            {node.attributes.map((attribute) => (
              <div
                key={attribute.name}
                className='grid grid-cols-[minmax(90px,30%)_minmax(0,1fr)] gap-12px py-7px text-11px'
              >
                <span className='break-all text-t-secondary'>{attribute.name}</span>
                <code className='break-all text-t-primary'>{attribute.value}</code>
              </div>
            ))}
          </div>
        </div>
      )}
      {node.kind === 'dataset' && (
        <div className='mt-18px'>
          <h4 className='m-0 text-12px font-600 text-t-primary'>{t('preview.scientific.hdf5.dataSlice')}</h4>
          {loading && <PreviewLoadingState label={t('preview.scientific.hdf5.readingSlice')} />}
          {error && <p className='text-12px text-danger-6'>{t(hdf5WorkerErrorKey(error))}</p>}
          {detail && (
            <>
              <pre className='mt-8px max-h-420px overflow-auto border border-solid border-[var(--color-border-2)] bg-fill-1 p-12px text-11px leading-18px text-t-primary'>
                {JSON.stringify(detail.preview, null, 2)}
              </pre>
              {detail.truncated && (
                <p className='m-0 mt-6px text-11px text-t-tertiary'>{t('preview.scientific.hdf5.sliceTruncated')}</p>
              )}
            </>
          )}
        </div>
      )}
    </div>
  );
};

function renderEmbedding(canvas: HTMLCanvasElement, points: [number, number][]): () => void {
  const draw = () => {
    const rect = canvas.getBoundingClientRect();
    const ratio = Math.min(window.devicePixelRatio || 1, 2);
    canvas.width = Math.max(1, Math.floor(rect.width * ratio));
    canvas.height = Math.max(1, Math.floor(rect.height * ratio));
    const context = canvas.getContext('2d');
    if (!context) return;
    const xs = points.map((point) => point[0]);
    const ys = points.map((point) => point[1]);
    const minX = Math.min(...xs);
    const maxX = Math.max(...xs);
    const minY = Math.min(...ys);
    const maxY = Math.max(...ys);
    const padding = 18 * ratio;
    context.clearRect(0, 0, canvas.width, canvas.height);
    context.fillStyle = getComputedStyle(canvas).getPropertyValue('--color-text-1').trim() || '#3f3f46';
    context.globalAlpha = Math.min(0.62, Math.max(0.3, 2400 / points.length));
    for (const [x, y] of points) {
      const px = padding + ((x - minX) / Math.max(maxX - minX, 1)) * (canvas.width - padding * 2);
      const py = padding + (1 - (y - minY) / Math.max(maxY - minY, 1)) * (canvas.height - padding * 2);
      context.fillRect(px, py, Math.max(1.5, ratio * 1.2), Math.max(1.5, ratio * 1.2));
    }
  };
  draw();
  const observer = new ResizeObserver(draw);
  observer.observe(canvas);
  return () => observer.disconnect();
}

async function loadHdf5Buffer(
  url: string,
  signal: AbortSignal,
  onProgress: (progress: number | null) => void
): Promise<ArrayBuffer> {
  const response = await fetch(url, {
    credentials: 'same-origin',
    headers: { Accept: 'application/x-hdf5, application/octet-stream' },
    signal,
  });
  if (!response.ok) throw new ScientificPreviewError('request-failed', { status: response.status });
  const declaredSize = Number(response.headers.get('content-length') || 0);
  if (declaredSize > MAX_BROWSER_HDF5_BYTES) throw new ScientificPreviewError('too-large', { limit: '512 MB' });
  if (!response.body) return response.arrayBuffer();
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let received = 0;
  /* eslint-disable no-await-in-loop */
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    received += value.byteLength;
    if (received > MAX_BROWSER_HDF5_BYTES) {
      await reader.cancel();
      throw new ScientificPreviewError('too-large', { limit: '512 MB' });
    }
    chunks.push(value);
    onProgress(declaredSize > 0 ? Math.round((received / declaredSize) * 100) : null);
  }
  /* eslint-enable no-await-in-loop */
  const merged = new Uint8Array(received);
  let offset = 0;
  for (const chunk of chunks) {
    merged.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return merged.buffer;
}

function formatShape(shape: number[]): string {
  return `[${shape.join(', ')}]`;
}
function formatInteger(value: number, locale: string): string {
  return new Intl.NumberFormat(locale).format(value);
}
function formatPercent(value: number, total: number, locale: string): string {
  const ratio = total > 0 ? value / total : 0;
  return new Intl.NumberFormat(locale, {
    style: 'percent',
    maximumFractionDigits: ratio > 0 && ratio < 0.01 ? 1 : 0,
  }).format(ratio);
}

function hdf5WorkerErrorKey(code: Hdf5WorkerErrorCode): string {
  if (code === 'not-ready') return 'preview.scientific.hdf5.detailErrors.notReady';
  if (code === 'not-dataset') return 'preview.scientific.hdf5.detailErrors.notDataset';
  return 'preview.scientific.hdf5.detailErrors.readFailed';
}

const Hdf5Failure: React.FC<{
  filename: string;
  contentUrl?: string;
  error: ScientificPreviewError;
  onRetry: () => void;
}> = ({ filename, contentUrl, error, onRetry }) => {
  const { t } = useTranslation();
  return (
    <div className='size-full min-h-240px flex flex-col items-center justify-center px-24px text-center bg-1'>
      <h3 className='m-0 text-14px font-600 text-t-primary'>
        {t('preview.scientific.hdf5.cannotPreviewNamed', { name: filename })}
      </h3>
      <p className='m-0 mt-8px max-w-480px text-12px leading-20px text-t-secondary'>
        {t(scientificPreviewErrorKey(error), {
          kind: t('preview.scientific.hdf5.kind'),
          ...error.details,
        })}
      </p>
      <div className='mt-16px flex items-center gap-8px'>
        <Button icon={<Refresh theme='outline' size={15} />} onClick={onRetry}>
          {t('preview.scientific.retry')}
        </Button>
        {contentUrl && (
          <a href={contentUrl} download={filename} className='no-underline'>
            <Button icon={<Download theme='outline' size={15} />}>
              {t('preview.scientific.hdf5.downloadOriginal')}
            </Button>
          </a>
        )}
      </div>
    </div>
  );
};

export default SynonBiomedHdf5Viewer;
