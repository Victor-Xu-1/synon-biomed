import {
  normalizeComposerContextItems,
  type ComposerContextItem,
} from '@/renderer/components/chat/SendBox/composerCompositionModel';
import type { FileOrFolderItem } from '@/renderer/utils/file/fileTypes';

const DRAFT_VERSION = 1;
const DRAFT_STORAGE_PREFIX = 'synonbiomed.composer.draft.v1';
const MAX_DRAFT_BYTES = 1 << 20;
const MAX_DRAFT_CONTENT_LENGTH = 1_000_000;
const MAX_DRAFT_FILE_ITEMS = 100;
const MAX_DRAFT_PATH_LENGTH = 8_192;
const MAX_DRAFT_NAME_LENGTH = 1_024;
const MAX_OWNER_ID_LENGTH = 256;
const MAX_CONVERSATION_ID_LENGTH = 512;

export type AcpSendBoxDraft = {
  _type: 'acp';
  content: string;
  atPath: Array<string | FileOrFolderItem>;
  uploadFile: string[];
  contextItems: ComposerContextItem[];
};

type PersistedSendBoxDraft = {
  version: 1;
  draft: AcpSendBoxDraft;
};

export function sendBoxDraftStorageKey(ownerId: string, conversationId: string): string | null {
  const owner = ownerId.trim() || 'local';
  const conversation = conversationId.trim();
  if (
    owner.length > MAX_OWNER_ID_LENGTH ||
    conversation.length === 0 ||
    conversation.length > MAX_CONVERSATION_ID_LENGTH
  ) {
    return null;
  }
  return `${DRAFT_STORAGE_PREFIX}:${encodeURIComponent(owner)}:${encodeURIComponent(conversation)}`;
}

export function loadSendBoxDraft(ownerId: string, conversationId: string): AcpSendBoxDraft | undefined {
  const storage = browserStorage();
  const key = sendBoxDraftStorageKey(ownerId, conversationId);
  if (!storage || !key) return undefined;
  let raw: string | null = null;
  try {
    raw = storage.getItem(key);
    if (!raw) return undefined;
    if (new TextEncoder().encode(raw).byteLength > MAX_DRAFT_BYTES) {
      storage.removeItem(key);
      return undefined;
    }
    const parsed = JSON.parse(raw) as unknown;
    if (!isRecord(parsed) || parsed.version !== DRAFT_VERSION) {
      storage.removeItem(key);
      return undefined;
    }
    const draft = normalizeAcpSendBoxDraft(parsed.draft);
    if (!draft) {
      storage.removeItem(key);
      return undefined;
    }
    return draft;
  } catch {
    try {
      storage.removeItem(key);
    } catch {
      // Storage may become unavailable between reads. The in-memory draft
      // remains authoritative for the current page lifetime.
    }
    return undefined;
  }
}

export function saveSendBoxDraft(ownerId: string, conversationId: string, value: AcpSendBoxDraft): boolean {
  const storage = browserStorage();
  const key = sendBoxDraftStorageKey(ownerId, conversationId);
  const draft = normalizeAcpSendBoxDraft(value);
  if (!storage || !key || !draft) return false;
  try {
    if (isEmptyDraft(draft)) {
      storage.removeItem(key);
      return true;
    }
    const persisted: PersistedSendBoxDraft = { version: DRAFT_VERSION, draft };
    const serialized = JSON.stringify(persisted);
    if (new TextEncoder().encode(serialized).byteLength > MAX_DRAFT_BYTES) return false;
    storage.setItem(key, serialized);
    return true;
  } catch {
    return false;
  }
}

function isEmptyDraft(draft: AcpSendBoxDraft): boolean {
  return (
    draft.content.length === 0 &&
    draft.atPath.length === 0 &&
    draft.uploadFile.length === 0 &&
    draft.contextItems.length === 0
  );
}

export function clearSendBoxDraft(ownerId: string, conversationId: string): void {
  const storage = browserStorage();
  const key = sendBoxDraftStorageKey(ownerId, conversationId);
  if (!storage || !key) return;
  try {
    storage.removeItem(key);
  } catch {
    // The in-memory store is still cleared by the caller.
  }
}

function normalizeAcpSendBoxDraft(value: unknown): AcpSendBoxDraft | null {
  if (!isRecord(value) || value._type !== 'acp') return null;
  if (typeof value.content !== 'string' || value.content.length > MAX_DRAFT_CONTENT_LENGTH) return null;
  const atPath = normalizeAtPath(value.atPath);
  const uploadFile = normalizeStringList(value.uploadFile, MAX_DRAFT_PATH_LENGTH);
  if (!atPath || !uploadFile) return null;
  return {
    _type: 'acp',
    content: value.content,
    atPath,
    uploadFile,
    contextItems: normalizeComposerContextItems(value.contextItems),
  };
}

function normalizeAtPath(value: unknown): Array<string | FileOrFolderItem> | null {
  if (!Array.isArray(value) || value.length > MAX_DRAFT_FILE_ITEMS) return null;
  const result: Array<string | FileOrFolderItem> = [];
  const seen = new Set<string>();
  for (const candidate of value) {
    let item: string | FileOrFolderItem | null = null;
    if (validBoundedString(candidate, MAX_DRAFT_PATH_LENGTH)) {
      item = candidate;
    } else if (isRecord(candidate)) {
      const path = validBoundedString(candidate.path, MAX_DRAFT_PATH_LENGTH) ? candidate.path : null;
      const name = validBoundedString(candidate.name, MAX_DRAFT_NAME_LENGTH) ? candidate.name : null;
      const relativePath =
        candidate.relativePath === undefined
          ? undefined
          : validBoundedString(candidate.relativePath, MAX_DRAFT_PATH_LENGTH)
            ? candidate.relativePath
            : null;
      if (path && name && typeof candidate.isFile === 'boolean' && relativePath !== null) {
        item = {
          path,
          name,
          isFile: candidate.isFile,
          ...(relativePath === undefined ? {} : { relativePath }),
        };
      }
    }
    if (!item) return null;
    const identity = typeof item === 'string' ? `path:${item}` : `item:${item.path}:${item.relativePath ?? ''}`;
    if (seen.has(identity)) continue;
    seen.add(identity);
    result.push(item);
  }
  return result;
}

function normalizeStringList(value: unknown, maxLength: number): string[] | null {
  if (!Array.isArray(value) || value.length > MAX_DRAFT_FILE_ITEMS) return null;
  const result: string[] = [];
  const seen = new Set<string>();
  for (const candidate of value) {
    if (!validBoundedString(candidate, maxLength)) return null;
    if (seen.has(candidate)) continue;
    seen.add(candidate);
    result.push(candidate);
  }
  return result;
}

function validBoundedString(value: unknown, maxLength: number): value is string {
  return typeof value === 'string' && value.trim().length > 0 && value.length <= maxLength;
}

function browserStorage(): Storage | null {
  if (typeof window === 'undefined') return null;
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}
