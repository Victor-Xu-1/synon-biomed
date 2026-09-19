/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { ArtifactReferenceWire } from '@/common/adapter/messageStreamProtocol';
import type { ConversationArtifactIndex } from '../artifacts';

const STANDALONE_ARTIFACT_REFERENCE = /(^|\n)[ \t]*\{\{artifact:([^{}\r\n]+)\}\}[ \t]*(?=\r?\n|$)/g;
const ARTIFACT_REFERENCE = /\{\{artifact:([^{}\r\n]+)\}\}/g;
const TRUNCATED_ARTIFACT_IMAGE = /!\[([^\]\r\n]+)\]\([ \t]*(?=\r?\n|$)/g;
const LEGACY_ARTIFACT_INLINE_CODE = /`(\/artifacts\/[^`/?#\s]+)`/g;
const LEGACY_ARTIFACT_MARKDOWN_LINK = /(\]\(\s*)\/artifacts\/([^)/?#\s]+)(\s*\))/g;
const VERSIONED_ARTIFACT_PREVIEW_INLINE_CODE = /(`)(\/#\/artifacts\/[^`/?#\s]+\?version=[^`&#\s]+)(`)/g;
const VERSIONED_ARTIFACT_PREVIEW_MARKDOWN_LINK =
  /(\]\(\s*)\/#\/artifacts\/([^?/#)\s]+)\?version=([^&#)\s]+)([^)]*)(\s*\))/g;
const IMAGE_EXTENSION = /\.(?:avif|bmp|gif|jpe?g|png|svg|webp)$/i;

type PresentedArtifactReference = {
  filename: string;
  versionId: string;
  isImage: boolean;
};

function availableReferenceFiles(
  references: readonly ArtifactReferenceWire[] | undefined,
  index: ConversationArtifactIndex
): Map<string, PresentedArtifactReference> {
  const files = new Map<string, PresentedArtifactReference>();
  for (const reference of references ?? []) {
    if (reference.availability && reference.availability !== 'available') continue;
    const versionId = reference.version_id.trim();
    if (!versionId) continue;
    const indexed = index.byVersionId.get(versionId);
    const filename = indexed?.filename.trim() || reference.filename?.trim() || '';
    if (!filename) continue;
    const contentType = indexed?.content_type || reference.content_type || '';
    files.set(versionId, {
      filename,
      versionId,
      isImage: contentType.startsWith('image/') || IMAGE_EXTENSION.test(filename),
    });
  }
  return files;
}

function referencedFilesByFilename(
  references: readonly ArtifactReferenceWire[] | undefined,
  index: ConversationArtifactIndex
) {
  const files = new Map<string, PresentedArtifactReference>();
  const ambiguous = new Set<string>();
  for (const file of availableReferenceFiles(references, index).values()) {
    const key = file.filename.trim().toLocaleLowerCase();
    if (!key || ambiguous.has(key)) continue;
    if (files.has(key)) {
      files.delete(key);
      ambiguous.add(key);
      continue;
    }
    files.set(key, file);
  }
  return files;
}

type LegacyArtifactResolution = {
  versionId: string;
  filename: string;
};

function legacyArtifactResolutions(
  references: readonly ArtifactReferenceWire[] | undefined,
  index: ConversationArtifactIndex
): Map<string, LegacyArtifactResolution | null> {
  const resolutions = new Map<string, LegacyArtifactResolution | null>();
  for (const reference of references ?? []) {
    if (reference.availability && reference.availability !== 'available') continue;
    const artifactId = reference.artifact_id.trim();
    const versionId = reference.version_id.trim();
    if (!artifactId || !versionId) continue;
    const indexed = index.byVersionId.get(versionId);
    const candidate = {
      versionId,
      filename: indexed?.filename.trim() || reference.filename?.trim() || artifactId,
    };
    const current = resolutions.get(artifactId);
    if (current === undefined) {
      resolutions.set(artifactId, candidate);
    } else if (current && current.versionId !== versionId) {
      // An artifact id without a version is ambiguous once more than one
      // version is present. Do not guess a link for historical prose.
      resolutions.set(artifactId, null);
    }
  }
  return resolutions;
}

function escapeMarkdownLabel(value: string): string {
  return value.replace(/[\\[\]]/g, '\\$&');
}

function decodeLegacyArtifactId(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

function findArtifactVersion(
  references: readonly ArtifactReferenceWire[] | undefined,
  rawArtifactId: string,
  rawVersionId: string
): ArtifactReferenceWire | null {
  const artifactId = decodeLegacyArtifactId(rawArtifactId);
  const versionId = decodeLegacyArtifactId(rawVersionId);
  return (
    (references ?? []).find(
      (reference) =>
        (!reference.availability || reference.availability === 'available') &&
        reference.artifact_id.trim() === artifactId &&
        reference.version_id.trim() === versionId
    ) ?? null
  );
}

function presentLegacyArtifactLinks(
  content: string,
  references: readonly ArtifactReferenceWire[] | undefined,
  index: ConversationArtifactIndex
): string {
  const resolutions = legacyArtifactResolutions(references, index);
  const resolve = (rawArtifactId: string): LegacyArtifactResolution | null => {
    const artifactId = decodeLegacyArtifactId(rawArtifactId);
    return resolutions.get(artifactId) ?? null;
  };
  let presented = content.replace(LEGACY_ARTIFACT_INLINE_CODE, (match, rawPath: string) => {
    const resolution = resolve(rawPath.slice('/artifacts/'.length));
    if (!resolution) return match;
    return `[${escapeMarkdownLabel(resolution.filename)}]({{artifact:${resolution.versionId}}})`;
  });
  presented = presented.replace(
    LEGACY_ARTIFACT_MARKDOWN_LINK,
    (match, prefix: string, rawArtifactId: string, suffix: string) => {
      const resolution = resolve(rawArtifactId);
      return resolution ? `${prefix}{{artifact:${resolution.versionId}}}${suffix}` : match;
    }
  );
  presented = presented.replace(
    VERSIONED_ARTIFACT_PREVIEW_MARKDOWN_LINK,
    (
      match: string,
      prefix: string,
      rawArtifactId: string,
      rawVersionId: string,
      _trailingQuery: string,
      suffix: string
    ) => {
      const reference = findArtifactVersion(references, rawArtifactId, rawVersionId);
      return reference ? `${prefix}{{artifact:${reference.version_id}}}${suffix}` : match;
    }
  );
  presented = presented.replace(
    VERSIONED_ARTIFACT_PREVIEW_INLINE_CODE,
    (match: string, _opening: string, rawURL: string, _closing: string) => {
      const parsed = /^\/#\/artifacts\/([^/?#\s]+)\?version=([^&#\s]+)$/.exec(rawURL);
      if (!parsed) return match;
      const reference = findArtifactVersion(references, parsed[1], parsed[2]);
      if (!reference) return match;
      const indexed = index.byVersionId.get(reference.version_id);
      const filename = indexed?.filename.trim() || reference.filename?.trim() || reference.artifact_id;
      return `[${escapeMarkdownLabel(filename)}]({{artifact:${reference.version_id}}})`;
    }
  );
  return presented;
}

/**
 * Presents immutable artifact references without creating a second artifact
 * authority. Structured message references select exact versions; this helper
 * only formats them for Markdown and repairs the bounded historical image
 * truncation emitted by the old completion normalizer.
 */
export function presentArtifactReferenceContent(
  content: string,
  references: readonly ArtifactReferenceWire[] | undefined,
  index: ConversationArtifactIndex,
  settled: boolean
): string {
  const filesByVersion = availableReferenceFiles(references, index);
  const byFilename = referencedFilesByFilename(references, index);
  let presented = presentLegacyArtifactLinks(content, references, index);
  presented = presented.replace(STANDALONE_ARTIFACT_REFERENCE, (match, leading: string, rawVersionId: string) => {
    const versionId = rawVersionId.trim();
    const file = filesByVersion.get(versionId);
    if (!file) return '';
    const reference = `{{artifact:${versionId}}}`;
    return `${leading}${file.isImage ? `![${file.filename}](${reference})` : `[${file.filename}](${reference})`}`;
  });
  presented = presented.replace(TRUNCATED_ARTIFACT_IMAGE, (_match, rawLabel: string) => {
    const label = rawLabel.trim();
    if (!settled) return '';
    const file = byFilename.get(label.toLocaleLowerCase());
    if (!file?.isImage) return label;
    return `![${file.filename}]({{artifact:${file.versionId}}})`;
  });
  presented = presented.replace(
    ARTIFACT_REFERENCE,
    (match: string, _rawVersionId: string, offset: number, source: string) =>
      source.slice(Math.max(0, offset - 2), offset) === '](' ? match : ''
  );
  return presented.replace(/\n{3,}/g, '\n\n').trimEnd();
}
