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
  let presented = content.replace(STANDALONE_ARTIFACT_REFERENCE, (match, leading: string, rawVersionId: string) => {
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
