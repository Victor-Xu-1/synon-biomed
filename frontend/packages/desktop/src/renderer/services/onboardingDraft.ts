import type { OnboardingStep } from '@/renderer/pages/onboarding/onboardingModel';

const DRAFT_VERSION = 1;
const DRAFT_PREFIX = 'synonbiomed.onboarding.draft.v1';
const MAX_TEXT_LENGTH = 16_384;
const MAX_SELECTION_ENTRIES = 512;

export type OnboardingDraft = {
  version: 1;
  step: OnboardingStep;
  networkEnabled: Record<string, boolean>;
  connectorEnabled: Record<string, boolean>;
  skillEnabled: Record<string, boolean>;
  scientificRuntimeEnabled: Record<string, boolean>;
  profileSummary: string;
  attachmentNames: string[];
  selectedTask: string;
  customTask: string;
};

export function loadOnboardingDraft(ownerId: string): OnboardingDraft | null {
  const storage = browserStorage();
  const key = draftKey(ownerId);
  if (!storage || !key) return null;
  const raw = storage.getItem(key);
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as unknown;
    const draft = parseDraft(parsed);
    if (draft) return draft;
  } catch {
    // Invalid or partially-written drafts are never recovered.
  }
  storage.removeItem(key);
  return null;
}

export function saveOnboardingDraft(ownerId: string, draft: Omit<OnboardingDraft, 'version'>): void {
  const storage = browserStorage();
  const key = draftKey(ownerId);
  if (!storage || !key) return;
  const normalized = parseDraft({ ...draft, version: DRAFT_VERSION });
  if (!normalized) throw new Error('onboarding_draft_invalid');
  storage.setItem(key, JSON.stringify(normalized));
}

export function clearOnboardingDraft(ownerId: string): void {
  const storage = browserStorage();
  const key = draftKey(ownerId);
  if (storage && key) storage.removeItem(key);
}

function parseDraft(value: unknown): OnboardingDraft | null {
  if (!isRecord(value) || value.version !== DRAFT_VERSION || !isStep(value.step)) return null;
  const networkEnabled = booleanMap(value.networkEnabled);
  const connectorEnabled = booleanMap(value.connectorEnabled);
  const skillEnabled = booleanMap(value.skillEnabled);
  const scientificRuntimeEnabled =
    value.scientificRuntimeEnabled === undefined ? {} : booleanMap(value.scientificRuntimeEnabled);
  const profileSummary = boundedString(value.profileSummary);
  const selectedTask = boundedString(value.selectedTask);
  const customTask = boundedString(value.customTask);
  const attachmentNames = stringList(value.attachmentNames);
  if (
    !networkEnabled ||
    !connectorEnabled ||
    !skillEnabled ||
    !scientificRuntimeEnabled ||
    profileSummary === null ||
    selectedTask === null ||
    customTask === null ||
    !attachmentNames
  ) {
    return null;
  }
  return {
    version: 1,
    step: value.step,
    networkEnabled,
    connectorEnabled,
    skillEnabled,
    scientificRuntimeEnabled,
    profileSummary,
    attachmentNames,
    selectedTask,
    customTask,
  };
}

function booleanMap(value: unknown): Record<string, boolean> | null {
  if (!isRecord(value)) return null;
  const entries = Object.entries(value);
  if (entries.length > MAX_SELECTION_ENTRIES) return null;
  const result: Record<string, boolean> = {};
  for (const [key, item] of entries) {
    if (!key || key.length > 512 || typeof item !== 'boolean') return null;
    result[key] = item;
  }
  return result;
}

function stringList(value: unknown): string[] | null {
  if (!Array.isArray(value)) return null;
  const result: string[] = [];
  for (const item of value) {
    if (typeof item !== 'string' || !item) return null;
    result.push(item);
  }
  return result;
}

function boundedString(value: unknown): string | null {
  return typeof value === 'string' && value.length <= MAX_TEXT_LENGTH ? value : null;
}

function isStep(value: unknown): value is OnboardingStep {
  return Number.isInteger(value) && typeof value === 'number' && value >= 0 && value <= 5;
}

function draftKey(ownerId: string): string | null {
  const owner = ownerId.trim();
  if (!owner || owner.length > 256) return null;
  return `${DRAFT_PREFIX}:${encodeURIComponent(owner)}`;
}

function browserStorage(): Storage | null {
  return typeof window === 'undefined' ? null : window.localStorage;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}
