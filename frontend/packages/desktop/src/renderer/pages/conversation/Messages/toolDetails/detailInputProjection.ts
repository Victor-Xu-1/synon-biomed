import { toolPublicDetailText, type ToolPublicDetailTextKey } from '@/renderer/services/i18n/toolPublicDetailLocale';
import type { ToolPublicDetailKind } from '../toolActivityPresentationRegistry';
import { formatPublicTaskPath } from '../components/toolPublicDetailBlocks';
import { isPublicNarrativeSafe } from '../components/toolStepSummaryModel';
import {
  formatPublicCollectionItem,
  formatPublicRecord,
  formatPublicRecordValue,
  isPublicRecordKey,
  isRecord,
  publicLanguageLabel,
  publicMemoryScopeLabel,
  publicModeLabel,
  publicRecordKeyLabel,
  truncate,
} from './detailFormatting';
import type { ToolPublicDetailCollection, ToolPublicDetailRow } from './detailTypes';
import { publicSourceUrl } from './retrievalReceiptPresentation';
import { formatBytes } from './detailFormatting';

const PUBLIC_INPUT_KEYS = [
  'query',
  'term',
  'pattern',
  'dataset',
  'subject',
  'title',
  'accession',
  'accessions',
  'identifier',
  'url',
  'domain',
  'mode',
  'capability',
  'environment',
  'provider',
  'operation',
  'use_pip',
  'background',
  'timeout_seconds',
  'seed',
  'language',
  'python_version',
  'retmax',
  'max_results',
  'task_summary',
  'file_path',
  'path',
  'skill',
  'filename',
  'host_path',
  'entity',
  'category',
  'reason',
  'question',
  'intent',
  'direction',
  'local',
  'remote',
  'scheduler',
  'max_concurrent',
] as const;

const PUBLIC_INPUT_COLLECTION_KEYS = [
  'dependencies',
  'packages',
  'desired_outputs',
  'files',
  'checkpoints',
  'paths',
  'inputs',
  'outputs',
  'append',
  'replace',
] as const;

const PUBLIC_INPUT_BLOCK_KEYS = new Set(['code', 'command', 'content', 'old_string', 'new_string', 'old_text', 'text']);
const PUBLIC_INPUT_STRUCTURAL_KEYS = new Set<string>([
  'human_description',
  'resource_requirements',
  ...PUBLIC_INPUT_KEYS,
  ...PUBLIC_INPUT_COLLECTION_KEYS,
]);

const INPUT_KEYS_BY_DETAIL_KIND: Record<ToolPublicDetailKind, readonly (typeof PUBLIC_INPUT_KEYS)[number][]> = {
  analysis: ['dataset', 'subject', 'title', 'accession', 'identifier', 'domain', 'language', 'background', 'seed'],
  inspection: ['file_path', 'path', 'dataset', 'subject', 'title', 'accession', 'accessions', 'identifier'],
  research: ['query', 'term', 'pattern', 'subject', 'title', 'accession', 'accessions', 'retmax', 'max_results'],
  retrieval: ['url', 'domain', 'query', 'term', 'title', 'accession', 'identifier'],
  artifact: ['title', 'subject'],
  file: ['file_path', 'path', 'title', 'subject'],
  method: ['query', 'term', 'subject', 'title'],
  plan: ['title', 'subject'],
  environment: ['mode', 'language', 'python_version', 'use_pip'],
  compute: [
    'provider',
    'mode',
    'intent',
    'question',
    'environment',
    'direction',
    'local',
    'remote',
    'scheduler',
    'timeout_seconds',
    'max_concurrent',
  ],
  memory: ['query', 'entity', 'category', 'subject', 'title'],
  access: ['domain', 'host_path', 'mode', 'reason', 'capability', 'operation'],
  generic: ['query', 'term', 'subject', 'title', 'identifier', 'mode'],
};

const INPUT_COLLECTION_KEYS_BY_DETAIL_KIND: Record<ToolPublicDetailKind, readonly string[]> = {
  analysis: ['files'],
  inspection: ['files'],
  research: [],
  retrieval: [],
  artifact: ['files', 'checkpoints'],
  file: ['files', 'paths'],
  method: ['desired_outputs'],
  plan: [],
  environment: ['dependencies', 'packages'],
  compute: ['inputs', 'outputs', 'files'],
  memory: ['append', 'replace'],
  access: ['paths'],
  generic: PUBLIC_INPUT_COLLECTION_KEYS,
};

export function buildToolDetailInputProjection(
  input: Record<string, unknown>,
  chinese: boolean,
  detailKind: ToolPublicDetailKind,
  byteLimit = false
): { rows: ToolPublicDetailRow[]; collections: ToolPublicDetailCollection[] } {
  const rows = buildInputRows(input, chinese, detailKind, byteLimit);
  appendResourceRows(rows, input.resource_requirements, chinese);
  return {
    rows,
    collections: [
      ...buildCollections(input, INPUT_COLLECTION_KEYS_BY_DETAIL_KIND[detailKind], chinese),
      ...buildSupplementalInputCollections(input, detailKind, chinese),
    ],
  };
}

function buildInputRows(
  input: Record<string, unknown>,
  chinese: boolean,
  detailKind: ToolPublicDetailKind,
  byteLimit: boolean
): ToolPublicDetailRow[] {
  const rows: ToolPublicDetailRow[] = [];
  for (const key of INPUT_KEYS_BY_DETAIL_KIND[detailKind]) {
    const value = formatPublicValue(input[key], key, chinese);
    if (value) rows.push({ label: inputLabel(key, chinese), value });
  }
  for (const [key, candidate] of Object.entries(input)) {
    if (PUBLIC_INPUT_STRUCTURAL_KEYS.has(key) || PUBLIC_INPUT_BLOCK_KEYS.has(key)) continue;
    if (!isPublicRecordKey(key) || key.startsWith('_') || Array.isArray(candidate) || isRecord(candidate)) continue;
    if (detailKind === 'environment' && ['name', 'create', 'force'].includes(key)) continue;
    if (
      byteLimit &&
      key === 'limit' &&
      typeof candidate === 'number' &&
      Number.isSafeInteger(candidate) &&
      candidate >= 0
    ) {
      rows.push({ label: toolPublicDetailText(chinese, 'readLimit'), value: formatBytes(candidate) });
      continue;
    }
    const value = formatSupplementalInputValue(candidate, key, chinese);
    if (value) rows.push({ label: publicRecordKeyLabel(key, chinese), value });
  }
  return rows;
}

function buildCollections(
  value: Record<string, unknown>,
  keys: readonly string[],
  chinese: boolean
): ToolPublicDetailCollection[] {
  const collections: ToolPublicDetailCollection[] = [];
  for (const key of keys) {
    const candidate = value[key];
    if (!Array.isArray(candidate) || candidate.length === 0) continue;
    const items = candidate
      .map((item) => formatPublicCollectionItem(item, chinese, key))
      .filter((item): item is string => Boolean(item));
    if (items.length === 0) continue;
    const uniqueItems = [...new Set(items)];
    collections.push({
      label: inputCollectionLabel(key, chinese),
      items: uniqueItems,
      total: uniqueItems.length,
    });
  }
  return collections;
}

function buildSupplementalInputCollections(
  input: Record<string, unknown>,
  detailKind: ToolPublicDetailKind,
  chinese: boolean
): ToolPublicDetailCollection[] {
  if (detailKind === 'plan' || detailKind === 'method' || detailKind === 'environment') return [];
  const explicitKeys = new Set(INPUT_COLLECTION_KEYS_BY_DETAIL_KIND[detailKind]);
  const collections: ToolPublicDetailCollection[] = [];
  for (const [key, candidate] of Object.entries(input)) {
    if (!Array.isArray(candidate) || candidate.length === 0 || explicitKeys.has(key)) continue;
    if (PUBLIC_INPUT_STRUCTURAL_KEYS.has(key) || !isPublicRecordKey(key) || key.startsWith('_')) continue;
    const items = candidate
      .map((item) => (isRecord(item) ? formatPublicRecord(item, chinese) : formatSupplementalCollectionItem(item, key)))
      .filter((item): item is string => Boolean(item));
    if (items.length === 0) continue;
    const uniqueItems = [...new Set(items)];
    collections.push({
      label: publicRecordKeyLabel(key, chinese),
      items: uniqueItems,
      total: uniqueItems.length,
    });
  }
  return collections;
}

function formatSupplementalInputValue(value: unknown, key: string, chinese: boolean): string | null {
  if (typeof value === 'string' && /(?:^|_)(?:url|uri|domain)(?:_|$)/iu.test(key)) {
    const inputKey = key.toLowerCase().includes('domain') ? 'domain' : 'url';
    return formatPublicValue(value, inputKey, chinese);
  }
  if (typeof value === 'boolean') return toolPublicDetailText(chinese, value ? 'yes' : 'no');
  return formatPublicRecordValue(value, chinese);
}

function formatSupplementalCollectionItem(value: unknown, key: string): string | null {
  if (typeof value === 'string' && /(?:^|_)(?:path|file|files)(?:_|$)/iu.test(key)) {
    return formatPublicTaskPath(value);
  }
  return formatPublicRecordValue(value);
}

function appendResourceRows(rows: ToolPublicDetailRow[], value: unknown, chinese: boolean) {
  if (!isRecord(value)) return;
  for (const [key, labelKey, unit] of [
    ['cpu_cores', 'cpu', 'cores'],
    ['memory_gb', 'memory', 'gb'],
    ['disk_gb', 'storage', 'gb'],
  ] as const) {
    const candidate = value[key];
    if (typeof candidate !== 'number' || !Number.isFinite(candidate) || candidate <= 0) continue;
    rows.push({
      label: toolPublicDetailText(chinese, labelKey),
      value: unit === 'cores' ? toolPublicDetailText(chinese, 'cpuCores', { count: candidate }) : `${candidate} GB`,
    });
  }
}

function formatPublicValue(value: unknown, key: (typeof PUBLIC_INPUT_KEYS)[number], chinese: boolean): string | null {
  if (typeof value === 'string') {
    const normalized = value.trim().replace(/\s+/g, ' ');
    if (!normalized) return null;
    if (key === 'file_path' || key === 'path' || key === 'host_path' || key === 'local' || key === 'remote') {
      return formatPublicTaskPath(normalized);
    }
    if (key === 'url' || key === 'domain') {
      const source = publicSourceUrl(
        key === 'domain' ? `https://${normalized.replace(/^https?:\/\//iu, '')}` : normalized
      );
      return source && key === 'domain' ? new URL(source).hostname : source;
    }
    if (key === 'mode') return publicModeLabel(normalized, chinese);
    if (key === 'language') return publicLanguageLabel(normalized);
    if (key === 'entity') return publicMemoryScopeLabel(normalized, chinese);
    return isPublicNarrativeSafe(normalized) ? truncate(normalized, 180) : null;
  }
  if (typeof value === 'number' && Number.isFinite(value)) return String(value);
  if (typeof value === 'boolean') {
    if ((key === 'background' || key === 'use_pip') && value === false) return null;
    return toolPublicDetailText(chinese, value ? 'yes' : 'no');
  }
  if (!Array.isArray(value) || value.length === 0) return null;
  const publicStrings = value.filter((item): item is string => typeof item === 'string' && isPublicNarrativeSafe(item));
  if (publicStrings.length === value.length) return publicStrings.slice(0, 5).join(', ');
  return toolPublicDetailText(chinese, 'itemCount', {
    count: value.length,
    itemWord: value.length === 1 ? 'item' : 'items',
  });
}

function inputLabel(key: (typeof PUBLIC_INPUT_KEYS)[number], chinese: boolean): string {
  const labels: Record<(typeof PUBLIC_INPUT_KEYS)[number], ToolPublicDetailTextKey> = {
    query: 'inputQuery',
    term: 'inputTerm',
    pattern: 'inputPattern',
    dataset: 'inputDataset',
    subject: 'inputSubject',
    title: 'inputTitle',
    accession: 'inputAccession',
    accessions: 'inputAccessions',
    identifier: 'inputIdentifier',
    url: 'inputUrl',
    domain: 'inputDomain',
    mode: 'inputMode',
    capability: 'inputCapability',
    environment: 'inputEnvironment',
    provider: 'inputProvider',
    operation: 'inputOperation',
    use_pip: 'inputUsePip',
    background: 'inputBackground',
    timeout_seconds: 'inputTimeout',
    seed: 'inputSeed',
    language: 'inputLanguage',
    python_version: 'inputPythonVersion',
    retmax: 'inputResultLimit',
    max_results: 'inputResultLimit',
    task_summary: 'inputTaskSummary',
    file_path: 'inputFilePath',
    path: 'inputPath',
    skill: 'inputSkill',
    filename: 'inputFilename',
    host_path: 'inputHostPath',
    entity: 'inputEntity',
    category: 'inputCategory',
    reason: 'inputReason',
    question: 'inputQuestion',
    intent: 'inputIntent',
    direction: 'inputDirection',
    local: 'inputLocal',
    remote: 'inputRemote',
    scheduler: 'inputScheduler',
    max_concurrent: 'inputMaxConcurrent',
  };
  return toolPublicDetailText(chinese, labels[key]);
}

function inputCollectionLabel(key: string, chinese: boolean): string {
  const labels: Record<string, ToolPublicDetailTextKey> = {
    dependencies: 'collectionInputDependencies',
    packages: 'collectionInputPackages',
    desired_outputs: 'collectionDesiredOutputs',
    files: 'collectionFiles',
    checkpoints: 'collectionCheckpoints',
    paths: 'collectionPaths',
    inputs: 'collectionInputs',
    outputs: 'collectionOutputs',
    append: 'collectionAppend',
    replace: 'collectionReplace',
  };
  return toolPublicDetailText(chinese, labels[key] ?? 'collectionDetails');
}
