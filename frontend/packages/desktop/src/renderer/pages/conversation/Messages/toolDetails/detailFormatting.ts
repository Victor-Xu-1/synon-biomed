import { isPublicDetailField, normalizePublicDetailKey, publicDetailFieldLabel } from './detailFieldPolicy';
import { toolPublicDetailText, type ToolPublicDetailTextKey } from '@/renderer/services/i18n/toolPublicDetailLocale';
import { formatPublicTaskPath, sanitizePublicTaskText } from '../components/toolPublicDetailBlocks';
import { isPublicNarrativeSafe } from '../components/toolStepSummaryModel';
import type { ToolPublicDetailCollection, ToolPublicDetailRow } from './detailTypes';

export function safeShortContext(value: unknown): string | null {
  if (typeof value !== 'string') return null;
  const normalized = value.trim().replace(/\s+/gu, ' ');
  if (!normalized || normalized.length > 120) return null;
  return sanitizePublicTaskText(normalized);
}

export function publicEnvironmentContext(value: unknown): string | null {
  const context = safeShortContext(value);
  if (!context) return null;
  if (
    /(?:^|[-_])(?:acceptance|internal|private|runtime|workspace)(?:[-_]|$)|^(?:swr-[0-9a-f]{16,}|synon(?:[-_]|$))|[0-9a-f]{8}-[0-9a-f-]{20,}/iu.test(
      context
    )
  ) {
    return null;
  }
  return context;
}

export function collectionCount(value: unknown): number | null {
  if (Array.isArray(value)) return value.length;
  if (typeof value === 'number' && Number.isFinite(value) && value >= 0) return Math.trunc(value);
  return null;
}

export function parseStructured(value: string | undefined): Record<string, unknown> | null {
  if (!value) return null;
  try {
    const parsed = JSON.parse(value) as unknown;
    return isRecord(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

export function dedupeRows(rows: ToolPublicDetailRow[]): ToolPublicDetailRow[] {
  const seen = new Set<string>();
  return rows.filter((row) => {
    const key = `${row.label}\u0000${row.value}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

export function dedupeCollections(collections: ToolPublicDetailCollection[]): ToolPublicDetailCollection[] {
  const seen = new Set<string>();
  return collections.filter((collection) => {
    const key = `${collection.label}\u0000${collection.items.join('\u0000')}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

export function publicModeLabel(value: string, chinese: boolean): string | null {
  const labels: Record<string, ToolPublicDetailTextKey> = {
    list: 'modeList',
    search: 'modeSearch',
    inspect: 'modeInspect',
    compare: 'modeCompare',
    retrieve: 'modeRetrieve',
    create: 'modeCreate',
    ensure: 'modeEnsure',
    install: 'modeInstall',
    check: 'modeCheck',
    read: 'modeRead',
    append: 'modeAppend',
    replace: 'modeReplace',
    set: 'modeSet',
    ro: 'modeReadOnly',
    rw: 'modeReadWrite',
  };
  const label = labels[value.toLowerCase()];
  return label ? toolPublicDetailText(chinese, label) : null;
}

export function publicLanguageLabel(value: string): string | null {
  const normalized = value.toLowerCase();
  if (normalized === 'python') return 'Python';
  if (normalized === 'r') return 'R';
  if (normalized === 'javascript' || normalized === 'typescript') return 'JavaScript';
  return null;
}

export function publicMemoryScopeLabel(value: unknown, chinese: boolean): string | null {
  if (typeof value !== 'string') return null;
  const normalized = value.trim();
  if (!normalized) return null;
  if (normalized === 'profile') return toolPublicDetailText(chinese, 'memoryProfile');
  if (normalized === 'frame' || normalized.startsWith('frame:')) return toolPublicDetailText(chinese, 'memoryTask');
  if (normalized === 'project' || normalized.startsWith('project:'))
    return toolPublicDetailText(chinese, 'memoryProject');
  if (normalized.startsWith('artifact:')) return toolPublicDetailText(chinese, 'memoryArtifact');
  if (normalized.startsWith('category:')) return normalized.slice('category:'.length) || null;
  return null;
}

export function formatPublicCollectionItem(value: unknown, chinese: boolean, collectionKey: string): string | null {
  if (isRecord(value)) {
    if (collectionKey === 'environments') return formatEnvironmentRecord(value, chinese);
    return formatPublicRecord(value, chinese);
  }
  if (typeof value !== 'string') return null;
  const normalized = value.trim().replace(/\s+/g, ' ');
  if (!normalized || normalized.length > 180) return null;
  if (isPublicNarrativeSafe(normalized)) return normalized;
  const taskPath = formatPublicTaskPath(normalized);
  if (taskPath && isSafeRelativeTaskPath(taskPath)) return taskPath;
  if (/^[A-Za-z0-9][A-Za-z0-9.+~-]{0,79}(?:[<>=!~]{1,2}[A-Za-z0-9.*+-]{1,40})?$/u.test(normalized)) {
    return normalized;
  }
  return null;
}

export function isSafeRelativeTaskPath(value: string): boolean {
  const sanitized = sanitizePublicTaskText(value);
  if (!sanitized || sanitized !== value) return false;
  if (value.startsWith('/') || /^[A-Za-z]:\\|^\\\\/u.test(value)) return false;
  if (/(?:^|[\\/])\.\.(?:[\\/]|$)/u.test(value)) return false;
  if (/\b(?:api[_-]?key|authorization|cookie|password|private[_-]?key|secret|token)\b/iu.test(value)) return false;
  return /^(?:\.\/?|\.\\)?[A-Za-z0-9_.+@~-]+(?:[\\/][A-Za-z0-9_.+@ -]+)*$/u.test(value);
}

export function formatPublicRecord(value: Record<string, unknown>, chinese: boolean): string | null {
  const artifact = formatArtifactRecord(value);
  if (artifact) return artifact;
  if (isEnvironmentRecord(value)) return formatEnvironmentRecord(value, chinese);
  const parts: string[] = [];
  for (const [key, candidate] of Object.entries(value)) {
    if (/^id$/iu.test(key)) {
      const identity = formatPublicRecordIdentity(candidate);
      if (identity) parts.push(`${publicRecordKeyLabel(key, chinese)}: ${identity}`);
      continue;
    }
    if (!isPublicRecordKey(key)) continue;
    const formatted =
      key === 'abstract' && typeof candidate === 'string'
        ? truncate(sanitizePublicTaskText(candidate) ?? '', 240)
        : formatPublicRecordValue(candidate, chinese);
    if (!formatted) continue;
    parts.push(`${publicRecordKeyLabel(key, chinese)}: ${formatted}`);
    if (parts.length >= 6) break;
  }
  if (parts.length === 0) return null;
  return truncate(parts.join(' · '), 360);
}

function formatPublicRecordIdentity(value: unknown): string | null {
  if (typeof value !== 'string') return null;
  const normalized = value.trim();
  if (
    !normalized ||
    normalized.length > 80 ||
    !/^[\p{L}\p{N}][\p{L}\p{N}._:+/-]*$/u.test(normalized) ||
    /^(?:mem_|proj(?:ect)?_|artifact_|frame_|swr-|synon[-_])|^[0-9a-f]{8}-[0-9a-f-]{20,}$/iu.test(normalized)
  ) {
    return null;
  }
  return normalized;
}

function isEnvironmentRecord(value: Record<string, unknown>): boolean {
  return (
    'python_version' in value ||
    'r_version' in value ||
    'missing' in value ||
    (typeof value.name === 'string' && (typeof value.language === 'string' || Array.isArray(value.packages)))
  );
}

function formatArtifactRecord(value: Record<string, unknown>): string | null {
  const filename = typeof value.filename === 'string' ? formatPublicTaskPath(value.filename.trim()) : null;
  if (!filename) return null;
  const details = [filename];
  if (typeof value.content_type === 'string' && /^[\w.+-]+\/[\w.+-]+$/u.test(value.content_type)) {
    details.push(value.content_type);
  }
  if (typeof value.size_bytes === 'number' && Number.isFinite(value.size_bytes) && value.size_bytes >= 0) {
    details.push(formatBytes(value.size_bytes));
  }
  return details.join(' · ');
}

export function formatEnvironmentRecord(value: Record<string, unknown>, chinese: boolean): string | null {
  const rawName = typeof value.name === 'string' ? value.name.trim() : '';
  const name = publicEnvironmentContext(value.name);
  if (rawName && !name) return null;
  const pythonVersion = typeof value.python_version === 'string' ? `Python ${value.python_version}` : null;
  const rVersion = typeof value.r_version === 'string' ? `R ${value.r_version}` : null;
  const language = publicLanguageLabel(typeof value.language === 'string' ? value.language : '');
  const runtime = pythonVersion ?? rVersion ?? language;
  const missing = Array.isArray(value.missing)
    ? value.missing.filter((item): item is string => typeof item === 'string' && item.trim().length > 0)
    : [];
  if (!name && !runtime && missing.length === 0) return null;
  const details = name ? [name] : [];
  if (runtime && runtime.toLowerCase() !== name?.toLowerCase()) details.push(runtime);
  if (missing.length > 0) {
    details.push(`${toolPublicDetailText(chinese, 'missingComponents')}: ${missing.slice(0, 6).join(', ')}`);
  }
  return details.join(' · ');
}

export function isPublicRecordKey(key: string): boolean {
  return isPublicDetailField(key, 'summary');
}

export function formatBytes(value: number): string {
  if (value < 1024) return `${Math.round(value)} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(value < 10 * 1024 ? 1 : 0)} KB`;
  if (value < 1024 * 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(value < 10 * 1024 * 1024 ? 1 : 0)} MB`;
  return `${(value / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

export function formatPublicRecordValue(value: unknown, chinese = false): string | null {
  if (typeof value === 'number' && Number.isFinite(value)) return String(value);
  if (typeof value === 'boolean') return toolPublicDetailText(chinese, value ? 'yes' : 'no');
  if (typeof value !== 'string') return null;
  const normalized = value.trim().replace(/\s+/gu, ' ');
  if (!normalized || normalized.length > 260) return null;
  if (/(?:^|\s)(?:\/[^\s]+){2,}|[A-Za-z]:\\|\\\\wsl|\b(?:sk-|api[_-]?key|bearer\s+)[^\s]+/iu.test(normalized)) {
    return null;
  }
  if (
    /\b(?:harness|system prompt|runtime policy|tool call|workdir|workspace)\b|提示词|系统提示|运行时策略|工具调用|工作目录/iu.test(
      normalized
    )
  ) {
    return null;
  }
  return normalized;
}

export const publicRecordKeyLabel = publicDetailFieldLabel;

export const normalizePublicRecordKey = normalizePublicDetailKey;

export function publicEnvironmentOutputLabel(value: string, chinese: boolean): string | null {
  const labels: Record<string, ToolPublicDetailTextKey> = {
    preflight: 'environmentPreflight',
    create: 'environmentCreate',
    list: 'environmentList',
    install: 'environmentInstall',
    reuse: 'environmentReuse',
  };
  const key = labels[value.trim().toLowerCase()];
  return key ? toolPublicDetailText(chinese, key) : formatPublicRecordValue(value, chinese);
}

export function formatEnvironmentBlocker(value: unknown, chinese: boolean): string | null {
  if (typeof value !== 'string') return null;
  const labels: Record<string, ToolPublicDetailTextKey> = {
    insufficient_disk: 'blockerDisk',
    insufficient_memory: 'blockerMemory',
    missing_packages: 'blockerPackages',
    unsupported_python: 'blockerPython',
    unsupported_r: 'blockerR',
  };
  const key = labels[value.trim().toLowerCase()];
  return key ? toolPublicDetailText(chinese, key) : formatPublicRecordValue(value, chinese);
}

export function formatDuration(seconds: number, chinese: boolean): string {
  if (seconds < 1) return toolPublicDetailText(chinese, 'underOneSecond');
  if (seconds < 60) {
    const count = seconds.toFixed(chinese && seconds >= 10 ? 0 : 1);
    return toolPublicDetailText(chinese, 'seconds', { count });
  }
  const minutes = Math.floor(seconds / 60);
  const remainder = Math.round(seconds % 60);
  return toolPublicDetailText(chinese, 'minutesSeconds', { minutes, seconds: remainder });
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value));
}

export function truncate(value: string, maxCharacters: number): string {
  const characters = Array.from(value);
  return characters.length <= maxCharacters ? value : `${characters.slice(0, maxCharacters - 1).join('')}…`;
}
