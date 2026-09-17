import { ipcBridge } from '@/common';
import type { TChatConversation } from '@/common/config/storage';
import React, { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams } from 'react-router';
import useSWR from 'swr';
import ChatConversation from './components/ChatConversation';
import ConversationLoadingSurface from './components/ConversationLoadingSurface';
import { usePreviewContext } from '@/renderer/pages/conversation/Preview/context/PreviewContext';
import { useAutoTitle } from '@/renderer/hooks/chat/useAutoTitle';
import {
  getConversationOrNullShared,
  readRecentConversationRouteDetail,
  readConversationRouteSnapshot,
  rememberConversationRouteSnapshot,
} from '@/renderer/pages/conversation/utils/conversationCache';
import { useAuth } from '@/renderer/hooks/context/AuthContext';
import { useRealtime } from '@/renderer/hooks/context/RealtimeContext';
import { redactErrorText } from '@/renderer/pages/conversation/platforms/acp/errorDiagnostics';
import { scheduleConversationRoutePerformanceStage } from './conversationRoutePerformance';
import { addEventListener } from '@/renderer/utils/emitter';
import { getConversationRuntimeViewSnapshot } from '@/renderer/pages/conversation/runtime/conversationRuntimeViewStore';

const isSynonBiomedConversationData = (conversation: TChatConversation | null | undefined): boolean => {
  const extra = conversation?.extra as { backend?: string; workspace?: string } | undefined;
  return (
    extra?.backend?.trim().toLowerCase() === 'synonbiomed' || Boolean(extra?.workspace?.startsWith('synonbiomed://'))
  );
};

const hasActiveSynonBiomedRuntime = (
  conversation: TChatConversation | null | undefined,
  conversationId: string
): boolean => {
  const runtimeView = getConversationRuntimeViewSnapshot(conversationId);
  return (
    runtimeView.isProcessing ||
    runtimeView.localSubmitting ||
    conversation?.runtime?.is_processing === true ||
    conversation?.runtime?.has_task === true ||
    conversation?.status === 'running'
  );
};

const ChatConversationIndex: React.FC = () => {
  const { id } = useParams();
  const { user } = useAuth();
  const { runtime: realtimeRuntime } = useRealtime();
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { closePreview } = usePreviewContext();
  const { syncTitleFromHistory } = useAutoTitle(user?.id ?? '');
  const previousConversationIdRef = useRef<string | undefined>(id);
  const defaultConversationTitle = t('conversation.welcome.newConversation');

  useEffect(() => {
    if (!id) return;

    // The PreviewProvider starts closed, so closing again on mount is both
    // unnecessary and harmful: layout remounts within the same conversation
    // would immediately dismiss a preview that was just opened. Only an
    // actual route-id transition owns closing the previous conversation's
    // preview.
    if (previousConversationIdRef.current && previousConversationIdRef.current !== id) {
      closePreview();
    }

    previousConversationIdRef.current = id;
  }, [id, closePreview]);

  useEffect(() => {
    if (!id) return;
    return scheduleConversationRoutePerformanceStage('shell');
  }, [id]);

  const recentRouteDetail = id && user?.id ? readRecentConversationRouteDetail(user.id, id) : undefined;
  const routeSnapshot = recentRouteDetail ?? (id && user?.id ? readConversationRouteSnapshot(user.id, id) : undefined);
  const { data, error, isLoading, mutate } = useSWR(
    id && user?.id ? `conversation/${user.id}/${id}` : null,
    () => getConversationOrNullShared(id!),
    { fallbackData: routeSnapshot, revalidateOnMount: !recentRouteDetail }
  );

  useEffect(() => {
    if (data && user?.id) rememberConversationRouteSnapshot(user.id, data, 'detail');
  }, [data, user?.id]);

  useEffect(() => {
    if (data !== null) return;

    // Only an authoritative not-found result may leave the route. A backend
    // restart or short network outage is not evidence that the conversation
    // disappeared; keep the route mounted so realtime recovery can restore it.
    closePreview();
    void navigate('/guid', {
      replace: true,
      state: { resetAssistant: true },
    });
  }, [closePreview, data, error, navigate]);

  useEffect(() => {
    if (!id) return;
    // A validated realtime reconnect is the earliest reliable signal that the
    // same backend authority is available again. Revalidate immediately rather
    // than leaving a failed initial request in SWR's exponential retry window.
    return realtimeRuntime.subscribeReconnected(() => {
      void mutate();
    });
  }, [id, mutate, realtimeRuntime]);

  useEffect(() => {
    if (!id) return;

    return ipcBridge.conversation.listChanged.on((event) => {
      if (event.conversation_id !== id || (event.action !== 'updated' && event.action !== 'created')) {
        return;
      }

      // Synon Biomed publishes live transcript/runtime changes through the
      // bounded stream and runtime wake paths. Re-reading the full conversation
      // for every metadata update competes with first-token rendering and can
      // replace the live projection with a publication that is one step behind.
      if (
        (event.action === 'updated' || event.action === 'created') &&
        isSynonBiomedConversationData(data) &&
        hasActiveSynonBiomedRuntime(data, id)
      ) {
        return;
      }

      void mutate();
    });
  }, [data, id, mutate]);

  useEffect(() => {
    if (!id || !isSynonBiomedConversationData(data)) return;

    return addEventListener('synonbiomed.runtime.reconciled', (conversationId, runtime) => {
      if (conversationId !== id) return;
      void mutate((current) => (current ? { ...current, runtime } : current), { revalidate: false });
    });
  }, [data, id, mutate]);

  useEffect(() => {
    if (!data || data.name !== defaultConversationTitle) {
      return;
    }

    void syncTitleFromHistory(data.id, undefined, { conversation: data });
  }, [data, defaultConversationTitle, syncTitleFromHistory]);

  useEffect(() => {
    if (!error) return;
    console.warn(
      '[ChatConversationIndex] Failed to load conversation:',
      redactErrorText(error instanceof Error ? error.message : String(error))
    );
  }, [error]);

  if (isLoading && !routeSnapshot && !data) return <ConversationLoadingSurface />;
  if (data === null) return <ConversationLoadingSurface />;
  if (error && data === undefined) return <ConversationLoadingSurface />;
  if (!data) return <ConversationLoadingSurface />;
  return <ChatConversation conversation={data}></ChatConversation>;
};

export default ChatConversationIndex;
