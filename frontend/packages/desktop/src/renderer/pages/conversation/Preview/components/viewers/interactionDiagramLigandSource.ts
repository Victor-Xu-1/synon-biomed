/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

const validSmiles = (value: string): boolean =>
  value.length > 0 && value.length <= 4096 && /^[A-Za-z0-9@+\-[\]()=#$%.:/\\*]+$/.test(value);
const MAX_SMILES_FILE_BYTES = 1024 * 1024;
const ARTIFACT_VERSION_URL = /^\/api\/artifacts\/([^/?#]+)\/versions\/([^/?#]+)$/;

export function findCandidateSmiles(content: string, candidateId: string): string | null {
  const expected = candidateId.trim().toLowerCase();
  if (!expected) return null;
  for (const rawLine of content.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith('#')) continue;
    const fields = line
      .split(line.includes('\t') ? /\t+/ : line.includes(',') ? /\s*,\s*/ : /\s+/)
      .map((field) => field.trim().replace(/^['"]|['"]$/g, ''))
      .filter(Boolean);
    const idIndex = fields.findIndex((field) => field.toLowerCase() === expected);
    if (idIndex < 0) continue;
    const smiles = fields
      .filter((_, index) => index !== idIndex)
      .filter((field) => !/^(smiles|candidate(?:_id)?|id|name)$/i.test(field))
      .find(validSmiles);
    if (smiles) return smiles;
  }
  return null;
}

export async function loadInteractionDiagramCandidateSmiles(
  contentUrl: string,
  candidateId: string,
  fetchImpl: typeof fetch = fetch
): Promise<string | null> {
  const normalizedUrl = contentUrl.trim();
  if (!normalizedUrl) return null;
  if (!normalizedUrl.startsWith('/api/artifacts/')) throw new Error('INTERACTION_SMILES_URL_INVALID');
  const response = await fetchImpl(contentUrl, {
    headers: { accept: 'text/plain, chemical/x-daylight-smiles, text/*;q=0.9' },
  });
  if (!response.ok) throw new Error(`INTERACTION_SMILES_REQUEST_FAILED:${response.status}`);
  const contentLength = Number(response.headers.get('content-length') ?? 0);
  if (Number.isFinite(contentLength) && contentLength > MAX_SMILES_FILE_BYTES) {
    throw new Error('INTERACTION_SMILES_FILE_TOO_LARGE');
  }
  const content = await response.text();
  if (new TextEncoder().encode(content).byteLength > MAX_SMILES_FILE_BYTES) {
    throw new Error('INTERACTION_SMILES_FILE_TOO_LARGE');
  }
  return findCandidateSmiles(content, candidateId);
}

export async function loadInteractionDiagramCandidateSmilesFromSources(
  contentUrls: readonly (string | null | undefined)[],
  candidateId: string,
  fetchImpl: typeof fetch = fetch
): Promise<string | null> {
  let lastError: unknown;
  const attempted = new Set<string>();
  for (const value of contentUrls) {
    const contentUrl = value?.trim();
    if (!contentUrl || attempted.has(contentUrl)) continue;
    attempted.add(contentUrl);
    try {
      // Same-turn provenance is authoritative; consult the current artifact
      // only when that immutable version does not contain this candidate.
      // eslint-disable-next-line no-await-in-loop
      const smiles = await loadInteractionDiagramCandidateSmiles(contentUrl, candidateId, fetchImpl);
      if (smiles) return smiles;
    } catch (error) {
      lastError = error;
    }
  }
  if (lastError) throw lastError;
  return null;
}

export async function resolveCurrentInteractionDiagramCompanionUrl(
  contentUrl: string,
  fetchImpl: typeof fetch = fetch
): Promise<string | null> {
  const match = ARTIFACT_VERSION_URL.exec(contentUrl.trim());
  if (!match) return null;
  const artifactId = decodeURIComponent(match[1]);
  const response = await fetchImpl(`/api/artifacts/${encodeURIComponent(artifactId)}/versions`, {
    headers: { accept: 'application/json' },
  });
  if (!response.ok) throw new Error(`INTERACTION_SMILES_VERSIONS_REQUEST_FAILED:${response.status}`);
  const payload: unknown = await response.json();
  if (!Array.isArray(payload)) throw new Error('INTERACTION_SMILES_VERSIONS_INVALID');
  const current = payload
    .map((value) => {
      if (!value || typeof value !== 'object') return null;
      const record = value as Record<string, unknown>;
      const versionId = typeof record.version_id === 'string' ? record.version_id.trim() : '';
      const versionNumber = typeof record.version_number === 'number' ? record.version_number : Number.NaN;
      return versionId && Number.isSafeInteger(versionNumber) && versionNumber > 0
        ? { versionId, versionNumber }
        : null;
    })
    .filter((value): value is { versionId: string; versionNumber: number } => value !== null)
    .toSorted((left, right) => right.versionNumber - left.versionNumber)[0];
  return current ? `/api/artifacts/versions/${encodeURIComponent(current.versionId)}` : null;
}

export async function loadCurrentInteractionDiagramCandidateSmiles(
  contentUrl: string,
  candidateId: string,
  fetchImpl: typeof fetch = fetch
): Promise<string | null> {
  let currentUrl: string | null = null;
  let resolutionError: unknown;
  try {
    currentUrl = await resolveCurrentInteractionDiagramCompanionUrl(contentUrl, fetchImpl);
  } catch (error) {
    resolutionError = error;
  }
  try {
    const smiles = await loadInteractionDiagramCandidateSmilesFromSources(
      [currentUrl, contentUrl],
      candidateId,
      fetchImpl
    );
    if (smiles) return smiles;
  } catch (error) {
    resolutionError = error;
  }
  if (resolutionError) throw resolutionError;
  return null;
}
