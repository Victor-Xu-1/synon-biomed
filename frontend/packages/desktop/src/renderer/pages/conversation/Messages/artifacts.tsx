/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import type {
  ConversationArtifactReference,
  IConversationArtifact,
  IConversationArtifactStatus,
  IScientificFilesArtifact,
  ISynonBiomedScientificFile,
} from '@/common/adapter/ipcBridge';
import { useAuth } from '@/renderer/hooks/context/AuthContext';
import { createKeyedSnapshotStore } from '@/renderer/services/keyedSnapshotStore';
import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react';

type ConversationArtifactContextValue = {
  artifacts: IConversationArtifact[];
  index: ConversationArtifactIndex;
  upsertArtifact: (artifact: IConversationArtifact) => void;
  updateArtifactStatus: (artifact_id: string, status: IConversationArtifactStatus) => void;
  syncWindowReferences: (references: ConversationArtifactReference[]) => void;
  resolveWindowReferences: (
    references: ConversationArtifactReference[],
    referenceIds?: string[]
  ) => Promise<IConversationArtifact[] | null>;
};

export type ConversationArtifactIndex = {
  byFilename: Map<string, ISynonBiomedScientificFile>;
  byArtifactId: Map<string, ISynonBiomedScientificFile>;
  byVersionId: Map<string, ISynonBiomedScientificFile>;
};

const EMPTY_ARTIFACT_INDEX: ConversationArtifactIndex = {
  byFilename: new Map(),
  byArtifactId: new Map(),
  byVersionId: new Map(),
};

const ConversationArtifactContext = createContext<ConversationArtifactContextValue>({
  artifacts: [],
  index: EMPTY_ARTIFACT_INDEX,
  upsertArtifact: () => {},
  updateArtifactStatus: () => {},
  syncWindowReferences: () => {},
  resolveWindowReferences: async () => null,
});

const artifactSnapshotStore = createKeyedSnapshotStore<IConversationArtifact[]>({ maxEntries: 8, ttlMs: 30_000 });
const artifactReferenceSnapshotStore = createKeyedSnapshotStore<IConversationArtifact[]>({
  maxEntries: 2_048,
  ttlMs: 10 * 60_000,
});

function artifactSnapshotKey(ownerId: string, conversationId: string): string {
  return JSON.stringify([ownerId.trim() || '__pending__', conversationId]);
}

function artifactWindowSnapshotKey(
  ownerId: string,
  conversationId: string,
  references: ConversationArtifactReference[]
) {
  return JSON.stringify([
    ownerId.trim() || '__pending__',
    conversationId,
    references.map((reference) => [reference.artifact_id, reference.version_id]),
  ]);
}

function artifactReferenceSnapshotKey(
  ownerId: string,
  conversationId: string,
  reference: ConversationArtifactReference
): string {
  return JSON.stringify([ownerId.trim() || '__pending__', conversationId, reference.artifact_id, reference.version_id]);
}

function normalizeArtifactWindowReferences(
  references: ConversationArtifactReference[]
): ConversationArtifactReference[] {
  const result: ConversationArtifactReference[] = [];
  const seen = new Set<string>();
  for (const reference of references) {
    const artifactId = reference.artifact_id.trim();
    const versionId = reference.version_id.trim();
    if (!artifactId || !versionId) continue;
    const key = `${artifactId}\0${versionId}`;
    if (seen.has(key)) continue;
    seen.add(key);
    result.push({ artifact_id: artifactId, version_id: versionId });
  }
  return result;
}

function mergeArtifactReferenceBatches(batches: IConversationArtifact[][]): IConversationArtifact[] {
  const collections = batches.flat();
  const scientific = collections.filter(
    (artifact): artifact is IScientificFilesArtifact => artifact.kind === 'scientific_files'
  );
  if (scientific.length === 0) return [];
  const first = scientific[0];
  return [
    {
      ...first,
      payload: {
        ...first.payload,
        files: scientific.flatMap((artifact) => artifact.payload.files),
      },
      updated_at: Math.max(...scientific.map((artifact) => artifact.updated_at)),
    },
  ];
}

function cacheExactArtifactReferenceBatches(
  ownerId: string,
  conversationId: string,
  references: ConversationArtifactReference[],
  collections: IConversationArtifact[][]
): void {
  const requested = new Set(references.map((reference) => `${reference.artifact_id}\0${reference.version_id}`));
  const resolved = new Map<string, IConversationArtifact[]>();
  for (const collection of collections.flat()) {
    if (collection.kind !== 'scientific_files') continue;
    for (const file of collection.payload.files) {
      const key = `${file.artifact_id}\0${file.version_id}`;
      if (!requested.has(key) || resolved.has(key)) {
        throw new Error('artifact_reference_response_invalid');
      }
      resolved.set(key, [
        {
          ...collection,
          payload: { ...collection.payload, files: [file] },
        },
      ]);
    }
  }
  for (const reference of references) {
    artifactReferenceSnapshotStore.write(
      artifactReferenceSnapshotKey(ownerId, conversationId, reference),
      resolved.get(`${reference.artifact_id}\0${reference.version_id}`) ?? []
    );
  }
}

function readCachedArtifactReferenceBatches(
  ownerId: string,
  conversationId: string,
  references: ConversationArtifactReference[]
): { batches: IConversationArtifact[][]; missing: ConversationArtifactReference[] } {
  const batches: IConversationArtifact[][] = [];
  const missing: ConversationArtifactReference[] = [];
  for (const reference of references) {
    const cached = artifactReferenceSnapshotStore.read(
      artifactReferenceSnapshotKey(ownerId, conversationId, reference)
    );
    if (cached) batches.push(cached.value);
    else missing.push(reference);
  }
  return { batches, missing };
}

async function loadArtifactReferenceChunks(
  conversationId: string,
  chunks: ConversationArtifactReference[][],
  signal: AbortSignal,
  offset = 0,
  batches: IConversationArtifact[][] = []
): Promise<IConversationArtifact[][]> {
  const group = chunks.slice(offset, offset + 4);
  if (group.length === 0) return batches;
  batches.push(
    ...(await Promise.all(
      group.map((references) =>
        ipcBridge.conversation.listArtifacts.invoke({ conversation_id: conversationId, references }, signal)
      )
    ))
  );
  return loadArtifactReferenceChunks(conversationId, chunks, signal, offset + 4, batches);
}

function isAbortError(error: unknown): boolean {
  return Boolean(error && typeof error === 'object' && 'name' in error && error.name === 'AbortError');
}

const compareScientificFileVersions = (left: ISynonBiomedScientificFile, right: ISynonBiomedScientificFile): number => {
  if (left.version_number !== right.version_number) return left.version_number - right.version_number;
  if (left.updated_at !== right.updated_at) return left.updated_at - right.updated_at;
  if (left.created_at !== right.created_at) return left.created_at - right.created_at;
  return String(left.version_id).localeCompare(String(right.version_id));
};

export function createConversationArtifactIndex(collections: IConversationArtifact[]): ConversationArtifactIndex {
  const byFilename = new Map<string, ISynonBiomedScientificFile>();
  const byArtifactId = new Map<string, ISynonBiomedScientificFile>();
  const byVersionId = new Map<string, ISynonBiomedScientificFile>();
  for (const collection of collections) {
    if (collection.kind !== 'scientific_files') continue;
    for (const file of (collection as IScientificFilesArtifact).payload.files) {
      if (file.version_id) byVersionId.set(file.version_id, file);
      const current = byArtifactId.get(file.artifact_id);
      if (!current || compareScientificFileVersions(file, current) > 0) {
        byArtifactId.set(file.artifact_id, file);
      }
    }
  }
  const filenameOwners = new Map<string, string>();
  const ambiguousFilenames = new Set<string>();
  for (const file of byArtifactId.values()) {
    const key = file.filename.toLocaleLowerCase();
    const owner = filenameOwners.get(key);
    if (owner && owner !== file.artifact_id) {
      ambiguousFilenames.add(key);
      byFilename.delete(key);
      continue;
    }
    if (!ambiguousFilenames.has(key)) {
      filenameOwners.set(key, file.artifact_id);
      byFilename.set(key, file);
    }
  }
  return { byFilename, byArtifactId, byVersionId };
}

function upsertArtifacts(
  current: IConversationArtifact[],
  next: IConversationArtifact | IConversationArtifact[]
): IConversationArtifact[] {
  const incoming = Array.isArray(next) ? next : [next];
  if (!incoming.length) return current;

  const artifactById = new Map(current.map((artifact) => [artifact.id, artifact]));
  for (const artifact of incoming) {
    artifactById.set(artifact.id, artifact);
  }

  return Array.from(artifactById.values()).toSorted((a, b) => a.created_at - b.created_at);
}

export const useConversationArtifacts = (): IConversationArtifact[] =>
  useContext(ConversationArtifactContext).artifacts;

export const useConversationArtifactIndex = (): ConversationArtifactIndex =>
  useContext(ConversationArtifactContext).index;

export const useUpsertConversationArtifact = (): ((artifact: IConversationArtifact) => void) =>
  useContext(ConversationArtifactContext).upsertArtifact;

export const useUpdateConversationArtifactStatus = (): ((
  artifact_id: string,
  status: IConversationArtifactStatus
) => void) => useContext(ConversationArtifactContext).updateArtifactStatus;

export const useSyncConversationArtifactWindow = (): ((references: ConversationArtifactReference[]) => void) =>
  useContext(ConversationArtifactContext).syncWindowReferences;

export const useResolveConversationArtifactWindow = (): ((
  references: ConversationArtifactReference[],
  referenceIds?: string[]
) => Promise<IConversationArtifact[] | null>) => useContext(ConversationArtifactContext).resolveWindowReferences;

export const ConversationArtifactProvider: React.FC<React.PropsWithChildren<{ conversation_id: string }>> = ({
  conversation_id,
  children,
}) => {
  const { user } = useAuth();
  const ownerId = user?.id?.trim() ?? '';
  const baseSnapshotKey = artifactSnapshotKey(ownerId, conversation_id);
  const [artifacts, setArtifacts] = useState<IConversationArtifact[]>([]);
  const loadGenerationRef = useRef(0);
  const activeWindowKeyRef = useRef('');
  const activeWindowLoadRef = useRef<{
    key: string;
    referenceKeys: Set<string>;
    artifactIds: Set<string>;
    versionIds: Set<string>;
    promise: Promise<IConversationArtifact[]>;
  } | null>(null);
  const index = useMemo(() => createConversationArtifactIndex(artifacts), [artifacts]);

  const upsertArtifact = useCallback((artifact: IConversationArtifact) => {
    setArtifacts((current) => {
      const next = upsertArtifacts(current, artifact);
      if (activeWindowKeyRef.current) artifactSnapshotStore.write(activeWindowKeyRef.current, next);
      return next;
    });
  }, []);

  const updateArtifactStatus = useCallback((artifact_id: string, status: IConversationArtifactStatus) => {
    setArtifacts((current) => {
      const next = current.map((artifact) =>
        artifact.id === artifact_id ? { ...artifact, status, updated_at: Date.now() } : artifact
      );
      if (activeWindowKeyRef.current) artifactSnapshotStore.write(activeWindowKeyRef.current, next);
      return next;
    });
  }, []);

  const syncWindowReferences = useCallback(
    (rawReferences: ConversationArtifactReference[]) => {
      const references = normalizeArtifactWindowReferences(rawReferences);
      const snapshotKey = artifactWindowSnapshotKey(ownerId, conversation_id, references);
      if (activeWindowKeyRef.current === snapshotKey) return;
      const previousWindowKey = activeWindowKeyRef.current;
      if (previousWindowKey) artifactSnapshotStore.cancel(previousWindowKey);
      activeWindowKeyRef.current = snapshotKey;
      const generation = ++loadGenerationRef.current;
      const referenceKeys = new Set(references.map((reference) => `${reference.artifact_id}\0${reference.version_id}`));
      const artifactIds = new Set(references.map((reference) => reference.artifact_id));
      const versionIds = new Set(references.map((reference) => reference.version_id));
      const cachedWindow = artifactSnapshotStore.read(snapshotKey);
      if (cachedWindow) {
        activeWindowLoadRef.current = {
          key: snapshotKey,
          referenceKeys,
          artifactIds,
          versionIds,
          promise: Promise.resolve(cachedWindow.value),
        };
        setArtifacts(cachedWindow.value);
        return;
      }
      const referenceCache = readCachedArtifactReferenceBatches(ownerId, conversation_id, references);
      const cachedItems = mergeArtifactReferenceBatches(referenceCache.batches);
      setArtifacts(cachedItems);
      if (!conversation_id || !ownerId || references.length === 0) {
        activeWindowLoadRef.current = {
          key: snapshotKey,
          referenceKeys,
          artifactIds,
          versionIds,
          promise: Promise.resolve(cachedItems),
        };
        return;
      }
      if (referenceCache.missing.length === 0) {
        artifactSnapshotStore.write(snapshotKey, cachedItems);
        activeWindowLoadRef.current = {
          key: snapshotKey,
          referenceKeys,
          artifactIds,
          versionIds,
          promise: Promise.resolve(cachedItems),
        };
        return;
      }
      const chunks: ConversationArtifactReference[][] = [];
      for (let offset = 0; offset < referenceCache.missing.length; offset += 400) {
        chunks.push(referenceCache.missing.slice(offset, offset + 400));
      }
      const windowLoad = artifactSnapshotStore.load(snapshotKey, async (signal) => {
        const batches = await loadArtifactReferenceChunks(conversation_id, chunks, signal);
        if (signal.aborted) {
          const error = new Error('artifact reference request aborted');
          error.name = 'AbortError';
          throw error;
        }
        cacheExactArtifactReferenceBatches(ownerId, conversation_id, referenceCache.missing, batches);
        return mergeArtifactReferenceBatches(
          readCachedArtifactReferenceBatches(ownerId, conversation_id, references).batches
        );
      });
      activeWindowLoadRef.current = {
        key: snapshotKey,
        referenceKeys,
        artifactIds,
        versionIds,
        promise: windowLoad,
      };
      void windowLoad
        .then((items) => {
          if (generation === loadGenerationRef.current && activeWindowKeyRef.current === snapshotKey) {
            setArtifacts(items);
          }
        })
        .catch((error) => {
          if (generation !== loadGenerationRef.current || activeWindowKeyRef.current !== snapshotKey) return;
          if (!isAbortError(error)) console.error('[artifacts] snapshot refresh failed');
        });
    },
    [conversation_id, ownerId]
  );

  const resolveWindowReferences = useCallback(
    async (
      rawReferences: ConversationArtifactReference[],
      rawReferenceIds: string[] = []
    ): Promise<IConversationArtifact[] | null> => {
      const references = normalizeArtifactWindowReferences(rawReferences);
      const referenceIds = rawReferenceIds.map((value) => value.trim()).filter(Boolean);
      if (references.length === 0 && referenceIds.length === 0) return [];
      const active = activeWindowLoadRef.current;
      if (!active) return null;
      const belongsToActiveWindow =
        references.every((reference) =>
          active.referenceKeys.has(`${reference.artifact_id}\0${reference.version_id}`)
        ) && referenceIds.every((value) => active.versionIds.has(value) || active.artifactIds.has(value));
      return belongsToActiveWindow ? active.promise : null;
    },
    []
  );

  useLayoutEffect(() => {
    loadGenerationRef.current += 1;
    activeWindowKeyRef.current = '';
    activeWindowLoadRef.current = null;
    setArtifacts([]);
  }, [baseSnapshotKey]);

  useEffect(() => {
    return () => {
      loadGenerationRef.current += 1;
      if (activeWindowKeyRef.current) artifactSnapshotStore.cancel(activeWindowKeyRef.current);
      activeWindowLoadRef.current = null;
    };
  }, [baseSnapshotKey]);

  const value = useMemo<ConversationArtifactContextValue>(
    () => ({
      artifacts,
      index,
      upsertArtifact,
      updateArtifactStatus,
      syncWindowReferences,
      resolveWindowReferences,
    }),
    [artifacts, index, resolveWindowReferences, syncWindowReferences, upsertArtifact, updateArtifactStatus]
  );

  return <ConversationArtifactContext.Provider value={value}>{children}</ConversationArtifactContext.Provider>;
};
