/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ScientificPreviewError } from './scientificPreviewError';

const MAX_STRUCTURE_SOURCE_BYTES = 64 * 1024 * 1024;

export function resolveStructureFormat(filename: string): string {
  const normalized = normalizedStructureFilename(filename);
  if (normalized.endsWith('.cif') || normalized.endsWith('.mmcif') || normalized.endsWith('.bcif')) return 'mmcif';
  if (normalized.endsWith('.pdbqt')) return 'pdbqt';
  if (normalized.endsWith('.pqr')) return 'pqr';
  if (normalized.endsWith('.sdf')) return 'sdf';
  if (normalized.endsWith('.mol')) return 'mol';
  if (normalized.endsWith('.mol2')) return 'mol2';
  if (normalized.endsWith('.xyz')) return 'xyz';
  if (normalized.endsWith('.gro')) return 'gro';
  if (isVolumeFilename(normalized)) return 'cube';
  return 'pdb';
}

export function resolveStructureDisplayFormat(filename: string): string {
  const normalized = normalizedStructureFilename(filename);
  if (normalized.endsWith('.cif') || normalized.endsWith('.mmcif')) return 'CIF';
  if (normalized.endsWith('.bcif')) return 'BCIF';
  if (normalized.endsWith('.ent')) return 'PDB';
  if (normalized.endsWith('.cub')) return 'CUBE';
  const extension = normalized.split('.').at(-1);
  return extension && extension !== normalized ? extension.toUpperCase() : 'PDB';
}

export async function loadStructureContent({
  contentUrl,
  content,
  filename,
  format,
  signal,
}: {
  contentUrl?: string;
  content?: string;
  filename: string;
  format: string;
  signal: AbortSignal;
}): Promise<string | ArrayBuffer> {
  if (contentUrl) {
    const binary = format === 'mmcif' && normalizedStructureFilename(filename).endsWith('.bcif');
    const response = await fetch(contentUrl, {
      signal,
      headers: { accept: binary ? 'application/octet-stream, chemical/*' : 'text/plain, chemical/*' },
    });
    if (!response.ok) {
      throw new ScientificPreviewError('request-failed', { status: response.status });
    }
    const declaredLength = Number(response.headers.get('content-length'));
    if (Number.isFinite(declaredLength)) assertStructureSize(declaredLength);
    const source = binary ? await response.arrayBuffer() : await response.text();
    assertStructureSize(new Blob([source]).size);
    return source;
  }
  if (content != null) {
    assertStructureSize(new Blob([content]).size);
    return content;
  }
  throw new ScientificPreviewError('missing-content');
}

export function normalizedStructureFilename(value: string): string {
  return value.split(/[?#]/, 1)[0].toLowerCase();
}

function isVolumeFilename(value: string): boolean {
  return /\.(cube|cub)$/i.test(value);
}

function assertStructureSize(size: number): void {
  if (size > MAX_STRUCTURE_SOURCE_BYTES) {
    throw new ScientificPreviewError('too-large', { limit: '64 MB' });
  }
}
