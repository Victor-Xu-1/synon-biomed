/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { resolveSynonBiomedArtifactPreviewPlan } from '@/renderer/services/synonBiomedArtifactPreview';
import { useNearViewport } from '@/renderer/hooks/ui/useNearViewport';
import { Spin } from '@arco-design/web-react';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

type ArtifactThumbnailPreviewProps = {
  filename: string;
  contentUrl?: string;
  contentType?: string | null;
  previewKind?: string | null;
  sizeBytes?: number;
  className?: string;
  style?: React.CSSProperties;
  imageFit?: 'contain' | 'cover';
};

// The content endpoint serves the original immutable artifact. It is correct
// for an explicit preview, but too expensive for an automatic large-file
// thumbnail because the browser would download and decode the whole source.
export const MAX_AUTOMATIC_THUMBNAIL_SOURCE_BYTES = 2 * 1024 * 1024;

type TableThumbnailData = {
  headers: string[];
  rows: string[][];
  columnTypes: Array<'number' | 'text'>;
  totalRows: number;
  totalColumns: number;
};

export type ArtifactThumbnailVisualKind =
  | 'code'
  | 'document'
  | 'image'
  | 'spreadsheet'
  | 'heatmap'
  | 'scatter'
  | 'network';

export function extractTableThumbnail(content: string): TableThumbnailData | null {
  const rows = content
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
  if (rows.length === 0) return null;
  const delimiter = rows[0].includes('\t') ? '\t' : rows[0].includes(',') ? ',' : '';
  if (!delimiter) return null;
  const parsed = rows.slice(0, 10).map((line) => splitDelimitedThumbnailLine(line, delimiter));
  const totalColumns = parsed.reduce((maximum, row) => Math.max(maximum, row.length), 0);
  const headers = parsed[0]
    .map((cell) => cell.slice(0, 18))
    .filter(Boolean)
    .slice(0, 5);
  if (headers.length === 0) return null;
  const bodyRows = parsed.slice(1);
  const columnTypes = headers.map((_, columnIndex) =>
    bodyRows.some((row) => {
      const value = row[columnIndex]?.trim();
      return Boolean(value) && !/^-?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i.test(value);
    })
      ? 'text'
      : 'number'
  );
  return {
    headers,
    rows: bodyRows.slice(0, 5).map((row) => row.slice(0, headers.length)),
    columnTypes,
    totalRows: Math.max(0, rows.length - 1),
    totalColumns,
  };
}

export function resolveArtifactThumbnailVisualKind(
  type: string,
  filename: string,
  contentType?: string | null
): ArtifactThumbnailVisualKind {
  const extension = filename.toLowerCase().split('.').at(-1) ?? '';
  const normalizedType = contentType?.toLowerCase() ?? '';
  if (
    type === 'image' ||
    normalizedType.startsWith('image/') ||
    ['png', 'jpg', 'jpeg', 'gif', 'webp', 'avif', 'svg', 'bmp'].includes(extension)
  ) {
    return 'image';
  }
  if (
    type === 'table' ||
    normalizedType.includes('spreadsheet') ||
    ['csv', 'tsv', 'xlsx', 'xls', 'ods'].includes(extension)
  ) {
    return 'spreadsheet';
  }
  if (normalizedType.includes('graphml') || ['graphml', 'gml'].includes(extension)) return 'network';
  if (
    type === 'code' ||
    normalizedType.includes('json') ||
    normalizedType.includes('javascript') ||
    normalizedType.includes('python') ||
    ['json', 'jsonl', 'ndjson', 'py', 'js', 'ts', 'r', 'sh', 'yaml', 'yml'].includes(extension)
  ) {
    return 'code';
  }
  if (type === 'sequence' || type === 'msa' || ['fasta', 'fa', 'aln', 'clustal', 'sto'].includes(extension)) {
    return 'heatmap';
  }
  if (type === 'hdf5' || type === 'genome' || ['h5', 'hdf5', 'h5ad'].includes(extension)) return 'scatter';
  return 'document';
}

const ArtifactThumbnailPreview: React.FC<ArtifactThumbnailPreviewProps> = ({
  filename,
  contentUrl,
  contentType,
  previewKind,
  sizeBytes,
  className = '',
  style,
  imageFit = 'contain',
}) => {
  const { ref, isNearViewport } = useNearViewport<HTMLDivElement>();
  const plan = useMemo(
    () => resolveSynonBiomedArtifactPreviewPlan({ filename, contentType, previewKind }),
    [contentType, filename, previewKind]
  );
  const semanticKind = useMemo(
    () => resolveArtifactThumbnailVisualKind(plan.type, filename, contentType),
    [contentType, filename, plan.type]
  );
  const [imageFailed, setImageFailed] = useState(false);

  useEffect(() => {
    setImageFailed(false);
  }, [contentUrl, filename, plan.type]);

  const canLoadAutomaticSource =
    typeof sizeBytes === 'number' &&
    Number.isFinite(sizeBytes) &&
    sizeBytes >= 0 &&
    sizeBytes <= MAX_AUTOMATIC_THUMBNAIL_SOURCE_BYTES;
  // Keep off-screen artifact rows cheap. The virtual list deliberately
  // overscans rows for smooth scrolling, but those rows must not construct
  // semantic previews, images, SVGs, or text renderers until they approach the
  // viewport. The fixed placeholder preserves measurement without creating a
  // second loading/fetch authority.
  if (!isNearViewport) {
    return (
      <div ref={ref} aria-hidden='true' className={`size-full overflow-hidden bg-white ${className}`} style={style} />
    );
  }

  return (
    <div ref={ref} className={`size-full overflow-hidden bg-white ${className}`} style={style}>
      {plan.type === 'image' && contentUrl && canLoadAutomaticSource && !imageFailed ? (
        <img
          data-testid='artifact-image-thumbnail'
          src={contentUrl}
          alt={filename}
          width={152}
          height={92}
          loading='lazy'
          decoding='async'
          fetchPriority='low'
          onError={() => setImageFailed(true)}
          className={`size-full ${imageFit === 'cover' ? 'object-cover' : 'object-contain'}`}
        />
      ) : plan.type === 'structure' && contentUrl && canLoadAutomaticSource ? (
        <StructureArtifactThumbnail filename={filename} contentUrl={contentUrl} />
      ) : (
        <SemanticArtifactThumbnail type={plan.type} filename={filename} visualKind={semanticKind} />
      )}
    </div>
  );
};

const StructureArtifactThumbnail: React.FC<{ filename: string; contentUrl: string }> = ({ filename, contentUrl }) => {
  const { t } = useTranslation();
  const [preview, setPreview] = useState<{ status: 'loading' | 'ready' | 'error'; imageUrl?: string }>({
    status: 'loading',
  });

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    setPreview({ status: 'loading' });
    void import('@/renderer/pages/conversation/Preview/components/viewers/renderStructureThumbnail')
      .then(({ renderStructureThumbnail }) =>
        renderStructureThumbnail({ contentUrl, filename, signal: controller.signal })
      )
      .then((imageUrl) => {
        if (active) setPreview({ status: 'ready', imageUrl });
      })
      .catch((error: unknown) => {
        if (!active || controller.signal.aborted) return;
        console.warn('[ArtifactThumbnailPreview] Mol* structure thumbnail failed', {
          errorName: error instanceof Error ? error.name : typeof error,
          filename,
        });
        setPreview({ status: 'error' });
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [contentUrl, filename]);

  return (
    <div data-testid='artifact-structure-thumbnail' className='size-full overflow-hidden bg-white'>
      {preview.status === 'ready' && preview.imageUrl ? (
        <img
          src={preview.imageUrl}
          alt={filename}
          width={152}
          height={92}
          decoding='async'
          className='size-full object-cover'
          data-testid='artifact-structure-thumbnail-image'
        />
      ) : preview.status === 'loading' ? (
        <div
          role='status'
          aria-label={t('preview.scientific.structure.thumbnail.loading')}
          className='size-full flex-center bg-fill-1 text-t-tertiary'
        >
          <Spin size={14} />
        </div>
      ) : (
        <div
          role='img'
          aria-label={filename}
          data-testid='artifact-structure-thumbnail-error'
          className='size-full flex-center bg-fill-1 px-8px text-center text-10px text-t-tertiary'
        >
          {t('preview.scientific.structure.thumbnail.failed')}
        </div>
      )}
    </div>
  );
};

function splitDelimitedThumbnailLine(line: string, delimiter: string): string[] {
  const cells: string[] = [];
  let value = '';
  let quoted = false;
  for (let index = 0; index < line.length; index += 1) {
    const character = line[index];
    if (character === '"') {
      if (quoted && line[index + 1] === '"') {
        value += character;
        index += 1;
      } else {
        quoted = !quoted;
      }
    } else if (character === delimiter && !quoted) {
      cells.push(value.trim());
      value = '';
    } else {
      value += character;
    }
  }
  cells.push(value.trim());
  return cells;
}

const SemanticArtifactThumbnail: React.FC<{
  type: string;
  filename: string;
  visualKind?: ArtifactThumbnailVisualKind;
}> = ({ type, filename, visualKind = resolveArtifactThumbnailVisualKind(type, filename) }) => {
  const label = type === 'word' ? 'DOC' : type === 'ppt' ? 'SLIDE' : type === 'hdf5' ? 'MATRIX' : type.toUpperCase();
  return (
    <div
      role='img'
      aria-label={filename}
      className='size-full box-border flex-center overflow-hidden bg-white p-10px text-t-tertiary'
    >
      <SemanticThumbnailGraphic kind={visualKind} seed={filename} />
      <span className='sr-only'>{label}</span>
    </div>
  );
};

const SemanticThumbnailGraphic: React.FC<{ kind: ArtifactThumbnailVisualKind; seed: string }> = ({ kind, seed }) => {
  const random = useMemo(() => createSeededRandom(seed), [seed]);
  const colors = ['#d946ef', '#3b82f6', '#059669', '#d97706', '#6b7280'];
  if (kind === 'code') {
    const bars = Array.from({ length: 9 }, (unusedRow, rowIndex) => {
      const indent = rowIndex === 0 || rowIndex === 8 ? 0 : rowIndex > 1 && rowIndex < 7 ? 6 : 3;
      const segments = 1 + Math.floor(random() * 3);
      let x = 5 + indent;
      return Array.from({ length: segments }, (unusedSegment, segmentIndex) => {
        const width = 5 + random() * 8;
        const bar = (
          <rect
            key={`${rowIndex}-${segmentIndex}`}
            x={x}
            y={5 + rowIndex * 4.2}
            width={width}
            height='2.4'
            rx='0.7'
            fill={colors[Math.floor(random() * colors.length)]}
            opacity='0.72'
          />
        );
        x += width + 2;
        return bar;
      });
    }).flat();
    return (
      <svg viewBox='0 0 48 48' className='h-full max-h-86px w-auto max-w-full rd-5px shadow-sm' aria-hidden='true'>
        <rect width='48' height='48' rx='4' fill='#1e1e2e' />
        {bars}
      </svg>
    );
  }

  if (kind === 'spreadsheet') {
    return (
      <svg viewBox='0 0 96 64' className='size-full max-h-86px max-w-120px' aria-hidden='true'>
        <rect width='96' height='64' rx='5' fill='#f5f6f8' />
        {Array.from({ length: 6 }, (unusedRow, row) =>
          Array.from({ length: 5 }, (unusedColumn, column) => (
            <rect
              key={`${row}-${column}`}
              x={5 + column * 18}
              y={5 + row * 9}
              width='15'
              height='6'
              rx='1'
              fill={row === 0 ? '#d8dbe2' : random() > 0.72 ? '#3b82f6' : '#e7e9ee'}
            />
          ))
        )}
      </svg>
    );
  }

  if (kind === 'image') {
    return (
      <svg viewBox='0 0 96 64' className='size-full max-h-86px max-w-120px' aria-hidden='true'>
        <rect width='96' height='64' rx='5' fill='#f5f6f8' />
        <circle cx='28' cy='22' r='7' fill='#f59e0b' opacity='0.85' />
        <path d='M8 52 31 31l13 12 10-9 34 18H8Z' fill='#3b82f6' opacity='0.78' />
        <path d='m8 52 18-14 11 9 12-11 39 16H8Z' fill='#059669' opacity='0.65' />
      </svg>
    );
  }

  if (kind === 'network') {
    const nodes = Array.from({ length: 8 }, (_, index) => ({
      x: 12 + random() * 72,
      y: 10 + random() * 44,
      color: colors[index % colors.length],
    }));
    return (
      <svg viewBox='0 0 96 64' className='size-full max-h-86px max-w-120px' aria-hidden='true'>
        <rect width='96' height='64' rx='5' fill='#f5f6f8' />
        {nodes.slice(1).map((node, index) => (
          <line key={index} x1={nodes[index].x} y1={nodes[index].y} x2={node.x} y2={node.y} stroke='#c4c8d1' />
        ))}
        {nodes.map((node, index) => (
          <circle key={index} cx={node.x} cy={node.y} r='4' fill={node.color} stroke='white' strokeWidth='1.5' />
        ))}
      </svg>
    );
  }

  if (kind === 'scatter' || kind === 'heatmap') {
    return (
      <svg viewBox='0 0 96 64' className='size-full max-h-86px max-w-120px' aria-hidden='true'>
        <rect width='96' height='64' rx='5' fill='#f5f6f8' />
        {kind === 'scatter'
          ? Array.from({ length: 42 }, (_, index) => (
              <circle
                key={index}
                cx={8 + random() * 80}
                cy={7 + random() * 50}
                r='2'
                fill={colors[Math.floor(random() * 4)]}
                opacity='0.72'
              />
            ))
          : Array.from({ length: 40 }, (_, index) => (
              <rect
                key={index}
                x={5 + (index % 10) * 8.6}
                y={6 + Math.floor(index / 10) * 13}
                width='7.2'
                height='11'
                rx='1'
                fill={`hsl(${205 + random() * 55} 65% ${40 + random() * 42}%)`}
              />
            ))}
      </svg>
    );
  }

  return (
    <svg viewBox='0 0 96 64' className='size-full max-h-86px max-w-120px' aria-hidden='true'>
      <rect width='96' height='64' rx='5' fill='#f5f6f8' />
      <rect x='8' y='8' width='58' height='5' rx='2' fill='#697180' />
      {Array.from({ length: 7 }, (_, index) => (
        <rect key={index} x='8' y={18 + index * 6} width={38 + random() * 43} height='3' rx='1.5' fill='#b9bec8' />
      ))}
    </svg>
  );
};

function createSeededRandom(seed: string): () => number {
  let state = 2166136261;
  for (let index = 0; index < seed.length; index += 1) {
    state ^= seed.charCodeAt(index);
    state = Math.imul(state, 16777619);
  }
  return () => {
    state += 0x6d2b79f5;
    let value = state;
    value = Math.imul(value ^ (value >>> 15), value | 1);
    value ^= value + Math.imul(value ^ (value >>> 7), value | 61);
    return ((value ^ (value >>> 14)) >>> 0) / 4294967296;
  };
}

export default ArtifactThumbnailPreview;
