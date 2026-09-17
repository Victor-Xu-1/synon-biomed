import { normalizeSynonBiomedAskUserQuestions, type SynonBiomedAskUserQuestion } from './runtimeOperationsModel';

export type SynonBiomedAskUserHistory = {
  status: 'pending' | 'answered' | 'deferred' | 'discussed' | 'cancelled' | 'unavailable';
  answer: string | null;
};

export function normalizeSynonBiomedAskUserToolInput(value: unknown): SynonBiomedAskUserQuestion | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  const questions = Array.isArray(record.questions) ? record.questions : [record];
  return normalizeSynonBiomedAskUserQuestions(questions)[0] ?? null;
}

export function normalizeSynonBiomedAskUserHistory(question: string, output: unknown): SynonBiomedAskUserHistory {
  const text = typeof output === 'string' ? output.trim() : stringify(output);
  if (!text) return { status: 'pending', answer: null };
  // Read-only compatibility for facts persisted before the v1 AskUser result
  // contract. New writes never use these phrases.
  if (/^User cancelled/i.test(text)) return { status: 'cancelled', answer: null };
  if (/^The user wants to discuss/i.test(text)) return { status: 'discussed', answer: null };

  try {
    const parsed = JSON.parse(text) as unknown;
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return { status: 'deferred', answer: null };
    }
    const record = parsed as Record<string, unknown>;
    const version = record.version;
    if (version !== 1 && version !== undefined) return { status: 'unavailable', answer: null };
    if (
      record.status === 'awaiting_user_response' &&
      record.action === undefined &&
      hasOnlyKeys(record, ['version', 'status'])
    ) {
      return { status: 'pending', answer: null };
    }
    if (
      record.status === 'answered' &&
      record.action === (version === 1 ? 'answer' : undefined) &&
      record.answers &&
      typeof record.answers === 'object' &&
      !Array.isArray(record.answers) &&
      hasOnlyKeys(record, ['version', 'status', 'action', 'answers'])
    ) {
      const answer = (record.answers as Record<string, unknown>)[question];
      if (typeof answer !== 'string' || !answer.trim()) return { status: 'unavailable', answer: null };
      return { status: 'answered', answer: answer.trim() };
    }
    if (version === undefined) {
      return { status: 'deferred', answer: null };
    }
    if (version !== 1) return { status: 'unavailable', answer: null };
    if (
      record.status === 'deferred' &&
      record.action === 'decide_for_me' &&
      hasOnlyKeys(record, ['version', 'status', 'action'])
    ) {
      return { status: 'deferred', answer: null };
    }
    if (
      record.status === 'discussed' &&
      record.action === 'discuss' &&
      typeof record.message === 'string' &&
      record.message.trim() &&
      hasOnlyKeys(record, ['version', 'status', 'action', 'message'])
    ) {
      return { status: 'discussed', answer: null };
    }
    if (
      record.status === 'cancelled' &&
      record.action === 'cancel' &&
      hasOnlyKeys(record, ['version', 'status', 'action'])
    ) {
      return { status: 'cancelled', answer: null };
    }
  } catch {
    return { status: 'deferred', answer: null };
  }
  return { status: 'unavailable', answer: null };
}

export function isPendingSynonBiomedAskUserHistory(input: unknown, output: unknown): boolean {
  const question = normalizeSynonBiomedAskUserToolInput(input);
  return question !== null && normalizeSynonBiomedAskUserHistory(question.question, output).status === 'pending';
}

function hasOnlyKeys(record: Record<string, unknown>, allowed: string[]): boolean {
  const allowedKeys = new Set(allowed);
  return Object.keys(record).every((key) => allowedKeys.has(key));
}

function stringify(value: unknown): string {
  if (value == null) return '';
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}
