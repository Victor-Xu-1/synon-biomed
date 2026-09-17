import {
  createSynonBiomedTranscriptAnnotation,
  deleteSynonBiomedTranscriptAnnotation,
  loadSynonBiomedTranscriptAnnotations,
  updateSynonBiomedTranscriptAnnotation,
  type CreateSynonBiomedTranscriptAnnotationInput,
  type SynonBiomedTranscriptAnnotation,
} from '@/renderer/services/synonBiomedAnnotations';
import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

type ConversationAnnotationsValue = {
  annotations: SynonBiomedTranscriptAnnotation[];
  loading: boolean;
  error: string | null;
  create: (input: CreateSynonBiomedTranscriptAnnotationInput) => Promise<SynonBiomedTranscriptAnnotation>;
  update: (annotationId: string, patch: { note?: string; read?: boolean }) => Promise<SynonBiomedTranscriptAnnotation>;
  remove: (annotationId: string) => Promise<void>;
  refresh: () => Promise<void>;
};

const ConversationAnnotationsContext = createContext<ConversationAnnotationsValue | null>(null);

export const ConversationAnnotationsProvider: React.FC<{
  frameId: string | undefined;
  ownerId: string;
  children: React.ReactNode;
}> = ({ frameId, ownerId, children }) => {
  const { t } = useTranslation();
  const normalizedOwnerId = ownerId.trim();
  const authorityKey = `${normalizedOwnerId}\u0000${frameId ?? ''}`;
  const requestRef = useRef(0);
  const annotationsAuthorityRef = useRef(authorityKey);
  const [annotations, setAnnotations] = useState<SynonBiomedTranscriptAnnotation[]>([]);
  const [loading, setLoading] = useState(Boolean(frameId));
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    const requestId = requestRef.current + 1;
    requestRef.current = requestId;
    const requestAuthority = authorityKey;
    if (annotationsAuthorityRef.current !== requestAuthority) {
      annotationsAuthorityRef.current = requestAuthority;
      setAnnotations([]);
    }
    if (!frameId) {
      setAnnotations([]);
      setLoading(false);
      setError(null);
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const loaded = await loadSynonBiomedTranscriptAnnotations(frameId, {
        ownerId: normalizedOwnerId,
      });
      if (requestRef.current === requestId && annotationsAuthorityRef.current === requestAuthority) {
        setAnnotations(loaded);
      }
    } catch (loadError) {
      console.warn('[ConversationAnnotations] Failed to load transcript annotations:', loadError);
      if (requestRef.current === requestId && annotationsAuthorityRef.current === requestAuthority) {
        setError(t('conversation.transcriptAnnotations.loadFailed'));
      }
    } finally {
      if (requestRef.current === requestId && annotationsAuthorityRef.current === requestAuthority) {
        setLoading(false);
      }
    }
  }, [authorityKey, frameId, normalizedOwnerId, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const create = useCallback(
    async (input: CreateSynonBiomedTranscriptAnnotationInput) => {
      if (!frameId) throw new Error('Synon Biomed conversation frame is unavailable');
      const mutationAuthority = authorityKey;
      const created = await createSynonBiomedTranscriptAnnotation(frameId, input);
      if (annotationsAuthorityRef.current === mutationAuthority) {
        setAnnotations((current) => [...current, created]);
      }
      return created;
    },
    [authorityKey, frameId]
  );

  const update = useCallback(
    async (annotationId: string, patch: { note?: string; read?: boolean }) => {
      if (!frameId) throw new Error('Synon Biomed conversation frame is unavailable');
      const mutationAuthority = authorityKey;
      const updated = await updateSynonBiomedTranscriptAnnotation(frameId, annotationId, patch);
      if (annotationsAuthorityRef.current === mutationAuthority) {
        setAnnotations((current) => current.map((item) => (item.id === updated.id ? updated : item)));
      }
      return updated;
    },
    [authorityKey, frameId]
  );

  const remove = useCallback(
    async (annotationId: string) => {
      if (!frameId) throw new Error('Synon Biomed conversation frame is unavailable');
      const mutationAuthority = authorityKey;
      await deleteSynonBiomedTranscriptAnnotation(frameId, annotationId);
      if (annotationsAuthorityRef.current === mutationAuthority) {
        setAnnotations((current) => current.filter((item) => item.id !== annotationId));
      }
    },
    [authorityKey, frameId]
  );

  const visibleAnnotations = annotationsAuthorityRef.current === authorityKey ? annotations : [];
  const value = useMemo<ConversationAnnotationsValue>(
    () => ({ annotations: visibleAnnotations, loading, error, create, update, remove, refresh }),
    [create, error, loading, refresh, remove, update, visibleAnnotations]
  );

  return <ConversationAnnotationsContext.Provider value={value}>{children}</ConversationAnnotationsContext.Provider>;
};

export function useConversationAnnotations(): ConversationAnnotationsValue | null {
  return useContext(ConversationAnnotationsContext);
}

export function annotationsForMessage(
  annotations: SynonBiomedTranscriptAnnotation[],
  messageUuid: string | undefined,
  messageIndex: number
): SynonBiomedTranscriptAnnotation[] {
  return annotations.filter(
    (annotation) =>
      (messageUuid && annotation.messageUuid === messageUuid) ||
      (!annotation.messageUuid && annotation.messageIndex === messageIndex)
  );
}
