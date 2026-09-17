import { toolPublicDetailText } from '@/renderer/services/i18n/toolPublicDetailLocale';
import { formatBytes } from './detailFormatting';

const secretParameter = /api[_-]?key|auth|credential|password|secret|signature|token/iu;

/** Public sources are HTTP links, never credentials or executable schemes. */
export function publicSourceUrl(value: unknown): string | null {
  if (typeof value !== 'string' || value.length > 8192) return null;
  try {
    const url = new URL(value);
    if (!['https:', 'http:'].includes(url.protocol)) return null;
    url.username = '';
    url.password = '';
    for (const key of url.searchParams.keys()) {
      if (secretParameter.test(key)) url.searchParams.set(key, '[redacted]');
    }
    // Anchors are not resource identity and can contain signed/private values.
    url.hash = '';
    return url.toString();
  } catch {
    return null;
  }
}

function record(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

function receiptRecord(value: unknown, depth = 0): Record<string, unknown> | null {
  if (depth > 4) return null;
  if (typeof value === 'string') {
    try {
      return receiptRecord(JSON.parse(value), depth + 1);
    } catch {
      return null;
    }
  }
  if (!record(value)) return null;
  if (
    ['requested_url', 'url', 'bytes_read', 'available', 'complete', 'partial', 'truncated'].some((key) => key in value)
  )
    return value;
  return receiptRecord(value.result, depth + 1) ?? receiptRecord(value.data, depth + 1);
}

export function publicReceiptScalar(key: string, value: unknown): unknown {
  if (['url', 'source_url', 'requested_url'].includes(key)) return publicSourceUrl(value);
  if (key === 'bytes_read') {
    return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? formatBytes(value) : null;
  }
  if (['last_modified', 'response_date', 'retrieved_at'].includes(key) && typeof value === 'string') {
    const time = Date.parse(value);
    return Number.isFinite(time)
      ? new Date(time)
          .toISOString()
          .replace('T', ' ')
          .replace(/\.\d{3}Z$/u, ' UTC')
      : value;
  }
  return value;
}

export function retrievalReceiptPresentation(input: unknown, output: unknown, chinese: boolean) {
  const receipt = receiptRecord(output);
  const requested = record(input) ? publicSourceUrl(input.url) : null;
  const receiptRequest = publicSourceUrl(receipt?.requested_url);
  const returned = publicSourceUrl(receipt?.url ?? receipt?.source_url);
  // Only an explicit request identity can contradict input. A different final
  // URL alone may be a legitimate redirect, and must not imply a failure.
  const mismatch = !!(requested && receiptRequest && requested !== receiptRequest);
  const sourceChanged = !mismatch && !!(requested && returned && requested !== returned);
  const bytes = receipt?.bytes_read;
  const size = typeof bytes === 'number' && Number.isSafeInteger(bytes) && bytes >= 0 ? formatBytes(bytes) : null;
  const partial = receipt?.partial === true || receipt?.truncated === true || receipt?.complete === false;
  const summary = mismatch
    ? toolPublicDetailText(chinese, 'sourceMismatch')
    : receipt?.available === false
      ? toolPublicDetailText(chinese, 'fullTextUnavailable')
      : partial
        ? [toolPublicDetailText(chinese, 'partialContent'), size].filter(Boolean).join(' · ')
        : size !== null
          ? toolPublicDetailText(chinese, 'readSize', { size })
          : null;
  return {
    summary,
    notices: mismatch
      ? [toolPublicDetailText(chinese, 'sourceMismatchNote')]
      : sourceChanged
        ? [toolPublicDetailText(chinese, 'sourceChangedNote')]
        : [],
  };
}
