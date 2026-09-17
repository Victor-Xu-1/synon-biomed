import { redactErrorText as redactCommonErrorText, safeErrorCode } from '@/common/utils/errorRedaction';
import {
  buildRawErrorSummary,
  redactErrorText as redactAcpErrorText,
} from '@/renderer/pages/conversation/platforms/acp/errorDiagnostics';
import { describe, expect, it } from 'vitest';

const syntheticCommand =
  'provider --api-key synthetic-secret --token "synthetic token" --authorization=synthetic-auth ark-12345678-synthetic-provider-key';

describe('shared error redaction', () => {
  it('redacts command-line credentials and provider-prefixed keys', () => {
    const redacted = redactCommonErrorText(syntheticCommand);

    expect(redacted).toContain('--api-key [REDACTED]');
    expect(redacted).toContain('--token [REDACTED]');
    expect(redacted).toContain('--authorization=[REDACTED]');
    expect(redacted).toContain('[REDACTED_KEY]');
    expect(redacted).not.toContain('synthetic-secret');
    expect(redacted).not.toContain('synthetic token');
    expect(redacted).not.toContain('synthetic-auth');
    expect(redacted).not.toContain('ark-12345678-synthetic-provider-key');
    expect(redactCommonErrorText('model ark-code-latest unavailable')).toBe('model ark-code-latest unavailable');
  });

  it('does not consume a following option when a sensitive flag has no value', () => {
    expect(redactCommonErrorText('provider --token --next safe')).toBe('provider --token --next safe');
  });

  it('preserves authorization schemes while redacting header and quoted assignment values', () => {
    expect(redactCommonErrorText('Authorization: Bearer aaa.bbb.ccc-DDD')).toBe('Authorization: Bearer [REDACTED]');
    expect(redactCommonErrorText('Authorization: Basic synthetic-credential')).toBe('Authorization: [REDACTED]');
    expect(redactCommonErrorText('authorization=synthetic-auth')).toBe('authorization=[REDACTED]');
    expect(redactCommonErrorText('"authorization": "synthetic auth value"')).toBe('"authorization": "[REDACTED]"');
  });

  it('keeps the ACP compatibility export on the common implementation', () => {
    expect(redactAcpErrorText(syntheticCommand)).toBe(redactCommonErrorText(syntheticCommand));
  });

  it('keeps only bounded non-secret diagnostic codes', () => {
    expect(safeErrorCode('provider_timeout')).toBe('provider_timeout');
    expect(safeErrorCode('sk-1234567890abcdef')).toBeUndefined();
    expect(safeErrorCode('ark-12345678-synthetic-provider-key')).toBeUndefined();
    expect(safeErrorCode('provider timeout')).toBeUndefined();
  });

  it('applies the shared rules to telemetry summaries and stacks', () => {
    const error = new Error(syntheticCommand);
    const summary = buildRawErrorSummary(error);
    const serialized = JSON.stringify(summary);

    expect(summary?.message).toBe(redactCommonErrorText(syntheticCommand));
    expect(serialized).not.toContain('synthetic-secret');
    expect(serialized).not.toContain('synthetic token');
    expect(serialized).not.toContain('synthetic-auth');
    expect(serialized).not.toContain('ark-12345678-synthetic-provider-key');
  });

  it('drops secret-shaped error codes from telemetry summaries', () => {
    const error = Object.assign(new Error('provider failed'), { code: 'sk-1234567890abcdef' });
    const summary = buildRawErrorSummary(error);

    expect(summary).toEqual(expect.objectContaining({ name: 'Error', message: 'provider failed' }));
    expect(summary).not.toHaveProperty('code');
  });
});
