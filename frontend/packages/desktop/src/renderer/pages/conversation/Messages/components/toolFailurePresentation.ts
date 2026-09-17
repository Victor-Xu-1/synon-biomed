import { redactErrorText, safeErrorCode } from '@/common/utils/errorRedaction';

const MAX_SCAN_CHARS = 64 * 1024;
const MAX_SUMMARY_CHARS = 180;
const MAX_DETAIL_CHARS = 16 * 1024;
const MAX_VISITED_VALUES = 96;
const MAX_DEPTH = 5;
const ANSI_COLOR_SEQUENCE = new RegExp(`${String.fromCharCode(0x1b)}\\[[0-9;]*m`, 'g');
const STRUCTURAL_JSON_CHARACTERS = new Set(['{', '}', '[', ']', ',']);

const FIELD_PRIORITY: Record<string, number> = {
  message: 100,
  error_message: 98,
  errormessage: 98,
  error: 96,
  preview: 94,
  detail: 92,
  reason: 90,
  stderr: 84,
  cause: 82,
  result: 28,
  response: 24,
  data: 20,
  payload: 18,
};

const SENSITIVE_FIELD = /(?:api[_-]?key|authorization|cookie|credential|password|secret|session[_-]?token|token)/i;
const UUID_PATTERN = /\b[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b/gi;
const UNIX_LOCAL_PATH_PATTERN = /\/(?:home|tmp|var|opt|etc|mnt)\/(?:[^\s'"`:){\]}]|:(?!\/\/))+/g;
const WINDOWS_LOCAL_PATH_PATTERN = /(?:[A-Za-z]:\\|\\\\wsl(?:\.localhost)?\\)[^\s'"`:){\]}]+/g;

interface Candidate {
  score: number;
  order: number;
  text: string;
}

interface QueueItem {
  depth: number;
  field: string;
  priority: number;
  value: unknown;
}

export interface ToolFailurePresentation {
  errorCode: string | null;
  exitCode: number | null;
  httpStatus: number | null;
  summary: string | null;
}

export function buildToolFailurePresentation(value: unknown): ToolFailurePresentation {
  const candidates: Candidate[] = [];
  const queue: QueueItem[] = [{ value, field: '', depth: 0, priority: 0 }];
  const visited = new WeakSet<object>();
  let errorCode: string | null = null;
  let exitCode: number | null = null;
  let httpStatus: number | null = null;
  let order = 0;
  let visitedValues = 0;

  while (queue.length > 0 && visitedValues < MAX_VISITED_VALUES) {
    const current = queue.shift();
    if (!current) break;
    visitedValues += 1;
    const field = normalizeField(current.field);

    if (typeof current.value === 'string') {
      if (SENSITIVE_FIELD.test(field)) continue;
      const text = current.value.slice(0, MAX_SCAN_CHARS);
      const parsed = parseStructuredText(text);
      if (parsed !== null && current.depth < MAX_DEPTH) {
        queue.unshift({
          value: parsed,
          field: current.field,
          depth: current.depth + 1,
          priority: current.priority + 8,
        });
        continue;
      }
      const truncatedEnvelopeCode = structuredEnvelopeErrorCode(text);
      if (truncatedEnvelopeCode) {
        errorCode ??= truncatedEnvelopeCode;
        candidates.push({
          text: truncatedEnvelopeCode,
          order: order++,
          score: current.priority + 64,
        });
        continue;
      }
      const meaningful = meaningfulErrorLine(text);
      if (!meaningful) continue;
      candidates.push({
        text: meaningful,
        order: order++,
        score: current.priority + fieldPriority(field) + lineQuality(meaningful),
      });
      if (!errorCode && isErrorCodeField(field)) {
        errorCode = safeErrorCode(meaningful) ?? null;
      }
      continue;
    }

    if (typeof current.value === 'number' && Number.isFinite(current.value)) {
      const numeric = Math.trunc(current.value);
      if (isExitCodeField(field)) exitCode ??= numeric;
      if (isHTTPStatusField(field) && numeric >= 400 && numeric <= 599) httpStatus ??= numeric;
      continue;
    }

    if (!current.value || typeof current.value !== 'object' || current.depth >= MAX_DEPTH) continue;
    if (visited.has(current.value)) continue;
    visited.add(current.value);

    const entries = safeEntries(current.value)
      .filter(([key]) => !SENSITIVE_FIELD.test(key))
      .toSorted(([left], [right]) => fieldPriority(normalizeField(right)) - fieldPriority(normalizeField(left)));
    for (const [key, nested] of entries.slice(0, 40)) {
      const normalizedKey = normalizeField(key);
      if (!errorCode && isErrorCodeField(normalizedKey) && typeof nested === 'string' && safeErrorCode(nested)) {
        errorCode = nested;
      }
      if (typeof nested === 'number' && Number.isFinite(nested)) {
        const numeric = Math.trunc(nested);
        if (isExitCodeField(normalizedKey)) exitCode ??= numeric;
        if (isHTTPStatusField(normalizedKey) && numeric >= 400 && numeric <= 599) httpStatus ??= numeric;
      }
      queue.push({
        value: nested,
        field: key,
        depth: current.depth + 1,
        priority: current.priority + Math.max(0, Math.floor(fieldPriority(normalizedKey) / 8)),
      });
    }
  }

  candidates.sort((left, right) => right.score - left.score || left.order - right.order);
  const summary = candidates.length > 0 ? sanitizeSummary(candidates[0].text) : null;
  return {
    summary: summary || null,
    errorCode: errorCode ? sanitizeErrorCode(errorCode) : null,
    exitCode,
    httpStatus: httpStatus ?? inferHTTPStatus(summary),
  };
}

function structuredEnvelopeErrorCode(value: string): string | null {
  const trimmed = value.trimStart();
  if (!(trimmed.startsWith('{') || trimmed.startsWith('['))) return null;
  const match = /["'](?:error_code|errorCode|code)["']\s*:\s*["']([A-Za-z][A-Za-z0-9._-]{1,95})["']/i.exec(trimmed);
  return match ? (safeErrorCode(match[1]) ?? null) : null;
}

export function summarizeToolFailure(value: unknown): string | null {
  return buildToolFailurePresentation(value).summary;
}

export function sanitizeToolFailureContext(value: string): string | null {
  const meaningful = meaningfulErrorLine(value.slice(0, MAX_SCAN_CHARS));
  const summary = meaningful ? sanitizeSummary(meaningful) : '';
  return summary || null;
}

export function sanitizeToolFailureDetail(value: unknown): string | null {
  const sanitizeValue = (item: unknown, depth: number): unknown => {
    if (typeof item === 'string') return sanitizeFailureText(item);
    if (item === null || typeof item === 'number' || typeof item === 'boolean') return item;
    if (!item || typeof item !== 'object' || depth >= MAX_DEPTH) return '[TRUNCATED]';
    if (Array.isArray(item)) return item.slice(0, 40).map((nested) => sanitizeValue(nested, depth + 1));
    const result: Record<string, unknown> = {};
    for (const [key, nested] of safeEntries(item).slice(0, 40)) {
      result[key] = SENSITIVE_FIELD.test(key) ? '[REDACTED]' : sanitizeValue(nested, depth + 1);
    }
    return result;
  };

  if (typeof value === 'string') {
    const parsed = parseStructuredText(value.slice(0, MAX_SCAN_CHARS));
    if (parsed !== null) return JSON.stringify(sanitizeValue(parsed, 0), null, 2).slice(0, MAX_DETAIL_CHARS);
    return sanitizeFailureText(value);
  }
  try {
    return JSON.stringify(sanitizeValue(value, 0), null, 2).slice(0, MAX_DETAIL_CHARS);
  } catch {
    return null;
  }
}

function sanitizeFailureText(value: string): string {
  return redactErrorText(value.slice(0, MAX_DETAIL_CHARS))
    .replace(WINDOWS_LOCAL_PATH_PATTERN, '[local path]')
    .replace(UNIX_LOCAL_PATH_PATTERN, '[local path]')
    .replace(UUID_PATTERN, '[internal id]')
    .replace(/\btool\s+[`'"]?[A-Za-z][A-Za-z0-9_.:-]*[`'"]?\s+is\s+not\s+allowed\b/gi, 'tool operation is not allowed');
}

function meaningfulErrorLine(value: string): string | null {
  const lines = value
    .replace(ANSI_COLOR_SEQUENCE, '')
    .replace(/\r/g, '')
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean);
  if (lines.length === 0) return null;

  const nonStackLines = lines.filter((line) => !isStackFrame(line));
  const source = nonStackLines.length > 0 ? nonStackLines : lines;
  const exception = source.findLast((line) =>
    /(?:^|\b)[A-Za-z_][A-Za-z0-9_.]*(?:Error|Exception|Failure)\s*:/i.test(line)
  );
  if (exception) return exception;

  const explicitError = source.find((line) =>
    /(?:\b(?:failed|failure|invalid|refused|unavailable|error)\b|\bHTTP\s*[45]\d\d\b|\bstatus\s*[45]\d\d\b)/i.test(line)
  );
  if (explicitError) return explicitError;

  return (
    source.find((line) => Array.from(line).some((character) => !STRUCTURAL_JSON_CHARACTERS.has(character))) ?? null
  );
}

function isStackFrame(line: string): boolean {
  return (
    /^Traceback\b/i.test(line) ||
    /^During handling of the above exception/i.test(line) ||
    /^The above exception was the direct cause/i.test(line) ||
    /^File\s+["'].+["'],\s+line\s+\d+/i.test(line) ||
    /^at\s+(?:async\s+)?[^\s]+\s*\(.+\)$/i.test(line) ||
    /^\^+$/.test(line) ||
    /^\.{3}\s+\d+\s+frames?\s+hidden/i.test(line)
  );
}

function lineQuality(line: string): number {
  let score = 0;
  if (/(?:Error|Exception|Failure)\s*:/i.test(line)) score += 42;
  if (/\b(?:HTTP\s*)?[45]\d\d\b/i.test(line)) score += 18;
  if (/\b(?:invalid|refused|unavailable|failed)\b/i.test(line)) score += 12;
  if (/tool result reported failure|unknown error|something went wrong/i.test(line)) score -= 32;
  if (/^Traceback\b/i.test(line)) score -= 80;
  return score;
}

function sanitizeSummary(value: string): string {
  let text = redactErrorText(value)
    .replace(WINDOWS_LOCAL_PATH_PATTERN, '[local path]')
    .replace(UNIX_LOCAL_PATH_PATTERN, '[local path]')
    .replace(UUID_PATTERN, '[internal id]')
    .replace(/\btool\s+[`'"]?[A-Za-z][A-Za-z0-9_.:-]*[`'"]?\s+is\s+not\s+allowed\b/gi, 'tool operation is not allowed')
    .replace(/^\[?ERROR\b\]?:?\s*/i, '')
    .replace(/^Exception:\s*/i, '')
    .replace(/\s+/g, ' ')
    .trim();
  if (/^Traceback\b/i.test(text)) text = '';
  const characters = Array.from(text);
  if (characters.length > MAX_SUMMARY_CHARS) text = `${characters.slice(0, MAX_SUMMARY_CHARS - 1).join('')}…`;
  return text;
}

function sanitizeErrorCode(value: string): string | null {
  return safeErrorCode(value) ?? null;
}

function parseStructuredText(value: string): unknown | null {
  const trimmed = value.trim();
  if (trimmed.length < 2 || trimmed.length > MAX_SCAN_CHARS) return null;
  if (!((trimmed.startsWith('{') && trimmed.endsWith('}')) || (trimmed.startsWith('[') && trimmed.endsWith(']')))) {
    return null;
  }
  try {
    return JSON.parse(trimmed);
  } catch {
    return null;
  }
}

function safeEntries(value: object): Array<[string, unknown]> {
  try {
    return Object.entries(value);
  } catch {
    return [];
  }
}

function normalizeField(value: string): string {
  return value.replace(/[-\s]/g, '_').toLowerCase();
}

function fieldPriority(field: string): number {
  return FIELD_PRIORITY[field] ?? 0;
}

function isErrorCodeField(field: string): boolean {
  return field === 'error_code' || field === 'errorcode' || field === 'code';
}

function isExitCodeField(field: string): boolean {
  return field === 'exit_code' || field === 'exitcode' || field === 'return_code' || field === 'returncode';
}

function isHTTPStatusField(field: string): boolean {
  return field === 'http_status' || field === 'httpstatus' || field === 'status_code' || field === 'statuscode';
}

function inferHTTPStatus(summary: string | null): number | null {
  if (!summary) return null;
  const match = summary.match(/\b(?:HTTP\s*)?([45]\d\d)\b/i);
  return match ? Number(match[1]) : null;
}
