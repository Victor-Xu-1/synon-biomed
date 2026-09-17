export type ScientificPreviewErrorCode =
  | 'empty-content'
  | 'initialize-failed'
  | 'missing-content'
  | 'parse-failed'
  | 'request-failed'
  | 'search-failed'
  | 'too-large'
  | 'unsupported-format';

type ScientificPreviewErrorDetails = {
  limit?: string;
  status?: number;
  filename?: string;
};

export class ScientificPreviewError extends Error {
  readonly code: ScientificPreviewErrorCode;
  readonly details: ScientificPreviewErrorDetails;

  constructor(code: ScientificPreviewErrorCode, details: ScientificPreviewErrorDetails = {}) {
    super(code);
    this.name = 'ScientificPreviewError';
    this.code = code;
    this.details = details;
  }
}

export const resolveScientificPreviewError = (
  error: unknown,
  fallback: ScientificPreviewErrorCode
): ScientificPreviewError => (error instanceof ScientificPreviewError ? error : new ScientificPreviewError(fallback));

export const scientificPreviewErrorKey = (error: ScientificPreviewError): string =>
  error.code === 'request-failed' && error.details.status
    ? 'preview.scientific.errors.requestFailedWithStatus'
    : `preview.scientific.errors.${toCamelCase(error.code)}`;

export const logScientificPreviewError = (
  scope: string,
  error: unknown,
  fallback: ScientificPreviewErrorCode
): void => {
  const resolved = resolveScientificPreviewError(error, fallback);
  console.warn(scope, {
    code: resolved.code,
    errorName: error instanceof Error ? error.name : typeof error,
    status: resolved.details.status,
  });
};

const toCamelCase = (value: ScientificPreviewErrorCode): string =>
  value.replace(/-([a-z])/g, (_match, letter: string) => letter.toUpperCase());
