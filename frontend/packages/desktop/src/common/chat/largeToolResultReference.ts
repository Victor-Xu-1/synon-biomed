export type LargeToolResultReference = {
  contentUrl: string;
  sourceCount?: number;
};

/** Decode the immutable result reference used by both timeline and detail loading.
 * Only the canonical local artifact route is fetchable. A preview is navigation
 * metadata, never a replacement source collection or proof of source reading.
 */
export function parseLargeToolResultReference(output: string | undefined): LargeToolResultReference | null {
  const value = parseRecord(output);
  if (!value || value.truncated !== true) return null;
  const artifactId = value.artifact_id;
  const versionId = value.version_id;
  if (
    typeof artifactId !== 'string' ||
    !/^large-tool-result-[a-fA-F0-9]{32}$/u.test(artifactId) ||
    typeof versionId !== 'string' ||
    !/^ltr-[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$/u.test(versionId)
  )
    return null;
  const contentUrl = `/api/artifacts/${artifactId}/versions/${versionId}`;
  if (value.content_url !== contentUrl) return null;

  const preview = parseRecord(typeof value.preview === 'string' ? value.preview : undefined);
  const count = preview?.source_count;
  const sourceCount =
    value.outcome !== 'failed' &&
    preview?.view_format === 'search-results-display-lines' &&
    preview.source_version_id === versionId &&
    typeof count === 'number' &&
    Number.isSafeInteger(count) &&
    count >= 0
      ? count
      : undefined;
  return { contentUrl, ...(sourceCount === undefined ? {} : { sourceCount }) };
}

function parseRecord(raw: string | undefined): Record<string, unknown> | null {
  if (!raw) return null;
  try {
    const value: unknown = JSON.parse(raw);
    return value !== null && typeof value === 'object' && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : null;
  } catch {
    return null;
  }
}
