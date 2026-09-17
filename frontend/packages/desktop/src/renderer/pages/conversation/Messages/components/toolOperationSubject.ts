import type { NormalizedToolCall } from '@/common/chat/normalizeToolCall';
import { toolPublicDetailText, type ToolPublicDetailTextKey } from '@/renderer/services/i18n/toolPublicDetailLocale';
import { getToolActivityKind, type ToolActivityKind } from '../toolActivityPresentationRegistry';

type SubjectField = { key: string; format?: 'file' | 'host' | 'source' | 'packages' };
const fields = (...keys: string[]): SubjectField[] => keys.map((key) => ({ key }));
const files: SubjectField[] = [
  'file_path',
  'path',
  'filename',
  'file',
  'files',
  'paths',
  'input_file',
  'output_file',
].map((key) => ({ key, format: 'file' }));
const scientific = fields(
  'query',
  'term',
  'subject',
  'target',
  'accession',
  'accessions',
  'uniprot_accession',
  'pdb_id',
  'pdb_ids',
  'gene_symbol',
  'gene',
  'protein',
  'compound',
  'dataset',
  'title',
  'identifier'
);

// One allow-listed subject projection for every public activity kind. Never
// mine arbitrary output, code or metadata for a second generated description.
const SUBJECT_FIELDS: Record<ToolActivityKind, SubjectField[]> = {
  analysis: [...scientific, ...files],
  inspect: [...files, ...scientific],
  search: [...scientific, ...fields('name', 'pattern')],
  fetch: [...scientific, ...files, { key: 'url', format: 'source' }],
  save: [...files, ...fields('title', 'subject')],
  edit: [...files, ...fields('title', 'subject')],
  prepare: fields('query', 'pattern', 'name', 'title', 'subject', 'target'),
  plan: fields('task_summary', 'title', 'objective'),
  environment: [
    { key: 'packages', format: 'packages' },
    { key: 'pip_packages', format: 'packages' },
    { key: 'conda_packages', format: 'packages' },
    ...fields('language'),
  ],
  compute: [
    ...fields('job_name', 'title', 'subject', 'intent', 'provider', 'question', 'environment', 'direction'),
    ...files,
  ],
  memory: fields('query', 'entity', 'category', 'subject', 'title'),
  access: [{ key: 'url', format: 'host' }, ...fields('domain', 'reason', 'capability', 'operation')],
  other: [...scientific, ...files],
};

const unsafeSubject =
  /\p{Cc}|[\u202a-\u202e\u2066-\u2069<>{}`]|(?:api[_ -]?key|(?:access[_ -]?)?token|authorization|password|secret|credential)\s*[:=]|\b(?:sk-|ark-)[a-z0-9-]{12,}|(?:^|\s)(?:--[a-z]|[a-z]:\\|\/(?:home|users|mnt|etc)\/)|(?:system prompt|runtime policy|harness|ledger|backend|frontend|mcp__)/iu;

function record(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

function inputRecords(input: string | undefined): Record<string, unknown>[] {
  if (!input || input.length > 512 * 1024) return [];
  try {
    const parsed: unknown = JSON.parse(input);
    if (!record(parsed)) return [];
    const records = [parsed];
    for (let index = 0; index < records.length && index < 3; index++) {
      for (const key of ['input', 'arguments', 'params']) {
        const nested = records[index][key];
        if (record(nested) && records.length < 4) records.push(nested);
      }
    }
    return records;
  } catch {
    // A partially streamed input is not an authoritative object yet.
    return [];
  }
}

function publicValue(value: unknown, format?: SubjectField['format'], depth = 0): string | null {
  if (depth > 2) return null;
  if (record(value)) {
    const keys = format === 'file' ? ['path', 'file_path', 'filename', 'name'] : ['name', 'package', 'spec'];
    for (const key of keys) {
      const result = publicValue(value[key], format, depth + 1);
      if (result) return result;
    }
    return null;
  }
  if (typeof value !== 'string' || value.length > 4096) return null;
  let text = value.trim();
  if (!text) return null;
  if (format === 'host' || format === 'source') {
    try {
      const url = new URL(text);
      if (!['http:', 'https:'].includes(url.protocol)) return null;
      if (format === 'host') return url.hostname;
      const parts = url.pathname.split('/').filter(Boolean).slice(-2);
      const subject = parts.map((part) => decodeURIComponent(part)).join('/');
      // Keep public path identifiers, but never signed query parameters,
      // encoded control characters, credentials or opaque long path tokens.
      const safePath =
        subject &&
        parts.every((part) => part.length <= 80) &&
        !/^(?:file|index|default|download|page)$/iu.test(subject) &&
        !unsafeSubject.test(subject) &&
        !/token|secret|signature|credential/iu.test(subject);
      return safePath ? `${url.hostname} · ${subject}` : url.hostname;
    } catch {
      return null;
    }
  }
  if (format === 'file') {
    // Expose only a leaf name, never a personal/host directory or signed URL.
    if (/^[a-z]+:\/\//iu.test(text)) return null;
    text = text.replaceAll('\\', '/').split('/').at(-1) ?? '';
    if (!text || text.startsWith('.') || /^(?:credentials?|secrets?)(?:\.|$)/iu.test(text)) return null;
  }
  if (format === 'packages') {
    // Version operators are data, not HTML; URLs and installer switches are not.
    const namedSource = text.match(/^([a-z0-9][a-z0-9_.-]*)\s+@\s+https:\/\//iu);
    if (namedSource) return namedSource[1];
    return /^(?:[a-z0-9][a-z0-9_.-]*::)?[a-z0-9][a-z0-9_.-]*(?:\[[a-z0-9_,.-]+\])?(?:[<>=!~]+[a-z0-9*_.+,-]+)?$/iu.test(
      text
    )
      ? text
      : null;
  }
  if (unsafeSubject.test(text)) return null;
  return text.replace(/\s+/gu, ' ');
}

function fieldValue(value: unknown, format: SubjectField['format'], chinese: boolean): string | null {
  if (!Array.isArray(value)) return publicValue(value, format);
  const values = [
    ...new Set(
      value
        .slice(0, 30)
        .map((entry) => publicValue(entry, format))
        .filter((entry): entry is string => !!entry)
    ),
  ];
  if (!values.length) return null;
  const remaining = values.length > 3 || value.length > 30;
  const subjects = values.slice(0, 3).join(', ');
  return remaining ? toolPublicDetailText(chinese, 'subjectListMore', { subjects }) : subjects;
}

export function toolOperationSubject(
  tool: NormalizedToolCall,
  chinese: boolean,
  includeCodeFallback = true
): string | null {
  const records = inputRecords(tool.input);
  const kind = getToolActivityKind(tool.name);
  for (const input of records) {
    if (kind === 'environment') {
      const phasePackages = Array.isArray(input.pip_phases)
        ? input.pip_phases.slice(0, 10).flatMap((phase) => (Array.isArray(phase) ? phase.slice(0, 30) : []))
        : [];
      const packages = [...(Array.isArray(input.packages) ? input.packages.slice(0, 30) : []), ...phasePackages];
      const packageSubject = fieldValue(packages, 'packages', chinese);
      if (packageSubject) return packageSubject;
    }
    for (const field of SUBJECT_FIELDS[kind]) {
      const result = fieldValue(input[field.key], field.format, chinese);
      if (result) return Array.from(result).slice(0, 120).join('');
    }
  }
  if (kind === 'analysis' && includeCodeFallback) {
    for (const input of records) {
      const source = [input.code, input.command, input.script].find(
        (value): value is string => typeof value === 'string' && !!value.trim()
      );
      if (source) {
        const count = source.trimEnd().split(/\r?\n/u).length;
        const language = ['python', 'r', 'bash', 'powershell'].includes(tool.name.toLowerCase()) ? tool.name : '';
        return `${language ? `${language} · ` : ''}${toolPublicDetailText(chinese, 'subjectCodeLines', { count })}`;
      }
    }
  }
  return null;
}

export function toolOperationAction(tool: NormalizedToolCall, chinese: boolean): string | undefined {
  if (getToolActivityKind(tool.name) !== 'environment') return undefined;
  const input = inputRecords(tool.input)[0];
  const action = input?.mode ?? input?.action;
  if (typeof action !== 'string') return undefined;
  const labels: Record<string, ToolPublicDetailTextKey> = {
    install: 'actionInstallPackages',
    remove: 'actionRemovePackages',
    uninstall: 'actionRemovePackages',
    update: 'actionUpdatePackages',
    create: 'actionCreateEnvironment',
    list: 'actionListEnvironmentPackages',
    inspect: 'actionInspectEnvironment',
    delete: 'actionDeleteEnvironment',
  };
  return Object.hasOwn(labels, action) ? toolPublicDetailText(chinese, labels[action]) : undefined;
}
