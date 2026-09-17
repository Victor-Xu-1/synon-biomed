import { Button, Spin } from '@arco-design/web-react';
import { Minus, Plus, Search } from '@icon-park/react';
import type { Browser, CreateOpt, ReferenceGenome, TrackLoad, TrackType } from 'igv';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { GENOME_REFERENCE_OPTIONS, resolveGenomeReference, type GenomeReferenceId } from './genomeReferenceModel';
import { resolveGenomeTrackDefinition } from './genomeTrackModel';
import {
  logScientificPreviewError,
  resolveScientificPreviewError,
  ScientificPreviewError,
  scientificPreviewErrorKey,
} from './scientificPreviewError';

type SynonBiomedGenomeViewerProps = {
  filename: string;
  contentUrl?: string;
  content?: string;
  initialLocus?: string;
};

type OfflineGenomeCreateOpt = CreateOpt & {
  loadDefaultGenomes: false;
  showSequence: false;
};

const SynonBiomedGenomeViewer: React.FC<SynonBiomedGenomeViewerProps> = ({
  filename,
  contentUrl,
  content,
  initialLocus = '',
}) => {
  const { t } = useTranslation();
  const hostRef = useRef<HTMLDivElement>(null);
  const browserRef = useRef<Browser | null>(null);
  const igvRef = useRef<(typeof import('igv'))['default'] | null>(null);
  const [genome, setGenome] = useState<GenomeReferenceId>('hg38');
  const [locus, setLocus] = useState(initialLocus);
  const [loading, setLoading] = useState(true);
  const [ready, setReady] = useState(false);
  const [error, setError] = useState<ScientificPreviewError | null>(null);
  const trackResolution = useMemo<{
    track: ReturnType<typeof resolveGenomeTrackDefinition> | null;
    error: ScientificPreviewError | null;
  }>(() => {
    try {
      return { track: resolveGenomeTrackDefinition(filename), error: null };
    } catch (reason) {
      logScientificPreviewError('[SynonBiomedGenomeViewer] Unsupported genome track', reason, 'unsupported-format');
      return { track: null, error: resolveScientificPreviewError(reason, 'unsupported-format') };
    }
  }, [filename]);
  const { track } = trackResolution;

  useEffect(() => {
    const host = hostRef.current;
    if (!track) {
      setLoading(false);
      setReady(false);
      setError(trackResolution.error);
      return;
    }
    if (!host) {
      setLoading(false);
      setError(new ScientificPreviewError('missing-content'));
      return;
    }
    let localContentUrl: string | undefined;
    const sourceUrl =
      contentUrl ??
      (content
        ? (localContentUrl = URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' })))
        : undefined);
    if (!sourceUrl) {
      setLoading(false);
      setError(new ScientificPreviewError('missing-content'));
      return;
    }
    let active = true;
    let observer: ResizeObserver | null = null;
    setLoading(true);
    setReady(false);
    setError(null);
    host.replaceChildren();

    void import('igv')
      .then(async ({ default: igv }) => {
        if (!active) return;
        igvRef.current = igv;
        const trackConfig = {
          ...track,
          name: filename,
          url: sourceUrl,
          removable: false,
        } as TrackLoad<TrackType>;
        const reference = resolveGenomeReference(genome, t(`preview.scientific.genome.references.${genome}`));
        const options = {
          // The locked IGV runtime supports a chromosome-size-only reference,
          // but its public type omits that format. The packaged table supplies
          // coordinates without permitting any runtime reference download.
          reference: reference as unknown as ReferenceGenome,
          loadDefaultGenomes: false,
          showSequence: false,
          ...(initialLocus.trim() ? { locus: initialLocus.trim() } : {}),
          showNavigation: false,
          showSampleNames: true,
          tracks: [trackConfig],
        } satisfies OfflineGenomeCreateOpt;
        const browser = await igv.createBrowser(host, options);
        if (!active) {
          igv.removeBrowser(browser);
          return;
        }
        browserRef.current = browser;
        if (typeof ResizeObserver !== 'undefined') {
          observer = new ResizeObserver(() => browser.visibilityChange());
          observer.observe(host);
        }
        setReady(true);
      })
      .catch((reason: unknown) => {
        if (!active) return;
        logScientificPreviewError('[SynonBiomedGenomeViewer] Failed to initialize IGV', reason, 'initialize-failed');
        setError(resolveScientificPreviewError(reason, 'initialize-failed'));
      })
      .finally(() => {
        if (active) setLoading(false);
      });

    return () => {
      active = false;
      observer?.disconnect();
      const browser = browserRef.current;
      const igv = igvRef.current;
      browserRef.current = null;
      if (browser && igv) igv.removeBrowser(browser);
      host.replaceChildren();
      if (localContentUrl) URL.revokeObjectURL(localContentUrl);
    };
  }, [content, contentUrl, filename, genome, initialLocus, t, track, trackResolution.error]);

  const searchLocus = useCallback(async () => {
    const query = locus.trim();
    const browser = browserRef.current;
    if (!query || !browser) return;
    try {
      setError(null);
      await Promise.resolve(browser.search(query));
    } catch (reason) {
      logScientificPreviewError('[SynonBiomedGenomeViewer] Failed to search locus', reason, 'search-failed');
      setError(resolveScientificPreviewError(reason, 'search-failed'));
    }
  }, [locus]);

  return (
    <section className='size-full min-h-500px flex flex-col bg-1' aria-label={t('preview.scientific.genome.preview')}>
      <div className='min-h-46px px-12px py-7px flex flex-wrap items-center gap-8px border-b border-solid border-[var(--color-border-2)] bg-fill-1'>
        <label className='flex items-center gap-6px text-11px text-t-secondary'>
          {t('preview.scientific.genome.genome')}
          <select
            aria-label={t('preview.scientific.genome.reference')}
            value={genome}
            className='h-28px w-168px px-8px border border-solid border-[var(--color-border-2)] rounded-4px bg-1 text-11px text-t-primary'
            onChange={(event) => setGenome(event.target.value as GenomeReferenceId)}
          >
            {GENOME_REFERENCE_OPTIONS.map((item) => (
              <option key={item.value} value={item.value}>
                {t(item.labelKey)}
              </option>
            ))}
          </select>
        </label>
        <label className='min-w-220px flex flex-1 items-center gap-6px text-11px text-t-secondary'>
          {t('preview.scientific.genome.locus')}
          <input
            aria-label={t('preview.scientific.genome.locus')}
            value={locus}
            placeholder={t('preview.scientific.genome.locusPlaceholder')}
            className='h-28px min-w-0 flex-1 px-8px border border-solid border-[var(--color-border-2)] rounded-4px bg-1 text-11px text-t-primary'
            onChange={(event) => setLocus(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') void searchLocus();
            }}
          />
        </label>
        <Button
          size='mini'
          aria-label={t('preview.scientific.genome.searchAria')}
          title={t('preview.scientific.genome.search')}
          icon={<Search theme='outline' size={14} />}
          disabled={!ready || !locus.trim()}
          onClick={() => void searchLocus()}
        >
          {t('preview.scientific.genome.search')}
        </Button>
        <div className='h-18px border-l border-solid border-[var(--color-border-2)]' />
        <Button
          type='text'
          size='mini'
          aria-label={t('preview.scientific.genome.zoomOut')}
          title={t('preview.scientific.zoomOut')}
          icon={<Minus theme='outline' size={14} />}
          disabled={!ready}
          onClick={() => browserRef.current?.zoomOut()}
        />
        <Button
          type='text'
          size='mini'
          aria-label={t('preview.scientific.genome.zoomIn')}
          title={t('preview.scientific.zoomIn')}
          icon={<Plus theme='outline' size={14} />}
          disabled={!ready}
          onClick={() => browserRef.current?.zoomIn()}
        />
        <span className='ml-auto text-11px text-t-tertiary'>
          {track ? `${track.type} · ${track.format}` : t('preview.scientific.genome.unsupported')}
        </span>
      </div>

      <div className='relative min-h-500px flex-1 overflow-auto bg-white'>
        <div ref={hostRef} data-testid='synon-biomed-igv-host' className='min-h-500px w-full bg-white' />
        {loading ? (
          <div className='absolute inset-0 flex-center bg-1/90'>
            <Spin tip={t('preview.scientific.genome.loading')} />
          </div>
        ) : null}
        {error ? (
          <div className='absolute inset-0 flex-center px-24px bg-1'>
            <div className='max-w-560px text-center text-13px text-danger-6'>
              {t(scientificPreviewErrorKey(error), {
                kind: t('preview.scientific.genome.kind'),
                ...error.details,
              })}
            </div>
          </div>
        ) : null}
      </div>
    </section>
  );
};

export default SynonBiomedGenomeViewer;
