/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

function decodeArtifactReference(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

export function getSynonBiomedRelativeArtifactFilename(rawHref: string): string | null {
  const href = rawHref.trim();
  if (!href || href.startsWith('/') || href.startsWith('#') || href.startsWith('//')) return null;
  if (/^[a-z][a-z0-9+.-]*:/i.test(href)) return null;

  const path = decodeArtifactReference(href.split(/[?#]/, 1)[0]).replace(/\\/g, '/');
  const segments = path.split('/').filter((segment) => segment && segment !== '.');
  if (segments.length === 0 || segments.some((segment) => segment === '..')) return null;
  const filename = segments.at(-1) ?? '';
  return filename.includes('.') ? filename : null;
}

export function getSynonBiomedArtifactReferenceId(rawHref: string): string | null {
  const decoded = decodeArtifactReference(rawHref.trim());
  const match = /^\{\{artifact:([^{}]+)\}\}$/.exec(decoded);
  return match?.[1]?.trim() || null;
}

export function getSynonBiomedArtifactImageFilename(rawSrc: string): string | null {
  const src = rawSrc.trim();
  if (!src || src.startsWith('data:') || /^https?:\/\//i.test(src)) return null;
  return getSynonBiomedRelativeArtifactFilename(src);
}

export type SynonBiomedCompanionArtifact = {
  name?: string;
  filename?: string;
  contentUrl?: string;
  content_url?: string;
  availability?: string;
  children?: readonly SynonBiomedCompanionArtifact[];
};

export function createSynonBiomedCompanionArtifactUrls(
  artifacts: readonly SynonBiomedCompanionArtifact[]
): Readonly<Record<string, string>> {
  const urls = new Map<string, string>();
  const ambiguous = new Set<string>();
  const visit = (artifact: SynonBiomedCompanionArtifact): void => {
    const filename = (artifact.filename ?? artifact.name ?? '').trim();
    const contentUrl = (artifact.content_url ?? artifact.contentUrl ?? '').trim();
    if (filename && contentUrl && artifact.availability !== 'deleted' && artifact.availability !== 'missing') {
      const key = filename.toLocaleLowerCase();
      const current = urls.get(key);
      if (current && current !== contentUrl) {
        ambiguous.add(key);
        urls.delete(key);
      } else if (!ambiguous.has(key)) {
        urls.set(key, contentUrl);
      }
    }
    for (const child of artifact.children ?? []) visit(child);
  };
  for (const artifact of artifacts) visit(artifact);
  return Object.fromEntries(urls);
}
