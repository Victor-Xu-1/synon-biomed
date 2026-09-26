/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

export const STRUCTURE_SCENE_MANIFEST_SCHEMA = 'synon.structure-scene.v1' as const;

const MAX_STRUCTURE_SCENE_MANIFEST_BYTES = 1024 * 1024;
const MAX_STRUCTURE_SCENE_MANIFEST_CANDIDATES = 16;

export type StructureSceneSource = {
  name: string;
  versionId: string;
  url: string;
};

export type StructureSceneSources = {
  sceneId: string;
  sources: readonly StructureSceneSource[];
};

type StructureSceneReference = {
  name: string;
  version_id: string;
};

type StructureSceneManifest = {
  schema: typeof STRUCTURE_SCENE_MANIFEST_SCHEMA;
  scene_id: string;
  mother_structure: StructureSceneReference;
  derived_structures: StructureSceneReference[];
};

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const readReference = (value: unknown): StructureSceneReference | null => {
  if (!isRecord(value)) return null;
  const name = typeof value.name === 'string' ? value.name.trim() : '';
  const versionId = typeof value.version_id === 'string' ? value.version_id.trim() : '';
  return name && versionId ? { name, version_id: versionId } : null;
};

export function parseStructureSceneManifest(value: unknown): StructureSceneManifest | null {
  if (!isRecord(value) || value.schema !== STRUCTURE_SCENE_MANIFEST_SCHEMA) return null;
  const sceneId = typeof value.scene_id === 'string' ? value.scene_id.trim() : '';
  const mother = readReference(value.mother_structure);
  const derived = Array.isArray(value.derived_structures) ? value.derived_structures.map(readReference) : [];
  if (!sceneId || !mother || derived.length === 0 || derived.some((reference) => reference === null)) return null;
  const references = [mother, ...derived] as StructureSceneReference[];
  const versionIds = new Set(references.map((reference) => reference.version_id));
  if (versionIds.size !== references.length) return null;
  return {
    schema: STRUCTURE_SCENE_MANIFEST_SCHEMA,
    scene_id: sceneId,
    mother_structure: mother,
    derived_structures: derived as StructureSceneReference[],
  };
}

export async function loadStructureSceneSources(
  companionArtifactUrls: Readonly<Record<string, string>> | undefined,
  signal: AbortSignal
): Promise<StructureSceneSources | null> {
  const candidates = Object.entries(companionArtifactUrls ?? {})
    .filter(([name, url]) => name.toLocaleLowerCase().endsWith('.json') && Boolean(url.trim()))
    .slice(0, MAX_STRUCTURE_SCENE_MANIFEST_CANDIDATES);
  for (const [, url] of candidates) {
    try {
      // Preserve artifact order so the first valid scene manifest is authoritative.
      // eslint-disable-next-line no-await-in-loop
      const response = await fetch(url, { signal, headers: { accept: 'application/json, text/plain' } });
      if (!response.ok) continue;
      const declaredLength = Number(response.headers.get('content-length'));
      if (Number.isFinite(declaredLength) && declaredLength > MAX_STRUCTURE_SCENE_MANIFEST_BYTES) continue;
      // The bounded, ordered read above intentionally avoids racing candidate manifests.
      // eslint-disable-next-line no-await-in-loop
      const text = await response.text();
      if (new Blob([text]).size > MAX_STRUCTURE_SCENE_MANIFEST_BYTES) continue;
      const manifest = parseStructureSceneManifest(JSON.parse(text) as unknown);
      if (!manifest) continue;
      const urlByName = new Map(
        Object.entries(companionArtifactUrls ?? {}).map(([name, value]) => [name.toLocaleLowerCase(), value])
      );
      const references = [manifest.mother_structure, ...manifest.derived_structures];
      const sources = references.map((reference) => ({
        name: reference.name,
        versionId: reference.version_id,
        url: urlByName.get(reference.name.toLocaleLowerCase()) ?? '',
      }));
      if (sources.some((source) => !source.url)) continue;
      return { sceneId: manifest.scene_id, sources };
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') throw error;
      // A companion JSON file may be an ordinary report. Keep the primary
      // structure preview usable and continue looking for the versioned scene.
    }
  }
  return null;
}
