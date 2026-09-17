/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { resolveSynonBiomedArtifactPreviewPlan } from '@/renderer/services/synonBiomedArtifactPreview';
import { ArrowLeft, FileText, FileZip, FolderOpen, Refresh } from '@icon-park/react';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { usePreviewContext } from '../../context/PreviewContext';
import {
  ArchivePreviewError,
  fetchArchiveListing,
  type ArchiveEntry,
  type ArchiveListing,
  type ArchivePreviewFailure,
} from './archivePreviewClient';

type ArchiveViewerProps = {
  filename: string;
  contentUrl?: string;
};

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / 1024 ** index;
  return `${value >= 10 || index === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[index]}`;
}

function archiveEndpoint(contentUrl: string, containers: string[], entry?: string): string {
  const source = new URL(contentUrl, window.location.origin);
  source.hash = '';
  source.search = '';
  source.pathname = `${source.pathname.replace(/\/$/, '')}/archive${entry ? '/content' : ''}`;
  containers.forEach((container) => source.searchParams.append('container', container));
  if (entry) source.searchParams.set('entry', entry);
  return source.href;
}

const ArchiveViewer: React.FC<ArchiveViewerProps> = ({ filename, contentUrl }) => {
  const { t } = useTranslation();
  const { openPreview } = usePreviewContext();
  const [containers, setContainers] = useState<string[]>([]);
  const [directory, setDirectory] = useState('');
  const [listing, setListing] = useState<ArchiveListing | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<ArchivePreviewFailure | ''>('');
  const requestSequence = useRef(0);

  const loadListing = useCallback(
    async (signal?: AbortSignal) => {
      if (!contentUrl) {
        setError('remoteOnly');
        return;
      }
      const requestId = ++requestSequence.current;
      setLoading(true);
      setError('');
      try {
        const nextListing = await fetchArchiveListing(archiveEndpoint(contentUrl, containers), { signal });
        if (requestId !== requestSequence.current) return;
        setListing(nextListing);
      } catch (loadError) {
        if (loadError instanceof DOMException && loadError.name === 'AbortError') return;
        if (requestId !== requestSequence.current) return;
        setListing(null);
        setError(loadError instanceof ArchivePreviewError ? loadError.reason : 'unavailable');
      } finally {
        if (requestId === requestSequence.current) setLoading(false);
      }
    },
    [containers, contentUrl]
  );

  useEffect(() => {
    const controller = new AbortController();
    void loadListing(controller.signal);
    return () => controller.abort();
  }, [loadListing]);

  const visibleEntries = useMemo(() => {
    if (!listing) return [];
    const prefix = directory ? `${directory}/` : '';
    return listing.entries
      .filter((entry) => entry.path.startsWith(prefix))
      .filter((entry) => !entry.path.slice(prefix.length).includes('/'))
      .toSorted(
        (left, right) => Number(right.directory) - Number(left.directory) || left.name.localeCompare(right.name)
      );
  }, [directory, listing]);

  const openEntry = useCallback(
    async (entry: ArchiveEntry) => {
      if (!contentUrl) return;
      if (entry.directory) {
        setDirectory(entry.path);
        return;
      }
      if (entry.archive) {
        setContainers((current) => [...current, entry.path]);
        setDirectory('');
        return;
      }
      const entryUrl = archiveEndpoint(contentUrl, containers, entry.path);
      const plan = resolveSynonBiomedArtifactPreviewPlan({ filename: entry.name });
      const metadata = {
        file_name: entry.name,
        title: entry.name,
        contentUrl: entryUrl,
        editable: false,
        language: plan.language,
      };
      if (plan.fetchText) {
        const response = await fetch(entryUrl, { credentials: 'same-origin' });
        if (!response.ok) {
          setError('entryFailed');
          return;
        }
        openPreview(await response.text(), plan.type, metadata, { presentation: 'board' });
        return;
      }
      openPreview(plan.type === 'image' || plan.type === 'pdf' ? entryUrl : '', plan.type, metadata, {
        presentation: 'board',
      });
    },
    [containers, contentUrl, openPreview]
  );

  const goBack = () => {
    if (directory) {
      const parent = directory.split('/').slice(0, -1).join('/');
      setDirectory(parent);
      return;
    }
    if (containers.length > 0) setContainers((current) => current.slice(0, -1));
  };

  const currentName = containers.at(-1)?.split('/').at(-1) ?? listing?.filename ?? filename;
  const canGoBack = Boolean(directory || containers.length);

  return (
    <div className='preview-content-scroll flex-1 overflow-auto bg-1 px-18px py-16px' data-testid='archive-viewer'>
      <div className='mx-auto max-w-920px'>
        <div className='mb-14px flex items-center gap-10px border-b border-border-base pb-12px'>
          <button
            type='button'
            className='h-30px w-30px shrink-0 rounded-7px border-0 bg-transparent text-t-secondary hover:bg-fill-2 disabled:opacity-30'
            disabled={!canGoBack}
            aria-label={t('preview.archive.back')}
            onClick={goBack}
          >
            <ArrowLeft size={15} />
          </button>
          <FileZip size={18} className='shrink-0 text-t-secondary' />
          <div className='min-w-0 flex-1'>
            <div className='truncate text-14px font-600 text-t-primary'>{currentName}</div>
            <div className='truncate text-12px text-t-tertiary'>
              {[filename, ...containers, directory].filter(Boolean).join(' / ')}
            </div>
          </div>
          <button
            type='button'
            className='h-30px w-30px rounded-7px border-0 bg-transparent text-t-secondary hover:bg-fill-2'
            aria-label={t('preview.archive.refresh')}
            onClick={() => void loadListing()}
          >
            <Refresh size={15} />
          </button>
        </div>

        {loading ? (
          <div className='py-56px text-center text-13px text-t-secondary'>{t('preview.archive.loading')}</div>
        ) : error ? (
          <div className='rounded-8px bg-fill-1 px-14px py-12px text-13px text-t-secondary'>
            {t(`preview.archive.${error}`)}
          </div>
        ) : visibleEntries.length === 0 ? (
          <div className='py-56px text-center text-13px text-t-tertiary'>{t('preview.archive.empty')}</div>
        ) : (
          <div className='overflow-hidden rounded-10px border border-border-base bg-1'>
            {visibleEntries.map((entry) => (
              <button
                key={entry.path}
                type='button'
                className='flex w-full items-center gap-11px border-0 border-b border-border-base bg-transparent px-13px py-10px text-left last:border-b-0 hover:bg-fill-1'
                onClick={() => void openEntry(entry)}
              >
                {entry.directory ? (
                  <FolderOpen size={17} className='shrink-0 text-t-secondary' />
                ) : entry.archive ? (
                  <FileZip size={17} className='shrink-0 text-t-secondary' />
                ) : (
                  <FileText size={17} className='shrink-0 text-t-tertiary' />
                )}
                <span className='min-w-0 flex-1 truncate text-13px text-t-primary'>{entry.name}</span>
                {!entry.directory && (
                  <span className='shrink-0 text-12px text-t-tertiary'>{formatBytes(entry.size)}</span>
                )}
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  );
};

export default ArchiveViewer;
