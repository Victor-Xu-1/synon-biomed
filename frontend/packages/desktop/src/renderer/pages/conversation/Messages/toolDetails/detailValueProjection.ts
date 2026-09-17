import { isPublicDetailField, normalizePublicDetailKey, publicDetailFieldLabel } from './detailFieldPolicy';
import { sanitizePublicTaskText } from '../components/toolPublicDetailBlocks';
import type { ToolPublicDetailKind } from '../toolActivityPresentationRegistry';
import type { ToolDetailValueNode } from './detailTypes';
import { toolPublicDetailText } from '@/renderer/services/i18n/toolPublicDetailLocale';
import { publicReceiptScalar } from './retrievalReceiptPresentation';
import { formatBytes } from './detailFormatting';

const BLOCK_VALUE_KEY = /^(?:code|command|content|markdown|old_string|new_string|old_text|stderr|stdout|text)$/iu;
const OPAQUE_PRIVATE_VALUE =
  /^(?:mem_|proj(?:ect)?_|artifact_|frame_|swr-|synon[-_])|(?:^|[-_])(?:acceptance|internal|private|runtime)(?:[-_]|$)/iu;

export type DetailValueProjectionOptions = {
  chinese: boolean;
  detailKind: ToolPublicDetailKind;
  label: string;
  source: 'input' | 'output';
  /** Remote pages have no separate block renderer, so their public text fields stay visible. */
  includeBlockValues?: boolean;
  byteLimit?: boolean;
};

/**
 * Projects the complete public portion of a structured tool payload. The
 * projection keeps array order and duplicate records, filters private fields
 * at the boundary, and leaves DOM paging to DetailValueTree.
 */
export function projectToolDetailValue(
  value: unknown,
  options: DetailValueProjectionOptions
): ToolDetailValueNode | null {
  return projectValue(value, options.label, '$', options, 0);
}

function projectValue(
  value: unknown,
  label: string,
  path: string,
  options: DetailValueProjectionOptions,
  depth: number
): ToolDetailValueNode | null {
  if (depth > 64) {
    return {
      kind: 'scalar',
      path,
      label,
      value: toolPublicDetailText(options.chinese, 'maxDepth'),
    };
  }
  if (Array.isArray(value)) {
    const children = value
      .map((candidate, index) =>
        projectValue(
          candidate,
          toolPublicDetailText(options.chinese, 'entryIndex', {
            index: index + 1,
          }),
          `${path}/${index}`,
          options,
          depth + 1
        )
      )
      .filter((candidate): candidate is ToolDetailValueNode => candidate !== null);
    if (children.length === 0) return null;
    return { kind: 'array', path, label, children, total: children.length };
  }
  if (isRecord(value)) {
    const children: ToolDetailValueNode[] = [];
    for (const [key, candidate] of Object.entries(value)) {
      if (!isPublicDetailKey(key, candidate, options)) continue;
      const normalizedKey = normalizePublicDetailKey(key);
      const isUrl = /^(?:url|source_url|requested_url)$/u.test(normalizedKey);
      const isBytes =
        normalizedKey === 'bytes_read' ||
        (options.byteLimit && options.source === 'input' && normalizedKey === 'limit');
      const formatted =
        isBytes && typeof candidate === 'number' && Number.isSafeInteger(candidate) && candidate >= 0
          ? formatBytes(candidate)
          : publicReceiptScalar(normalizedKey, candidate);
      if (isUrl && !formatted) continue;
      const child = projectValue(
        formatted,
        isBytes && normalizedKey === 'limit'
          ? toolPublicDetailText(options.chinese, 'readLimit')
          : publicDetailKeyLabel(key, options.chinese),
        `${path}/${escapePathSegment(key)}`,
        options,
        depth + 1
      );
      if (child) children.push(child);
    }
    if (children.length === 0) return null;
    return { kind: 'object', path, label, children, total: children.length };
  }
  const scalar = formatPublicTreeScalar(value, options.chinese);
  return scalar === null ? null : { kind: 'scalar', path, label, value: scalar };
}

export function isPublicDetailKey(key: string, value: unknown, options: DetailValueProjectionOptions): boolean {
  const normalized = normalizePublicDetailKey(key);
  if (!isPublicDetailField(normalized, 'tree')) return false;
  if (!options.includeBlockValues && BLOCK_VALUE_KEY.test(normalized) && typeof value === 'string') return false;
  if (normalized === 'channels') return false;
  if (
    options.source === 'input' &&
    options.detailKind === 'environment' &&
    (normalized === 'create' || normalized === 'force') &&
    typeof value === 'boolean'
  ) {
    return false;
  }
  if (normalized === 'id') {
    return typeof value === 'string' && isPublicRecordIdentity(value);
  }
  if (
    (normalized === 'name' || normalized === 'environment') &&
    typeof value === 'string' &&
    OPAQUE_PRIVATE_VALUE.test(value.trim())
  ) {
    return false;
  }
  return true;
}

function isPublicRecordIdentity(value: string): boolean {
  const normalized = value.trim();
  return (
    normalized.length > 0 &&
    normalized.length <= 80 &&
    /^[\p{L}\p{N}][\p{L}\p{N}._:+/-]*$/u.test(normalized) &&
    !OPAQUE_PRIVATE_VALUE.test(normalized)
  );
}

export function formatPublicTreeScalar(value: unknown, chinese = false): string | null {
  if (value === null) return 'null';
  if (typeof value === 'number' && Number.isFinite(value)) return String(value);
  if (typeof value === 'boolean') return toolPublicDetailText(chinese, value ? 'yes' : 'no');
  if (typeof value !== 'string') return null;
  if (OPAQUE_PRIVATE_VALUE.test(value.trim())) return null;
  const sanitized = sanitizePublicTaskText(value);
  return sanitized?.trim() || null;
}

export const publicDetailKeyLabel = publicDetailFieldLabel;

function escapePathSegment(value: string): string {
  return value.replace(/~/gu, '~0').replace(/\//gu, '~1');
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value));
}
