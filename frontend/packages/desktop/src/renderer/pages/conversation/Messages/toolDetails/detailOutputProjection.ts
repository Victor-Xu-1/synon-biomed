import { toolPublicDetailText, type ToolPublicDetailTextKey } from '@/renderer/services/i18n/toolPublicDetailLocale';
import type { ToolPublicDetailKind } from '../toolActivityPresentationRegistry';
import { isPublicNarrativeSafe } from '../components/toolStepSummaryModel';
import {
  collectionCount,
  dedupeRows,
  formatBytes,
  formatDuration,
  formatEnvironmentBlocker,
  formatEnvironmentRecord,
  formatPublicCollectionItem,
  formatPublicRecord,
  formatPublicRecordValue,
  isPublicRecordKey,
  isRecord,
  normalizePublicRecordKey,
  publicEnvironmentContext,
  publicEnvironmentOutputLabel,
  publicRecordKeyLabel,
  truncate,
} from './detailFormatting';
import type { ToolPublicDetailCollection, ToolPublicDetailRow } from './detailTypes';
import { publicSourceUrl } from './retrievalReceiptPresentation';

const PUBLIC_OUTPUT_COLLECTION_KEYS = [
  'packages',
  'requested_packages',
  'dependencies',
  'records',
  'results',
  'items',
  'ligands',
  'artifacts',
  'sources',
  'environments',
  'providers',
  'jobs',
  'grants',
  'memories',
  'matches',
  'trashed',
] as const;
const PUBLIC_OUTPUT_TEXT_KEYS = ['summary', 'message', 'notes', 'description', 'next_action', 'recovery'] as const;
const PUBLIC_OUTPUT_COUNT_KEYS = [
  'count',
  'retrieved',
  'returnedResults',
  'results',
  'records',
  'items',
  'environments',
  'artifacts',
  'sources',
  'phases',
  'package_count',
  'files_written',
  'total_count',
  'n_retrieved',
  'n_returned',
  'selected_count',
  'scaffold_count',
  'providers',
  'jobs',
  'grants',
  'memories',
  'matches',
  'results_returned',
  'appended',
  'replaced',
  'removed',
  'trashed',
] as const;

const PUBLIC_OUTPUT_STRUCTURAL_KEYS = new Set<string>([
  ...PUBLIC_OUTPUT_COUNT_KEYS,
  ...PUBLIC_OUTPUT_COLLECTION_KEYS,
  ...PUBLIC_OUTPUT_TEXT_KEYS,
  'stdout',
  'stderr',
  'content',
  'text',
  'markdown',
  'preview',
  'output',
  'result',
  'data',
  'environment',
  'diagnostics',
  'provider',
  'job',
  'grant',
  'durationSeconds',
  'granted',
  'recoverable',
  'cancelled',
  'bytes',
  'status',
  'state',
  'direction',
  'filename',
  'name',
]);

const OUTPUT_COLLECTION_KEYS_BY_DETAIL_KIND: Record<ToolPublicDetailKind, readonly string[]> = {
  analysis: ['records', 'results', 'items', 'ligands', 'artifacts'],
  inspection: ['records', 'results', 'items', 'files'],
  research: ['records', 'results', 'sources'],
  retrieval: ['records', 'results', 'sources'],
  artifact: ['artifacts', 'files', 'results'],
  file: ['files', 'artifacts', 'trashed'],
  method: ['results', 'items'],
  plan: [],
  environment: ['environments', 'packages', 'requested_packages', 'dependencies', 'items'],
  compute: ['providers', 'jobs', 'environments', 'files', 'artifacts', 'results', 'items'],
  memory: ['memories', 'matches', 'records', 'results', 'items'],
  access: ['grants', 'trashed', 'files', 'items'],
  generic: PUBLIC_OUTPUT_COLLECTION_KEYS,
};

export function buildToolDetailOutputProjection(
  outputSources: Record<string, unknown>[],
  detailKind: ToolPublicDetailKind,
  chinese: boolean,
  hasOutputBlocks: boolean
): {
  rows: ToolPublicDetailRow[];
  collections: ToolPublicDetailCollection[];
  narrative: string[];
} {
  const rows: ToolPublicDetailRow[] = [];
  for (const outputSource of outputSources) appendOutputRowsDeep(rows, outputSource, chinese, detailKind);
  const collections = outputSources.flatMap((outputSource) => [
    ...buildCollectionsDeep(outputSource, OUTPUT_COLLECTION_KEYS_BY_DETAIL_KIND[detailKind], chinese),
    ...buildSupplementalOutputCollectionsDeep(outputSource, detailKind, chinese),
  ]);
  const narrative = outputSources.flatMap((outputSource) =>
    hasOutputBlocks
      ? []
      : [...collectStructuredNarrative(outputSource), ...collectSafeExecutionHighlights(outputSource)]
  );
  return { rows, collections, narrative };
}

export function normalizeToolDetailResultRows(
  rows: ToolPublicDetailRow[],
  collections: ToolPublicDetailCollection[],
  detailKind: ToolPublicDetailKind,
  chinese: boolean
): ToolPublicDetailRow[] {
  let normalized = dedupeRows(rows);
  if (detailKind === 'environment') {
    const environments = collections.find(
      (collection) => collection.label === collectionLabel('environments', chinese)
    );
    if (environments) {
      const label = outputLabel('environments', chinese);
      normalized = normalized.map((row) =>
        row.label === label
          ? {
              label,
              value: toolPublicDetailText(chinese, 'itemCount', {
                count: environments.total,
                itemWord: environments.total === 1 ? 'item' : 'items',
              }),
            }
          : row
      );
    }
  }
  const genericCountLabel = outputLabel('count', chinese);
  if (normalized.some((row) => row.label !== genericCountLabel)) {
    normalized = normalized.filter((row) => row.label !== genericCountLabel);
  }
  return dedupeRows(normalized);
}

export function collectPlainSafeHighlights(value: string | undefined): string[] {
  if (!value) return [];
  const highlights: string[] = [];
  for (const rawLine of value.replace(/\r/gu, '').split('\n')) {
    const line = rawLine.trim().replace(/^[✓✔•*-]+\s*/u, '');
    if (line.length < 2 || !isPublicNarrativeSafe(line)) continue;
    highlights.push(truncate(line, 360));
  }
  return highlights;
}

function appendOutputRowsDeep(
  rows: ToolPublicDetailRow[],
  output: Record<string, unknown>,
  chinese: boolean,
  detailKind: ToolPublicDetailKind
) {
  for (const record of collectOutputRecords(output)) {
    for (const key of PUBLIC_OUTPUT_COUNT_KEYS) {
      const count = collectionCount(record[key]);
      if (count === null) continue;
      rows.push({
        label: outputLabel(key, chinese),
        value: toolPublicDetailText(chinese, 'itemCount', {
          count,
          itemWord: count === 1 ? 'item' : 'items',
        }),
      });
    }
    const duration = record.durationSeconds;
    if (typeof duration === 'number' && Number.isFinite(duration) && duration >= 0) {
      rows.push({
        label: toolPublicDetailText(chinese, 'duration'),
        value: formatDuration(duration, chinese),
      });
    }
    if (typeof record.granted === 'boolean') {
      rows.push({
        label: toolPublicDetailText(chinese, 'accessState'),
        value: toolPublicDetailText(chinese, record.granted ? 'allowed' : 'notAllowed'),
      });
    }
    if (typeof record.recoverable === 'boolean') {
      rows.push({
        label: toolPublicDetailText(chinese, 'recovery'),
        value: toolPublicDetailText(chinese, record.recoverable ? 'recoverableFromTrash' : 'notRecoverable'),
      });
    }
    if (typeof record.cancelled === 'boolean') {
      rows.push({
        label: toolPublicDetailText(chinese, 'computeState'),
        value: toolPublicDetailText(chinese, record.cancelled ? 'cancelled' : 'stillRunning'),
      });
    }
    if (typeof record.bytes === 'number' && Number.isFinite(record.bytes) && record.bytes >= 0) {
      rows.push({
        label: toolPublicDetailText(chinese, 'dataSize'),
        value: formatBytes(record.bytes),
      });
    }
    const publicScalarFields: Array<readonly [string, ToolPublicDetailTextKey]> = [];
    if (detailKind === 'compute') {
      publicScalarFields.push(
        ['provider', 'computeProvider'],
        ['family', 'resourceType'],
        ['status', 'status'],
        ['state', 'state'],
        ['direction', 'transferDirection']
      );
    }
    if (detailKind === 'artifact' || detailKind === 'file' || detailKind === 'retrieval') {
      publicScalarFields.push(['filename', 'filename']);
    }
    for (const [key, labelKey] of publicScalarFields) {
      const value = formatPublicRecordValue(record[key]);
      if (value) rows.push({ label: toolPublicDetailText(chinese, labelKey), value });
    }
    for (const [key, candidate] of Object.entries(record)) {
      if (PUBLIC_OUTPUT_STRUCTURAL_KEYS.has(key) || !isPublicRecordKey(key) || key.startsWith('_')) continue;
      if (Array.isArray(candidate) || isRecord(candidate)) continue;
      const value = formatSupplementalOutputValue(candidate, key, chinese);
      if (value) rows.push({ label: publicRecordKeyLabel(key, chinese), value });
    }
  }
}

function formatSupplementalOutputValue(value: unknown, key: string, chinese: boolean): string | null {
  const normalizedKey = normalizePublicRecordKey(key);
  if (normalizedKey === 'bytes_read' && typeof value === 'number') {
    return Number.isSafeInteger(value) && value >= 0 ? formatBytes(value) : null;
  }
  if (['url', 'source_url', 'requested_url'].includes(normalizedKey)) return publicSourceUrl(value);
  if ((normalizedKey === 'mode' || normalizedKey === 'operation') && typeof value === 'string') {
    return publicEnvironmentOutputLabel(value, chinese);
  }
  if (normalizedKey === 'recommendation' && typeof value === 'string') {
    const recommendations: Record<string, ToolPublicDetailTextKey> = {
      reuse: 'recommendReuse',
      create: 'recommendCreate',
      install: 'recommendInstall',
      proceed: 'recommendProceed',
    };
    const labelKey = recommendations[value.trim().toLowerCase()];
    return labelKey ? toolPublicDetailText(chinese, labelKey) : formatPublicRecordValue(value, chinese);
  }
  if (normalizedKey === 'feasible' && typeof value === 'boolean') {
    return toolPublicDetailText(chinese, value ? 'feasible' : 'notFeasible');
  }
  if (normalizedKey === 'available' && typeof value === 'boolean') {
    return toolPublicDetailText(chinese, value ? 'available' : 'notAvailable');
  }
  if (typeof value === 'boolean') {
    const values: Record<string, readonly [ToolPublicDetailTextKey, ToolPublicDetailTextKey]> = {
      selected: ['selected', 'notSelected'],
      created: ['created', 'notCreated'],
      success: ['successful', 'unsuccessful'],
      search_truncated: ['truncated', 'complete'],
    };
    const localized = values[normalizedKey]?.[value ? 0 : 1];
    if (localized) return toolPublicDetailText(chinese, localized);
    return toolPublicDetailText(chinese, value ? 'yes' : 'no');
  }
  if (normalizedKey === 'reason' && typeof value === 'string') {
    const reason = value.trim().toLowerCase();
    if (reason === 'no_open_access_full_text' || reason === 'full_text_not_found' || reason === 'not_open_access') {
      return toolPublicDetailText(chinese, 'noOpenAccess');
    }
  }
  return formatPublicRecordValue(value, chinese);
}

function collectOutputRecords(value: Record<string, unknown>, depth = 0): Record<string, unknown>[] {
  if (depth > 4) return [];
  const records = [value];
  for (const key of ['result', 'data', 'environment', 'diagnostics', 'provider', 'job', 'grant']) {
    const nested = value[key];
    if (isRecord(nested)) records.push(...collectOutputRecords(nested, depth + 1));
  }
  return records;
}

function buildCollectionsDeep(
  value: Record<string, unknown>,
  keys: readonly string[],
  chinese: boolean
): ToolPublicDetailCollection[] {
  return collectOutputRecords(value).flatMap((record) => buildCollections(record, keys, chinese));
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
      label: collectionLabel(key, chinese),
      items: uniqueItems,
      total: uniqueItems.length,
    });
  }
  return collections;
}

function buildSupplementalOutputCollectionsDeep(
  value: Record<string, unknown>,
  detailKind: ToolPublicDetailKind,
  chinese: boolean
): ToolPublicDetailCollection[] {
  const collections: ToolPublicDetailCollection[] = [];
  for (const record of collectOutputRecords(value)) {
    for (const [key, candidate] of Object.entries(record)) {
      if (!Array.isArray(candidate) || candidate.length === 0) continue;
      if (PUBLIC_OUTPUT_STRUCTURAL_KEYS.has(key) || !isPublicRecordKey(key) || key.startsWith('_')) continue;
      const items = candidate
        .map((item) => {
          if (detailKind === 'environment' && /environments?/iu.test(key)) {
            return isRecord(item) ? formatEnvironmentRecord(item, chinese) : publicEnvironmentContext(item);
          }
          if (detailKind === 'environment' && normalizePublicRecordKey(key) === 'mutation_blockers') {
            return formatEnvironmentBlocker(item, chinese);
          }
          return isRecord(item) ? formatPublicRecord(item, chinese) : formatPublicRecordValue(item, chinese);
        })
        .filter((item): item is string => Boolean(item));
      if (items.length === 0) continue;
      const uniqueItems = [...new Set(items)];
      collections.push({
        label: publicRecordKeyLabel(key, chinese),
        items: uniqueItems,
        total: uniqueItems.length,
      });
    }
  }
  return collections;
}

function collectStructuredNarrative(value: unknown, depth = 0): string[] {
  if (!isRecord(value) || depth > 3) return [];
  const result: string[] = [];
  for (const key of PUBLIC_OUTPUT_TEXT_KEYS) {
    const candidate = value[key];
    if (typeof candidate === 'string' && isPublicNarrativeSafe(candidate)) result.push(candidate.trim());
  }
  for (const key of ['result', 'data']) result.push(...collectStructuredNarrative(value[key], depth + 1));
  return result;
}

function collectSafeExecutionHighlights(value: unknown, depth = 0): string[] {
  if (!isRecord(value) || depth > 3) return [];
  const result: string[] = [];
  for (const key of ['stdout', 'preview']) {
    const candidate = value[key];
    if (typeof candidate !== 'string') continue;
    for (const rawLine of candidate.replace(/\r/gu, '').split('\n')) {
      const line = rawLine.trim().replace(/^[✓✔•*-]+\s*/u, '');
      if (line.length < 4 || !isPublicNarrativeSafe(line)) continue;
      result.push(truncate(line, 220));
    }
  }
  for (const key of ['result', 'data']) result.push(...collectSafeExecutionHighlights(value[key], depth + 1));
  return result.slice(0, 6);
}

function outputLabel(key: (typeof PUBLIC_OUTPUT_COUNT_KEYS)[number], chinese: boolean): string {
  const labels: Record<(typeof PUBLIC_OUTPUT_COUNT_KEYS)[number], ToolPublicDetailTextKey> = {
    count: 'outputCount',
    retrieved: 'outputRetrieved',
    returnedResults: 'outputReturned',
    results: 'outputResults',
    records: 'outputRecords',
    items: 'outputItems',
    environments: 'outputEnvironments',
    artifacts: 'outputArtifacts',
    sources: 'outputSources',
    phases: 'outputPhases',
    package_count: 'outputPackageCount',
    files_written: 'outputFilesWritten',
    total_count: 'outputTotalCount',
    n_retrieved: 'outputRetrieved',
    n_returned: 'outputReturned',
    selected_count: 'outputSelectedCount',
    scaffold_count: 'outputScaffoldCount',
    providers: 'outputProviders',
    jobs: 'outputJobs',
    grants: 'outputGrants',
    memories: 'outputMemories',
    matches: 'outputMatches',
    results_returned: 'outputMatches',
    appended: 'outputAppended',
    replaced: 'outputReplaced',
    removed: 'outputRemoved',
    trashed: 'outputTrashed',
  };
  return toolPublicDetailText(chinese, labels[key]);
}

function collectionLabel(key: string, chinese: boolean): string {
  const labels: Record<string, ToolPublicDetailTextKey> = {
    packages: 'collectionResultPackages',
    requested_packages: 'collectionPreparedPackages',
    dependencies: 'collectionResultDependencies',
    files: 'collectionFiles',
    records: 'collectionRecords',
    results: 'collectionResults',
    items: 'collectionItems',
    ligands: 'collectionLigands',
    artifacts: 'collectionArtifacts',
    sources: 'collectionSources',
    environments: 'collectionEnvironments',
    providers: 'collectionProviders',
    jobs: 'collectionJobs',
    grants: 'collectionGrants',
    memories: 'collectionMemories',
    matches: 'collectionMatches',
    trashed: 'collectionTrashed',
  };
  return toolPublicDetailText(chinese, labels[key] ?? 'collectionDetails');
}
