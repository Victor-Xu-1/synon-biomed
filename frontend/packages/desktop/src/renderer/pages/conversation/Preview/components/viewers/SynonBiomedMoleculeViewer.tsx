import { RDKitRenderError, renderMoleculeSvg } from '@/renderer/services/rdkitBrowser';
import { Empty, Input, Spin } from '@arco-design/web-react';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  logScientificPreviewError,
  resolveScientificPreviewError,
  ScientificPreviewError,
  scientificPreviewErrorKey,
} from './scientificPreviewError';

type SynonBiomedMoleculeViewerProps = {
  filename: string;
  contentUrl?: string;
  content?: string;
};

type MoleculeRecord = {
  id: string;
  smiles: string;
  name: string;
  svg: string;
};

const MAX_MOLECULE_BYTES = 2 * 1024 * 1024;
const MAX_MOLECULES = 120;

const SynonBiomedMoleculeViewer: React.FC<SynonBiomedMoleculeViewerProps> = ({ filename, contentUrl, content }) => {
  const { i18n, t } = useTranslation();
  const [records, setRecords] = useState<MoleculeRecord[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<ScientificPreviewError | null>(null);
  const [query, setQuery] = useState('');

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    setRecords([]);
    void loadMoleculeSource({ content, contentUrl, signal: controller.signal })
      .then((source) => parseSmilesRecords(source, (index) => t('preview.scientific.molecule.defaultName', { index })))
      .then(async (sources) => {
        const rendered = await Promise.all(
          sources.slice(0, MAX_MOLECULES).map(async (source): Promise<MoleculeRecord | null> => {
            if (controller.signal.aborted) return null;
            try {
              const svg = await renderMoleculeSvg(source.smiles, 280, 190);
              return !controller.signal.aborted && svg
                ? { id: source.id, smiles: source.smiles, name: source.name, svg }
                : null;
            } catch (reason) {
              if (reason instanceof RDKitRenderError && reason.code === 'invalid_input') return null;
              throw reason;
            }
          })
        );
        if (!controller.signal.aborted) {
          setRecords(rendered.filter((record): record is MoleculeRecord => record !== null));
        }
      })
      .catch((reason: unknown) => {
        if (controller.signal.aborted) return;
        const failure = reason instanceof RDKitRenderError ? new ScientificPreviewError('initialize-failed') : reason;
        logScientificPreviewError('[SynonBiomedMoleculeViewer] Failed to load molecules', failure, 'parse-failed');
        setError(resolveScientificPreviewError(failure, 'parse-failed'));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [content, contentUrl, i18n.resolvedLanguage, t]);

  const visibleRecords = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    if (!normalized) return records;
    return records.filter(
      (record) => record.name.toLowerCase().includes(normalized) || record.smiles.toLowerCase().includes(normalized)
    );
  }, [query, records]);

  return (
    <section className='size-full min-h-500px flex flex-col bg-1' aria-label={t('preview.scientific.molecule.preview')}>
      <header className='shrink-0 border-b border-solid border-[var(--color-border-2)] px-14px py-11px'>
        <div className='flex min-w-0 flex-wrap items-center gap-10px'>
          <div className='min-w-0 flex-1'>
            <h2 className='m-0 truncate text-14px font-[600] text-t-primary'>{filename}</h2>
            <div className='mt-2px text-11px text-t-tertiary'>
              {t('preview.scientific.molecule.summary', {
                count: records.length,
                formattedCount: records.length.toLocaleString(i18n.resolvedLanguage),
              })}
            </div>
          </div>
          <Input.Search
            value={query}
            allowClear
            aria-label={t('preview.scientific.molecule.search')}
            placeholder={t('preview.scientific.molecule.searchPlaceholder')}
            className='w-220px max-w-full'
            onChange={setQuery}
          />
        </div>
      </header>
      <div className='min-h-0 flex-1 overflow-y-auto p-14px'>
        {loading ? (
          <div className='h-280px flex-center'>
            <Spin tip={t('preview.scientific.molecule.rendering')} />
          </div>
        ) : error ? (
          <div className='h-280px flex-center px-20px text-13px text-danger-6'>
            {t(scientificPreviewErrorKey(error), {
              kind: t('preview.scientific.molecule.kind'),
              ...error.details,
            })}
          </div>
        ) : visibleRecords.length === 0 ? (
          <Empty
            description={t(
              records.length ? 'preview.scientific.molecule.noMatches' : 'preview.scientific.molecule.noDrawableSmiles'
            )}
          />
        ) : (
          <div className='grid grid-cols-1 gap-12px sm:grid-cols-2 xl:grid-cols-3'>
            {visibleRecords.map((record) => (
              <article key={record.id} className='min-w-0 overflow-hidden border border-solid border-arco-2 bg-1'>
                <img
                  src={`data:image/svg+xml;charset=utf-8,${encodeURIComponent(record.svg)}`}
                  alt={t('preview.scientific.molecule.structureAlt', { name: record.name })}
                  className='h-190px w-full bg-white object-contain p-8px'
                />
                <div className='border-t border-solid border-arco-2 bg-fill-0 px-10px py-9px'>
                  <div className='truncate text-13px font-[600] text-t-primary'>{record.name}</div>
                  <div className='mt-4px break-all font-mono text-10px leading-16px text-t-tertiary'>
                    {record.smiles}
                  </div>
                </div>
              </article>
            ))}
          </div>
        )}
      </div>
    </section>
  );
};

export function parseSmilesRecords(
  source: string,
  fallbackName: (index: number) => string = (index) => `Molecule ${index}`
): Array<Omit<MoleculeRecord, 'svg'>> {
  return source
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line && !line.startsWith('#'))
    .flatMap((line, index) => {
      const [smiles, ...nameParts] = line.split(/\s+/);
      if (!smiles) return [];
      return [{ id: `${index}-${smiles}`, smiles, name: nameParts.join(' ') || fallbackName(index + 1) }];
    });
}

async function loadMoleculeSource({
  content,
  contentUrl,
  signal,
}: {
  content?: string;
  contentUrl?: string;
  signal: AbortSignal;
}): Promise<string> {
  if (content !== undefined) {
    assertMoleculeSize(new Blob([content]).size);
    return content;
  }
  if (!contentUrl) throw new ScientificPreviewError('missing-content');
  const response = await fetch(contentUrl, { headers: { accept: 'text/plain' }, signal });
  if (!response.ok) throw new ScientificPreviewError('request-failed', { status: response.status });
  const declaredLength = Number(response.headers.get('content-length'));
  if (Number.isFinite(declaredLength)) assertMoleculeSize(declaredLength);
  const source = await response.text();
  assertMoleculeSize(new Blob([source]).size);
  return source;
}

function assertMoleculeSize(size: number): void {
  if (size > MAX_MOLECULE_BYTES) throw new ScientificPreviewError('too-large', { limit: '2 MB' });
}

export default SynonBiomedMoleculeViewer;
