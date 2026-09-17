const RDKIT_WORKER_PATH = 'rdkit/rdkit-worker.js';
const RDKIT_PROTOCOL_VERSION = 1;
const RDKIT_REQUEST_TIMEOUT_MS = 15_000;
const MAX_PENDING_REQUESTS = 128;
const MAX_SMILES_LENGTH = 4096;
const MIN_RENDER_DIMENSION = 16;
const MAX_RENDER_DIMENSION = 1024;
const MAX_SVG_LENGTH = 1024 * 1024;
const MAX_MOLBLOCK_LENGTH = 1024 * 1024;

type RDKitResult = { svg: string; molBlock: string; smiles: string };

type RDKitOperation = 'render_svg' | 'validate_molblock';

type PendingRequest = {
  operation: RDKitOperation;
  worker: Worker;
  timeout: number;
  resolve: (result: RDKitResult | null) => void;
  reject: (error: RDKitRenderError) => void;
};

export type RDKitRenderErrorCode =
  | 'disposed'
  | 'invalid_input'
  | 'protocol_error'
  | 'queue_full'
  | 'runtime_unavailable'
  | 'timeout';

export class RDKitRenderError extends Error {
  readonly code: RDKitRenderErrorCode;

  constructor(code: RDKitRenderErrorCode, message: string) {
    super(message);
    this.name = 'RDKitRenderError';
    this.code = code;
  }
}

type RDKitWorkerResponse =
  | {
      version: 1;
      id: number;
      ok: true;
      svg: string;
      molBlock?: string;
      smiles?: string;
    }
  | {
      version: 1;
      id: number;
      ok: false;
      error: 'invalid_request' | 'invalid_smiles' | 'render_failed' | 'runtime_unavailable';
    };

let worker: Worker | null = null;
let workerCreationFailure: RDKitRenderError | null = null;
let nextRequestId = 1;
const pending = new Map<number, PendingRequest>();

const resolveRendererAsset = (path: string): string => new URL(path, document.baseURI).toString();

function fixedError(code: RDKitRenderErrorCode, message: string): RDKitRenderError {
  return new RDKitRenderError(code, message);
}

function rememberWorkerCreationFailure(): RDKitRenderError {
  if (workerCreationFailure) return workerCreationFailure;
  const failure = fixedError('runtime_unavailable', 'RDKit worker is unavailable');
  workerCreationFailure = failure;
  window.queueMicrotask(() => {
    if (workerCreationFailure === failure) workerCreationFailure = null;
  });
  return failure;
}

function failWorker(target: Worker, error: RDKitRenderError): void {
  if (worker === target) worker = null;
  try {
    target.terminate();
  } catch {
    // The authority is already detached; pending callers still converge below.
  }
  for (const [id, request] of pending) {
    if (request.worker !== target) continue;
    pending.delete(id);
    window.clearTimeout(request.timeout);
    request.reject(error);
  }
}

function isWorkerResponse(value: unknown): value is RDKitWorkerResponse {
  if (!value || typeof value !== 'object') return false;
  const record = value as Record<string, unknown>;
  if (record.version !== RDKIT_PROTOCOL_VERSION || !Number.isSafeInteger(record.id) || Number(record.id) <= 0) {
    return false;
  }
  if (record.ok === true) {
    return (
      typeof record.svg === 'string' &&
      (record.molBlock === undefined || typeof record.molBlock === 'string') &&
      (record.smiles === undefined || typeof record.smiles === 'string')
    );
  }
  return (
    record.ok === false &&
    (record.error === 'invalid_request' ||
      record.error === 'invalid_smiles' ||
      record.error === 'render_failed' ||
      record.error === 'runtime_unavailable')
  );
}

function handleWorkerMessage(target: Worker, event: MessageEvent<unknown>): void {
  if (worker !== target) return;
  const response = event.data;
  if (!isWorkerResponse(response)) {
    failWorker(target, fixedError('protocol_error', 'RDKit worker returned an invalid response'));
    return;
  }
  const request = pending.get(response.id);
  if (!request || request.worker !== target) return;
  if (response.ok === false) {
    if (response.error === 'invalid_smiles' || response.error === 'render_failed') {
      pending.delete(response.id);
      window.clearTimeout(request.timeout);
      request.resolve(null);
      return;
    }
    failWorker(
      target,
      response.error === 'runtime_unavailable'
        ? fixedError('runtime_unavailable', 'RDKit rendering is unavailable')
        : fixedError('protocol_error', 'RDKit worker rejected a valid request')
    );
    return;
  }
  const svg = response.svg.trim();
  const molBlock = response.molBlock?.trim() ?? '';
  const smiles = response.smiles?.trim() ?? '';
  if (
    !svg ||
    svg.length > MAX_SVG_LENGTH ||
    !svg.includes('<svg') ||
    !svg.includes('</svg>') ||
    molBlock.length > MAX_MOLBLOCK_LENGTH ||
    smiles.length > MAX_SMILES_LENGTH
  ) {
    failWorker(target, fixedError('protocol_error', 'RDKit worker returned an invalid SVG'));
    return;
  }
  if (request.operation === 'validate_molblock' && (!molBlock || !smiles)) {
    failWorker(target, fixedError('protocol_error', 'RDKit worker returned incomplete molecule data'));
    return;
  }
  pending.delete(response.id);
  window.clearTimeout(request.timeout);
  request.resolve({ svg, molBlock, smiles });
}

function currentWorker(): Worker {
  if (worker) return worker;
  if (workerCreationFailure) throw workerCreationFailure;
  if (typeof Worker !== 'function') throw rememberWorkerCreationFailure();
  let created: Worker;
  try {
    created = new Worker(resolveRendererAsset(RDKIT_WORKER_PATH), {
      name: 'synon-rdkit',
    });
  } catch {
    throw rememberWorkerCreationFailure();
  }
  workerCreationFailure = null;
  created.addEventListener('message', (event) => handleWorkerMessage(created, event));
  created.addEventListener('error', () =>
    failWorker(created, fixedError('runtime_unavailable', 'RDKit worker failed'))
  );
  created.addEventListener('messageerror', () =>
    failWorker(created, fixedError('protocol_error', 'RDKit worker response failed'))
  );
  worker = created;
  return created;
}

function validateRenderInput(source: string, width: number, height: number, maximumLength: number): string {
  const normalized = source.trim();
  if (!normalized || normalized.length > maximumLength) {
    throw fixedError('invalid_input', 'Invalid molecule input');
  }
  for (const dimension of [width, height]) {
    if (!Number.isInteger(dimension) || dimension < MIN_RENDER_DIMENSION || dimension > MAX_RENDER_DIMENSION) {
      throw fixedError('invalid_input', 'Invalid molecule render dimensions');
    }
  }
  return normalized;
}

function allocateRequestId(): number {
  for (let attempt = 0; attempt <= MAX_PENDING_REQUESTS; attempt += 1) {
    const id = nextRequestId;
    nextRequestId = Number.isSafeInteger(nextRequestId + 1) ? nextRequestId + 1 : 1;
    if (!pending.has(id)) return id;
  }
  throw fixedError('queue_full', 'RDKit worker queue is full');
}

export function renderMoleculeSvg(smiles: string, width: number, height: number): Promise<string | null> {
  return requestRdkit({
    operation: 'render_svg',
    source: smiles,
    width,
    height,
  }).then((result) => result?.svg ?? null);
}

export type RDKitMoleculeResult = RDKitResult;

export function validateAndRenderMolBlock(
  molBlock: string,
  width: number,
  height: number
): Promise<RDKitMoleculeResult | null> {
  return requestRdkit({
    operation: 'validate_molblock',
    source: molBlock,
    width,
    height,
  });
}

function requestRdkit({
  operation,
  source,
  width,
  height,
}: {
  operation: 'render_svg' | 'validate_molblock';
  source: string;
  width: number;
  height: number;
}): Promise<RDKitResult | null> {
  let normalized: string;
  try {
    normalized = validateRenderInput(
      source,
      width,
      height,
      operation === 'render_svg' ? MAX_SMILES_LENGTH : MAX_MOLBLOCK_LENGTH
    );
  } catch (error) {
    return Promise.reject(error);
  }
  if (pending.size >= MAX_PENDING_REQUESTS) {
    return Promise.reject(fixedError('queue_full', 'RDKit worker queue is full'));
  }
  let target: Worker;
  let id: number;
  try {
    target = currentWorker();
    id = allocateRequestId();
  } catch (error) {
    return Promise.reject(
      error instanceof RDKitRenderError ? error : fixedError('runtime_unavailable', 'RDKit worker is unavailable')
    );
  }

  return new Promise<RDKitResult | null>((resolve, reject) => {
    const timeout = window.setTimeout(() => {
      failWorker(target, fixedError('timeout', 'RDKit worker timed out'));
    }, RDKIT_REQUEST_TIMEOUT_MS);
    pending.set(id, { operation, worker: target, timeout, resolve, reject });
    try {
      target.postMessage(
        {
          version: RDKIT_PROTOCOL_VERSION,
          id,
          operation,
          source: normalized,
          width,
          height,
        },
        []
      );
    } catch {
      failWorker(target, fixedError('runtime_unavailable', 'RDKit worker failed'));
    }
  });
}

export function disposeRdkitWorker(): void {
  workerCreationFailure = null;
  if (worker) failWorker(worker, fixedError('disposed', 'RDKit worker was disposed'));
}
