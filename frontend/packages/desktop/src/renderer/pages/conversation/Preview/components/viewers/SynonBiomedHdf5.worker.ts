/// <reference lib="webworker" />

import h5wasm from 'h5wasm';
import type { Dataset, Datatype, Entity, Group } from 'h5wasm';
import type {
  AnnDataDistribution,
  AnnDataEmbedding,
  AnnDataOverview,
  Hdf5AttributeSummary,
  Hdf5DatasetDetail,
  Hdf5TreeNode,
  Hdf5WorkerRequest,
  Hdf5WorkerResponse,
} from './synonBiomedHdf5Model';

const MAX_TREE_NODES = 10_000;
const MAX_TREE_DEPTH = 32;
const MAX_ATTRIBUTE_CHARS = 240;
const MAX_PREVIEW_VALUES = 400;
const MAX_EMBEDDING_POINTS = 12_000;
const MAX_DERIVED_PCA_CELLS = 6_000;
const MAX_DERIVED_PCA_FEATURES = 32;
const MAX_DISTRIBUTION_ITEMS = 10;
const UNLABELED_CATEGORY = '\0synon-unlabeled';
const OTHER_CATEGORY = '\0synon-other';

const OBS_DISTRIBUTIONS = [
  ['cell_type_simulated', 'cellType'],
  ['cell_type', 'cellType'],
  ['responder', 'responder'],
  ['response', 'response'],
  ['RECIST', 'recist'],
  ['pathology', 'pathology'],
  ['sex', 'sex'],
] as const;

let activeFile: InstanceType<typeof h5wasm.File> | null = null;
let activeFilename: string | null = null;
type InspectableEntity = Group | Dataset | Datatype;

self.addEventListener('message', (event: MessageEvent<Hdf5WorkerRequest>) => {
  void handleRequest(event.data).catch(() => {
    post({
      type: 'error',
      ...(event.data.type === 'read'
        ? { requestId: event.data.requestId, code: 'read-failed' as const }
        : { code: 'parse-failed' as const }),
    });
  });
});

async function handleRequest(request: Hdf5WorkerRequest): Promise<void> {
  if (request.type === 'open') {
    await openFile(request.filename, request.buffer);
    return;
  }

  if (!activeFile) {
    post({ type: 'error', requestId: request.requestId, code: 'not-ready' });
    return;
  }

  try {
    const entity = activeFile.get(request.path);
    if (!isInspectableEntity(entity) || entity.type !== 'Dataset') {
      post({ type: 'error', requestId: request.requestId, code: 'not-dataset' });
      return;
    }
    post({ type: 'detail', requestId: request.requestId, detail: readDataset(entity as Dataset) });
  } catch {
    post({ type: 'error', requestId: request.requestId, code: 'read-failed' });
  }
}

async function openFile(filename: string, buffer: ArrayBuffer): Promise<void> {
  const { FS } = await h5wasm.ready;
  if (activeFile) activeFile.close();
  if (activeFilename) {
    try {
      FS.unlink(activeFilename);
    } catch {
      // The worker may have been recreated before the previous virtual file was registered.
    }
  }

  activeFilename = `/synon-${crypto.randomUUID()}.h5`;
  FS.writeFile(activeFilename, new Uint8Array(buffer));
  activeFile = new h5wasm.File(activeFilename, 'r');

  const context = { count: 0, truncated: false, visited: new Set<string>() };
  const root = buildTree(activeFile, filename, 0, context);
  const overview = readAnnDataOverview(activeFile, root);
  post({ type: 'ready', root, nodeCount: context.count, truncated: context.truncated, overview });
}

function readAnnDataOverview(file: Group, root: Hdf5TreeNode): AnnDataOverview | undefined {
  const matrix = file.get('/X');
  if (!isInspectableEntity(matrix) || matrix.type !== 'Dataset') return undefined;
  const shape = (matrix as Dataset).shape;
  if (!shape || shape.length < 2 || !file.get('/obs') || !file.get('/var')) return undefined;

  const distributions: AnnDataDistribution[] = [];
  const usedLabels = new Set<string>();
  for (const [key, labelKey] of OBS_DISTRIBUTIONS) {
    if (usedLabels.has(labelKey)) continue;
    const distribution = readDistribution(file, `/obs/${key}`, key, labelKey);
    if (!distribution) continue;
    distributions.push(distribution);
    usedLabels.add(labelKey);
    if (distributions.length >= 5) break;
  }

  return {
    cells: shape[0] ?? 0,
    features: shape[1] ?? 0,
    encodingVersion: findAttribute(root, 'encoding-version'),
    distributions,
    embedding: readEmbedding(file) || derivePcaEmbedding(matrix as Dataset),
  };
}

function readDistribution(
  file: Group,
  path: string,
  key: string,
  labelKey: NonNullable<AnnDataDistribution['labelKey']>
): AnnDataDistribution | null {
  const entity = file.get(path);
  if (!isInspectableEntity(entity)) return null;

  if (entity.type === 'Dataset') {
    return aggregateValues(key, labelKey, toPrimitiveArray((entity as Dataset).value));
  }
  if (entity.type !== 'Group') return null;

  const codes = (entity as Group).get('codes');
  const categories = (entity as Group).get('categories');
  if (!isInspectableEntity(codes) || codes.type !== 'Dataset') return null;
  if (!isInspectableEntity(categories) || categories.type !== 'Dataset') return null;

  const categoryValues = toPrimitiveArray((categories as Dataset).value).map(String);
  const codeValues = toPrimitiveArray((codes as Dataset).value);
  const counts = new Map<string, number>();
  for (const rawCode of codeValues) {
    const code = Number(rawCode);
    if (!Number.isInteger(code) || code < 0 || code >= categoryValues.length) continue;
    const value = categoryValues[code] || UNLABELED_CATEGORY;
    counts.set(value, (counts.get(value) || 0) + 1);
  }
  return finalizeDistribution(key, labelKey, counts);
}

function aggregateValues(
  key: string,
  labelKey: NonNullable<AnnDataDistribution['labelKey']>,
  values: Array<string | number | boolean | bigint>
) {
  const counts = new Map<string, number>();
  for (const value of values) {
    const normalized = String(value).trim() || UNLABELED_CATEGORY;
    counts.set(normalized, (counts.get(normalized) || 0) + 1);
  }
  return finalizeDistribution(key, labelKey, counts);
}

function finalizeDistribution(
  key: string,
  labelKey: NonNullable<AnnDataDistribution['labelKey']>,
  counts: Map<string, number>
): AnnDataDistribution | null {
  if (counts.size === 0 || counts.size > 200) return null;
  const sorted = [...counts.entries()].toSorted((left, right) => right[1] - left[1]);
  const visible = sorted.slice(0, MAX_DISTRIBUTION_ITEMS);
  const hiddenCount = sorted.slice(MAX_DISTRIBUTION_ITEMS).reduce((total, item) => total + item[1], 0);
  if (hiddenCount > 0) visible.push([OTHER_CATEGORY, hiddenCount]);
  return {
    key,
    label: key,
    labelKey,
    total: sorted.reduce((total, item) => total + item[1], 0),
    items: visible.map(([itemLabel, count]) => ({
      label: itemLabel.startsWith('\0synon-') ? '' : itemLabel,
      labelKey: itemLabel === UNLABELED_CATEGORY ? 'unlabeled' : itemLabel === OTHER_CATEGORY ? 'other' : undefined,
      count,
    })),
  };
}

function readEmbedding(file: Group): AnnDataEmbedding | undefined {
  const obsm = file.get('/obsm');
  if (!isInspectableEntity(obsm) || obsm.type !== 'Group') return undefined;
  const preferredKeys = ['X_umap', 'X_tsne', 'X_pca'];
  const key = preferredKeys.find((candidate) => (obsm as Group).keys().includes(candidate));
  if (!key) return undefined;
  const dataset = (obsm as Group).get(key);
  if (!isInspectableEntity(dataset) || dataset.type !== 'Dataset') return undefined;
  const shape = (dataset as Dataset).shape;
  if (!shape || shape.length !== 2 || shape[1] < 2 || shape[0] < 1) return undefined;

  const stride = Math.max(1, Math.ceil(shape[0] / MAX_EMBEDDING_POINTS));
  const sampled = toNumberArray(
    (dataset as Dataset).slice([
      [0, shape[0], stride],
      [0, 2],
    ])
  );
  const points: [number, number][] = [];
  for (let index = 0; index + 1 < sampled.length; index += 2) {
    const x = sampled[index];
    const y = sampled[index + 1];
    if (Number.isFinite(x) && Number.isFinite(y)) points.push([x, y]);
  }
  if (points.length === 0) return undefined;
  return {
    key,
    label: key === 'X_umap' ? 'UMAP' : key === 'X_tsne' ? 't-SNE' : 'PCA',
    source: 'stored',
    totalPoints: shape[0],
    sampledPoints: points.length,
    points,
  };
}

function derivePcaEmbedding(dataset: Dataset): AnnDataEmbedding | undefined {
  const shape = dataset.shape;
  if (!shape || shape.length !== 2 || shape[0] < 2 || shape[1] < 2) return undefined;

  const rowStride = Math.max(1, Math.ceil(shape[0] / MAX_DERIVED_PCA_CELLS));
  const columnStride = Math.max(1, Math.ceil(shape[1] / MAX_DERIVED_PCA_FEATURES));
  const rowCount = Math.ceil(shape[0] / rowStride);
  const columnCount = Math.ceil(shape[1] / columnStride);
  const values = toNumberArray(
    dataset.slice([
      [0, shape[0], rowStride],
      [0, shape[1], columnStride],
    ])
  );
  if (values.length !== rowCount * columnCount) return undefined;

  const means = new Float64Array(columnCount);
  const scales = new Float64Array(columnCount);
  for (let row = 0; row < rowCount; row += 1) {
    for (let column = 0; column < columnCount; column += 1) {
      means[column] += values[row * columnCount + column] || 0;
    }
  }
  for (let column = 0; column < columnCount; column += 1) means[column] /= rowCount;
  for (let row = 0; row < rowCount; row += 1) {
    for (let column = 0; column < columnCount; column += 1) {
      const centered = (values[row * columnCount + column] || 0) - means[column];
      scales[column] += centered * centered;
    }
  }
  for (let column = 0; column < columnCount; column += 1) {
    scales[column] = Math.sqrt(scales[column] / Math.max(1, rowCount - 1)) || 1;
  }

  const covariance = new Float64Array(columnCount * columnCount);
  for (let row = 0; row < rowCount; row += 1) {
    for (let left = 0; left < columnCount; left += 1) {
      const leftValue = ((values[row * columnCount + left] || 0) - means[left]) / scales[left];
      for (let right = left; right < columnCount; right += 1) {
        const rightValue = ((values[row * columnCount + right] || 0) - means[right]) / scales[right];
        covariance[left * columnCount + right] += leftValue * rightValue;
      }
    }
  }
  for (let left = 0; left < columnCount; left += 1) {
    for (let right = left; right < columnCount; right += 1) {
      const value = covariance[left * columnCount + right] / Math.max(1, rowCount - 1);
      covariance[left * columnCount + right] = value;
      covariance[right * columnCount + left] = value;
    }
  }

  const first = principalVector(covariance, columnCount);
  const second = principalVector(covariance, columnCount, first);
  const points: [number, number][] = [];
  for (let row = 0; row < rowCount; row += 1) {
    let x = 0;
    let y = 0;
    for (let column = 0; column < columnCount; column += 1) {
      const value = ((values[row * columnCount + column] || 0) - means[column]) / scales[column];
      x += value * first[column];
      y += value * second[column];
    }
    if (Number.isFinite(x) && Number.isFinite(y)) points.push([x, y]);
  }
  if (points.length < 2) return undefined;
  return {
    key: 'derived_pca',
    label: 'PCA',
    labelKey: 'pcaPreview',
    source: 'derived',
    totalPoints: shape[0],
    sampledPoints: points.length,
    points,
  };
}

function principalVector(covariance: Float64Array, size: number, orthogonalTo?: Float64Array): Float64Array {
  let vector = Float64Array.from({ length: size }, (_, index) => 1 + ((index * 17) % 11) / 11);
  normalizeVector(vector);
  for (let iteration = 0; iteration < 28; iteration += 1) {
    const next = new Float64Array(size);
    for (let row = 0; row < size; row += 1) {
      for (let column = 0; column < size; column += 1) {
        next[row] += covariance[row * size + column] * vector[column];
      }
    }
    if (orthogonalTo) {
      let projection = 0;
      for (let index = 0; index < size; index += 1) projection += next[index] * orthogonalTo[index];
      for (let index = 0; index < size; index += 1) next[index] -= projection * orthogonalTo[index];
    }
    normalizeVector(next);
    vector = next;
  }
  return vector;
}

function normalizeVector(vector: Float64Array): void {
  let magnitude = 0;
  for (const value of vector) magnitude += value * value;
  magnitude = Math.sqrt(magnitude) || 1;
  for (let index = 0; index < vector.length; index += 1) vector[index] /= magnitude;
}

function toPrimitiveArray(value: unknown): Array<string | number | boolean | bigint> {
  if (ArrayBuffer.isView(value)) return Array.from(value as unknown as ArrayLike<number | bigint>);
  if (!Array.isArray(value)) return value == null ? [] : [String(value)];
  return value
    .flat(Number.POSITIVE_INFINITY)
    .filter((item): item is string | number | boolean | bigint =>
      ['string', 'number', 'boolean', 'bigint'].includes(typeof item)
    );
}

function toNumberArray(value: unknown): number[] {
  return toPrimitiveArray(value).map(Number);
}

function findAttribute(node: Hdf5TreeNode, name: string): string | undefined {
  const value = node.attributes.find((attribute) => attribute.name === name)?.value;
  return value?.replace(/^"|"$/g, '');
}

function buildTree(
  entity: InspectableEntity,
  displayName: string,
  depth: number,
  context: { count: number; truncated: boolean; visited: Set<string> }
): Hdf5TreeNode {
  context.count += 1;
  const node = summarizeEntity(entity, displayName);
  if (entity.type !== 'Group' || depth >= MAX_TREE_DEPTH) return node;
  if (context.visited.has(entity.path)) return node;
  context.visited.add(entity.path);

  const children: Hdf5TreeNode[] = [];
  for (const name of (entity as Group).keys()) {
    const child = (entity as Group).get(name);
    if (!isInspectableEntity(child)) continue;
    if (context.count >= MAX_TREE_NODES) {
      context.truncated = true;
      break;
    }
    children.push(buildTree(child, name, depth + 1, context));
  }
  node.children = children;
  return node;
}

function summarizeEntity(entity: InspectableEntity, name: string): Hdf5TreeNode {
  const node: Hdf5TreeNode = {
    name,
    path: entity.path,
    kind: resolveKind(entity),
    attributes: readAttributeSummaries(entity),
  };
  if (entity.type === 'Dataset') {
    const dataset = entity as Dataset;
    node.shape = dataset.shape;
    node.dtype = formatDtype(dataset.dtype);
  }
  return node;
}

function readDataset(dataset: Dataset): Hdf5DatasetDetail {
  const shape = dataset.shape;
  let preview: unknown;
  let truncated = false;

  if (!shape || shape.length === 0) {
    preview = normalizeValue(dataset.json_value);
  } else {
    const ranges = buildPreviewRanges(shape);
    const previewSize = ranges.reduce((total, range) => total * Math.max(1, Number(range[1]) || 1), 1);
    truncated = shape.some((dimension, index) => dimension > (Number(ranges[index]?.[1]) || dimension));
    preview = normalizeValue(dataset.slice(ranges));
    if (previewSize > MAX_PREVIEW_VALUES) truncated = true;
  }

  return {
    path: dataset.path,
    shape,
    dtype: formatDtype(dataset.dtype),
    preview,
    truncated,
  };
}

function buildPreviewRanges(shape: number[]): [number, number][] {
  let remaining = MAX_PREVIEW_VALUES;
  return shape.map((dimension, index) => {
    const dimensionsLeft = shape.length - index;
    const fairShare = Math.max(1, Math.floor(remaining ** (1 / dimensionsLeft)));
    const take = Math.min(dimension, fairShare);
    remaining = Math.max(1, Math.floor(remaining / Math.max(1, take)));
    return [0, take];
  });
}

function readAttributeSummaries(entity: InspectableEntity): Hdf5AttributeSummary[] {
  return Object.entries(entity.attrs)
    .slice(0, 30)
    .map(([name, attribute]) => ({ name, value: truncateString(safeJson(attribute.json_value), MAX_ATTRIBUTE_CHARS) }));
}

function resolveKind(entity: InspectableEntity): Hdf5TreeNode['kind'] {
  if (entity.type === 'Group') return 'group';
  if (entity.type === 'Dataset') return 'dataset';
  if (entity.type === 'Datatype') return 'datatype';
  return 'link';
}

function isInspectableEntity(entity: Entity | null): entity is InspectableEntity {
  return Boolean(entity && 'type' in entity && 'path' in entity);
}

function formatDtype(dtype: unknown): string {
  return typeof dtype === 'string' ? dtype : truncateString(safeJson(dtype), MAX_ATTRIBUTE_CHARS);
}

function normalizeValue(value: unknown): unknown {
  if (typeof value === 'bigint') return value.toString();
  if (ArrayBuffer.isView(value)) return Array.from(value as unknown as ArrayLike<number | bigint>).map(normalizeValue);
  if (Array.isArray(value)) return value.slice(0, MAX_PREVIEW_VALUES).map(normalizeValue);
  if (value && typeof value === 'object') {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>)
        .slice(0, MAX_PREVIEW_VALUES)
        .map(([key, item]) => [key, normalizeValue(item)])
    );
  }
  return value;
}

function safeJson(value: unknown): string {
  try {
    return JSON.stringify(normalizeValue(value));
  } catch {
    return String(value);
  }
}

function truncateString(value: string, length: number): string {
  return value.length > length ? `${value.slice(0, length)}...` : value;
}

function post(message: Hdf5WorkerResponse): void {
  // WorkerGlobalScope.postMessage has no targetOrigin; the window-only lint rule is not applicable here.
  // eslint-disable-next-line unicorn/require-post-message-target-origin
  self.postMessage(message);
}
