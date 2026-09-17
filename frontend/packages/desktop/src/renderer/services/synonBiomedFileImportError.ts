export type SynonBiomedFileImportFailureKind =
  | 'permission'
  | 'not_found'
  | 'not_a_directory'
  | 'not_a_file'
  | 'too_large'
  | 'outside_roots'
  | 'connection'
  | 'unknown';

const knownKinds = new Set<SynonBiomedFileImportFailureKind>([
  'permission',
  'not_found',
  'not_a_directory',
  'not_a_file',
  'too_large',
  'outside_roots',
  'connection',
  'unknown',
]);

export class SynonBiomedFileImportError extends Error {
  readonly status: number;
  readonly kind: SynonBiomedFileImportFailureKind;

  constructor(status: number, kind: SynonBiomedFileImportFailureKind) {
    super(`Synon Biomed file import failed (${status}) [${kind}]`);
    this.name = 'SynonBiomedFileImportError';
    this.status = status;
    this.kind = kind;
  }
}

export const isSynonBiomedFileImportError = (error: unknown): error is SynonBiomedFileImportError =>
  error instanceof SynonBiomedFileImportError ||
  Boolean(
    error &&
    typeof error === 'object' &&
    'name' in error &&
    error.name === 'SynonBiomedFileImportError' &&
    'status' in error &&
    typeof error.status === 'number' &&
    'kind' in error &&
    typeof error.kind === 'string' &&
    knownKinds.has(error.kind as SynonBiomedFileImportFailureKind)
  );

export async function readSynonBiomedFileImportResponse(response: Response): Promise<unknown> {
  const text = await response.text();
  let payload: unknown;
  if (text) {
    try {
      payload = JSON.parse(text) as unknown;
    } catch {
      payload = null;
    }
  }
  if (!response.ok) {
    throw new SynonBiomedFileImportError(response.status, importFailureKind(response.status, payload));
  }
  return payload;
}

function importFailureKind(status: number, payload: unknown): SynonBiomedFileImportFailureKind {
  if (payload && typeof payload === 'object' && !Array.isArray(payload)) {
    const record = payload as Record<string, unknown>;
    const candidate = record.remoteKind ?? record.kind ?? record.code;
    if (typeof candidate === 'string' && knownKinds.has(candidate as SynonBiomedFileImportFailureKind)) {
      return candidate as SynonBiomedFileImportFailureKind;
    }
  }
  if (status === 401 || status === 403) return 'permission';
  if (status === 404) return 'not_found';
  if (status === 413) return 'too_large';
  if (status === 502 || status === 503 || status === 504) return 'connection';
  return 'unknown';
}
