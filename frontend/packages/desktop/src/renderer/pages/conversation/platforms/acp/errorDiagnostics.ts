/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { isBackendHttpError } from '@/common/adapter/httpBridge';
import { redactErrorText, safeErrorCode } from '@/common/utils/errorRedaction';

export { redactErrorText };

/** Redacted summary of an original error, safe to attach to a Sentry report. */
export type RawErrorSummary = {
  name?: string;
  message?: string;
  code?: string;
  status?: number;
  stack?: string;
};

const MAX_MESSAGE_LENGTH = 500;
const MAX_STACK_LENGTH = 1000;

const truncate = (text: string, max: number): string => (text.length > max ? `${text.slice(0, max - 1)}…` : text);

const getStringProp = (value: object, key: string): string | undefined => {
  const candidate = (value as Record<string, unknown>)[key];
  return typeof candidate === 'string' && candidate.length > 0 ? candidate : undefined;
};

/**
 * Build a redacted, size-bounded summary of an original error for telemetry.
 * Returns undefined for empty/nullish values so callers can omit the field.
 */
export const buildRawErrorSummary = (error: unknown): RawErrorSummary | undefined => {
  if (error === undefined || error === null) return undefined;

  if (isBackendHttpError(error)) {
    const code = error.code ? safeErrorCode(error.code) : undefined;
    return {
      name: error.name,
      status: error.status,
      ...(code ? { code } : {}),
      ...(error.backendMessage ? { message: truncate(redactErrorText(error.backendMessage), MAX_MESSAGE_LENGTH) } : {}),
    };
  }

  if (error instanceof Error) {
    const rawCode = getStringProp(error, 'code');
    const code = rawCode ? safeErrorCode(rawCode) : undefined;
    return {
      name: error.name,
      ...(error.message ? { message: truncate(redactErrorText(error.message), MAX_MESSAGE_LENGTH) } : {}),
      ...(code ? { code } : {}),
      ...(error.stack ? { stack: truncate(redactErrorText(error.stack), MAX_STACK_LENGTH) } : {}),
    };
  }

  const text = String(error);
  if (!text) return undefined;
  return { message: truncate(redactErrorText(text), MAX_MESSAGE_LENGTH) };
};
