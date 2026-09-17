/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

const REDACTION_RULES: ReadonlyArray<readonly [RegExp, string]> = [
  [
    /((?:^|\s)--(?:api[_-]?key|access[_-]?token|authorization|cookie|credential|password|passwd|secret|session[_-]?token|token)(?:\s*=\s*|\s+))(?:"[^"\r\n]*"|'[^'\r\n]*'|(?!-{2})[^\s,;]+)/gi,
    '$1[REDACTED]',
  ],
  [/\bsk-[a-zA-Z0-9._-]{16,}\b/g, '[REDACTED_KEY]'],
  [/\bark-(?=[a-zA-Z0-9._-]{20,}\b)(?=[a-zA-Z0-9._-]*\d)[a-zA-Z0-9._-]+\b/g, '[REDACTED_KEY]'],
  [/AIza[a-zA-Z0-9_-]{16,}/g, '[REDACTED_KEY]'],
  [/\b(?:AKIA|ASIA)[A-Z0-9]{16}\b/g, '[REDACTED_KEY]'],
  [/\beyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+/g, '[REDACTED]'],
  [/(Bearer\s+)[a-zA-Z0-9._\-+/=]+/gi, '$1[REDACTED]'],
  [/(\b[a-z][a-z0-9+.-]*:\/\/[^:/\s@]+:)[^@\s/]+(@)/gi, '$1[REDACTED]$2'],
  [
    /(["']?(?:api[_-]?key|access[_-]?token|authorization|cookie|credential|password|passwd|secret|session[_-]?token|token)["']?\s*[=:]\s*)(["'])[^"'\r\n]*(\2)/gi,
    '$1$2[REDACTED]$3',
  ],
  [/(authorization\s*=\s*)[^\s,;}]+/gi, '$1[REDACTED]'],
  [/(authorization\s*:)(?![ \t]*Bearer(?:[ \t]|$))([ \t]*)[^\r\n]+/gi, '$1$2[REDACTED]'],
  [
    /(["']?(?:api[_-]?key|access[_-]?token|cookie|credential|password|passwd|secret|session[_-]?token|token)["']?\s*[=:]\s*)[^\s"',}]+/gi,
    '$1[REDACTED]',
  ],
  [/\b[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}\b/g, '[email]'],
  [/(\/(?:Users|home)\/)[^/\s]+/g, '$1[user]'],
  [/([A-Za-z]:\\Users\\)[^\\/\s]+/g, '$1[user]'],
];

const MAX_ERROR_DIAGNOSTIC_LENGTH = 500;
const SAFE_ERROR_CODE = /^[A-Za-z][A-Za-z0-9_.:/-]{0,63}$/;

export const redactErrorText = (text: string): string => {
  let redacted = text;
  for (const [pattern, replacement] of REDACTION_RULES) redacted = redacted.replace(pattern, replacement);
  return redacted;
};

export const safeErrorCode = (value: string): string | undefined => {
  const normalized = value.trim();
  if (!SAFE_ERROR_CODE.test(normalized)) return undefined;
  return redactErrorText(normalized) === normalized ? normalized : undefined;
};

export const safeErrorDiagnostic = (error: unknown): string => {
  const text = error instanceof Error ? error.message : typeof error === 'string' ? error : String(error ?? 'unknown');
  return redactErrorText(text).slice(0, MAX_ERROR_DIAGNOSTIC_LENGTH);
};
