import type { FileOrFolderItem } from '@/renderer/utils/file/fileTypes';

export type ComposerReferenceTrigger = '@' | '#';

export type ActiveComposerReferenceQuery = {
  trigger: ComposerReferenceTrigger;
  start: number;
  end: number;
  query: string;
  token: string;
};

type ComposerReferenceBase = {
  key: string;
  label: string;
  detail: string;
  projectId: string | null;
  projectName: string | null;
  isCurrentProject: boolean;
};

export type WorkspaceComposerReference = ComposerReferenceBase & {
  kind: 'workspace';
  item: FileOrFolderItem;
};

export type ArtifactComposerReference = ComposerReferenceBase & {
  kind: 'artifact';
  artifactId: string;
  versionId?: string;
};

export type SessionComposerReference = ComposerReferenceBase & {
  kind: 'session';
  frameId: string;
};

export type ComposerReferenceItem = WorkspaceComposerReference | ArtifactComposerReference | SessionComposerReference;

const REFERENCE_BOUNDARY_RE = /[\s,;!?()[\]{}]/;
const COMPLETE_REFERENCE_RE = /(?:@|#)\[[^\]\n]+\]\([^\s)]+\)/g;
const DEFAULT_RESULT_LIMIT = 12;

function isEscaped(value: string, index: number): boolean {
  let slashCount = 0;
  for (let cursor = index - 1; cursor >= 0 && value[cursor] === '\\'; cursor -= 1) slashCount += 1;
  return slashCount % 2 === 1;
}

function normalize(value: string | null | undefined): string {
  return typeof value === 'string' ? value.trim().toLocaleLowerCase() : '';
}

function referencePriority(item: ComposerReferenceItem): number {
  if (item.isCurrentProject) return 0;
  if (item.kind === 'workspace') return 1;
  return 2;
}

function matchScore(item: ComposerReferenceItem, query: string): number {
  if (!query) return 0;
  const label = normalize(item.label);
  const detail = normalize(item.detail);
  const project = normalize(item.projectName);
  if (label === query) return 500;
  if (label.replace(/\.[^.]+$/, '') === query) return 450;
  if (label.startsWith(query)) return 400;
  if (label.includes(query)) return 300;
  if (project.startsWith(query)) return 200;
  if (detail.includes(query) || project.includes(query)) return 100;
  return -1;
}

function escapeReferenceLabel(value: string): string {
  return value.replace(/\\/g, '\\\\').replace(/\[/g, '\\[').replace(/\]/g, '\\]');
}

function requireReferenceTarget(value: string, field: string): string {
  const normalized = value.trim();
  if (!normalized || /[\s()[\]]/.test(normalized)) {
    throw new Error(`Invalid composer reference ${field}`);
  }
  return normalized;
}

export function getActiveComposerReferenceQuery(
  value: string,
  caretPosition: number,
  trigger: ComposerReferenceTrigger
): ActiveComposerReferenceQuery | null {
  if (!value) return null;
  const caret = Math.max(0, Math.min(caretPosition, value.length));
  let triggerIndex = -1;

  for (let index = caret - 1; index >= 0; index -= 1) {
    const char = value[index];
    if (char === trigger && !isEscaped(value, index)) {
      const previous = index > 0 ? value[index - 1] : '';
      if (!previous || REFERENCE_BOUNDARY_RE.test(previous)) {
        triggerIndex = index;
        break;
      }
    }
    if (REFERENCE_BOUNDARY_RE.test(char) && !isEscaped(value, index)) return null;
  }

  if (triggerIndex < 0 || value[triggerIndex + 1] === '[') return null;
  let end = value.length;
  for (let index = triggerIndex + 1; index < value.length; index += 1) {
    if (REFERENCE_BOUNDARY_RE.test(value[index]) && !isEscaped(value, index)) {
      end = index;
      break;
    }
  }
  if (caret > end) return null;

  return {
    trigger,
    start: triggerIndex,
    end,
    query: value.slice(triggerIndex + 1, end).replace(/\\(.)/g, '$1'),
    token: value.slice(triggerIndex, end),
  };
}

export function getCompletedComposerReferenceRanges(value: string): Array<{ start: number; end: number }> {
  return Array.from(value.matchAll(COMPLETE_REFERENCE_RE), (match) => ({
    start: match.index ?? 0,
    end: (match.index ?? 0) + match[0].length,
  }));
}

export function createWorkspaceComposerReferences(items: FileOrFolderItem[]): WorkspaceComposerReference[] {
  const references: WorkspaceComposerReference[] = [];
  for (const item of items) {
    const path = item.relativePath || item.path;
    if (!path) continue;
    references.push({
      kind: 'workspace',
      key: `workspace:${item.path}`,
      label: item.name || path.split(/[\\/]/).pop() || path,
      detail: path,
      projectId: null,
      projectName: null,
      isCurrentProject: false,
      item,
    });
  }
  return references;
}

export function filterComposerReferences(
  items: ComposerReferenceItem[],
  query: string,
  limit = DEFAULT_RESULT_LIMIT
): ComposerReferenceItem[] {
  const normalizedQuery = normalize(query);
  return items
    .map((item, index) => ({ item, index, score: matchScore(item, normalizedQuery) }))
    .filter(({ score }) => score >= 0)
    .toSorted((left, right) => {
      if (left.score !== right.score) return right.score - left.score;
      const priority = referencePriority(left.item) - referencePriority(right.item);
      if (priority !== 0) return priority;
      return left.index - right.index;
    })
    .slice(0, limit)
    .map(({ item }) => item);
}

export function serializeComposerReference(item: ArtifactComposerReference | SessionComposerReference): string {
  const label = escapeReferenceLabel(item.label.trim());
  if (!label) throw new Error('Invalid composer reference label');
  if (item.kind === 'artifact') {
    const artifactId = requireReferenceTarget(item.artifactId, 'artifactId');
    const target = item.versionId ? `${artifactId}#${requireReferenceTarget(item.versionId, 'versionId')}` : artifactId;
    return `@[${label}](${target})`;
  }
  return `#[${label}](${requireReferenceTarget(item.frameId, 'frameId')})`;
}

export function insertSerializedComposerReference(
  value: string,
  query: ActiveComposerReferenceQuery,
  reference: string
): { value: string; caret: number } {
  const suffix = value.slice(query.end);
  const separator = suffix === '' || /^\s/.test(suffix) ? '' : ' ';
  const insertion = `${reference}${separator}`;
  return {
    value: value.slice(0, query.start) + insertion + suffix,
    caret: query.start + insertion.length,
  };
}
