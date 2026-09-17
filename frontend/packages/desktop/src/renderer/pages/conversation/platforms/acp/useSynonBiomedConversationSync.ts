import type { TChatConversation, TConversationRuntimeSummary } from '@/common/config/storage';
import {
  getConversationRuntimeViewSnapshot,
  hydrateSucceeded,
} from '@/renderer/pages/conversation/runtime/conversationRuntimeViewStore';
import {
  subscribeSynonBiomedConversationWake,
  type SynonBiomedConversationWakeSignal,
} from '@/renderer/services/synonBiomedConversationWake';
import { getConversationOrNullShared } from '@/renderer/pages/conversation/utils/conversationCache';
import type { MessageListLoadSource } from '@/renderer/pages/conversation/Messages/hooks';
import { isMessageRequestAbort } from '@/renderer/pages/conversation/Messages/messageRequestAbort';
import { emitter } from '@/renderer/utils/emitter';
import { redactErrorText } from './errorDiagnostics';
import { useEffect, useRef } from 'react';

export const SYNON_BIOMED_CONVERSATION_POLL_INTERVAL_MS = 500;
export const SYNON_BIOMED_CONVERSATION_MAX_RETRY_INTERVAL_MS = 10_000;
export const SYNON_BIOMED_CONVERSATION_HANDOFF_MAX_ATTEMPTS = 10;
// Realtime wakes carry the normal live path. This is only the authority
// heartbeat used after the short handoff fallback is exhausted, so a missed
// terminal wake cannot leave a 24-hour task visually stuck in "running".
export const SYNON_BIOMED_CONVERSATION_LIVE_RECONCILE_INTERVAL_MS = 10_000;
export const SYNON_BIOMED_CONVERSATION_TERMINAL_FALLBACK_GRACE_MS = 1_500;

export type SynonBiomedConversationSyncIssue =
  | {
      kind: 'missing';
      retryDelayMs: null;
    }
  | {
      kind: 'unavailable';
      retryDelayMs: number;
    };

export type SynonBiomedConversationSyncOptions = {
  conversationId: string;
  initialConversation?: TChatConversation;
  workspace?: string;
  backend?: string;
  enabled: boolean;
  initialRuntime?: TConversationRuntimeSummary | null;
  runtimeActive?: boolean;
  pendingLocalSend?: boolean;
  refreshMessages: (replace?: boolean, source?: MessageListLoadSource) => Promise<unknown>;
  getLastStreamActivityAt?: () => number;
  onSettled: () => void;
  onIssue?: (issue: SynonBiomedConversationSyncIssue | null) => void;
  subscribeWake?: (conversationId: string, wake: (signal?: SynonBiomedConversationWakeSignal) => void) => () => void;
  pollIntervalMs?: number;
};

type SynonBiomedConversationPayload = {
  runtime?: TConversationRuntimeSummary;
};

type ConversationReadResult = {
  conversation?: TChatConversation | null;
  error: unknown | null;
};

type SynonBiomedConversationSyncScope = 'stream' | 'reconcile' | 'heartbeat';
type SynonBiomedConversationInternalWakeSignal = SynonBiomedConversationWakeSignal | { kind: 'heartbeat' };

const getSynonBiomedSyncSignalPriority = (signal: SynonBiomedConversationInternalWakeSignal): number =>
  signal.kind === 'reconcile'
    ? signal.refreshHistory === true
      ? 4
      : signal.terminalBoundary === true
        ? 3
        : 2
    : signal.kind === 'heartbeat'
      ? 1
      : 0;

export const isSynonBiomedConversation = (backend?: string, workspace?: string): boolean =>
  backend?.trim().toLowerCase() === 'synonbiomed' || Boolean(workspace?.startsWith('synonbiomed://'));

export const getSynonBiomedConversationRetryDelay = (
  failureCount: number,
  pollIntervalMs: number,
  maxIntervalMs = SYNON_BIOMED_CONVERSATION_MAX_RETRY_INTERVAL_MS
): number => Math.min(Math.max(pollIntervalMs, 1) * 2 ** Math.max(failureCount - 1, 0), maxIntervalMs);

const readRuntime = (value: unknown): TConversationRuntimeSummary | null => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const runtime = (value as SynonBiomedConversationPayload).runtime;
  if (!runtime || typeof runtime !== 'object') return null;
  if (typeof runtime.state !== 'string' || typeof runtime.is_processing !== 'boolean') return null;
  if (typeof runtime.can_send_message !== 'boolean' || typeof runtime.pending_confirmations !== 'number') return null;
  return runtime;
};

export function useSynonBiomedConversationSync({
  conversationId,
  initialConversation,
  workspace,
  backend,
  enabled,
  initialRuntime = null,
  runtimeActive = false,
  pendingLocalSend = false,
  refreshMessages,
  getLastStreamActivityAt,
  onSettled,
  onIssue,
  subscribeWake = subscribeSynonBiomedConversationWake,
  pollIntervalMs = SYNON_BIOMED_CONVERSATION_POLL_INTERVAL_MS,
}: SynonBiomedConversationSyncOptions): void {
  const runtimeActiveRef = useRef(runtimeActive);
  const pendingLocalSendRef = useRef(pendingLocalSend);
  const enabledRef = useRef(enabled);
  const refreshMessagesRef = useRef(refreshMessages);
  const getLastStreamActivityAtRef = useRef(getLastStreamActivityAt);
  const onSettledRef = useRef(onSettled);
  const onIssueRef = useRef(onIssue);
  const initialRuntimeRef = useRef(initialRuntime);
  const initialConversationRef = useRef(initialConversation);
  runtimeActiveRef.current = runtimeActive;
  pendingLocalSendRef.current = pendingLocalSend;
  enabledRef.current = enabled;
  refreshMessagesRef.current = refreshMessages;
  getLastStreamActivityAtRef.current = getLastStreamActivityAt;
  onSettledRef.current = onSettled;
  onIssueRef.current = onIssue;
  initialRuntimeRef.current = initialRuntime;
  initialConversationRef.current = initialConversation;
  useEffect(() => {
    if (!conversationId || !isSynonBiomedConversation(backend, workspace)) return;

    let disposed = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let consecutiveFailures = 0;
    let currentRuntime: TConversationRuntimeSummary | null = readRuntime({
      runtime: initialRuntimeRef.current,
    });
    let observedProcessing = currentRuntime?.is_processing === true || currentRuntime?.has_task === true;
    let terminalMountReconciled = enabledRef.current;
    let syncInFlight = false;
    let pendingSignal: SynonBiomedConversationInternalWakeSignal | null = null;
    let handoffPollsRemaining = SYNON_BIOMED_CONVERSATION_HANDOFF_MAX_ATTEMPTS;
    let initialRouteSnapshotPending = true;
    let lastFallbackHistoryRefreshAt = 0;

    const scheduleRetry = (
      delayMs: number,
      signal: SynonBiomedConversationInternalWakeSignal = { kind: 'heartbeat' },
      options: { keepAlive?: boolean } = {}
    ) => {
      // Realtime wakeups remain the primary live-update path. The bounded
      // handoff polls cover startup and the first missed publications. Once
      // that budget is spent, keep one low-frequency authoritative heartbeat
      // while the runtime says it is processing; without it, a lost terminal
      // wake leaves the page permanently showing a running task.
      const keepAlive = options.keepAlive === true;
      if (disposed || (!keepAlive && handoffPollsRemaining <= 0)) return;
      if (!keepAlive) handoffPollsRemaining -= 1;
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => {
        timer = undefined;
        requestSync(signal);
      }, delayMs);
    };

    const sync = async (signal: SynonBiomedConversationInternalWakeSignal) => {
      const scope: SynonBiomedConversationSyncScope = signal.kind;
      try {
        const isInitialRouteSync = initialRouteSnapshotPending;
        observedProcessing ||= currentRuntime?.is_processing === true || currentRuntime?.has_task === true;
        // The send endpoint can acknowledge a turn as processing before the
        // route conversation snapshot has caught up. When the runtime view
        // already says that a turn is active but the route snapshot is still
        // idle, the first sync must read the authoritative runtime.
        const runtimeHandoffNeedsReconcile =
          (runtimeActiveRef.current || pendingLocalSendRef.current) && currentRuntime?.is_processing !== true;
        const canUseInitialRouteSnapshot =
          initialRouteSnapshotPending &&
          initialConversationRef.current?.id === conversationId &&
          currentRuntime !== null &&
          !runtimeActiveRef.current &&
          !pendingLocalSendRef.current;
        const readConversation =
          !canUseInitialRouteSnapshot &&
          (scope !== 'stream' || currentRuntime === null || runtimeHandoffNeedsReconcile);
        const conversationRequest: Promise<ConversationReadResult> = readConversation
          ? getConversationOrNullShared(conversationId).then(
              (conversation): ConversationReadResult => ({
                conversation,
                error: null,
              }),
              (error: unknown): ConversationReadResult => ({
                conversation: undefined,
                error,
              })
            )
          : Promise.resolve({ conversation: undefined, error: null });
        const conversationResult = await conversationRequest;
        const conversation = conversationResult.conversation;
        if (readConversation && conversationResult.error && currentRuntime === null) {
          throw conversationResult.error;
        }
        if (readConversation && conversation === null && !conversationResult.error) {
          if (!disposed) {
            onIssueRef.current?.({
              kind: 'missing',
              retryDelayMs: null,
            });
          }
          return;
        }
        const hadCurrentRuntime = currentRuntime !== null;
        const runtime = readConversation && !conversationResult.error ? readRuntime(conversation) : currentRuntime;
        if (!runtime) {
          throw new Error('Synon Biomed conversation runtime is missing');
        }
        currentRuntime = runtime;
        initialRouteSnapshotPending = false;
        if (disposed) return;

        consecutiveFailures = 0;
        onIssueRef.current?.(null);
        if (conversationResult.error) {
          console.warn(
            '[SynonBiomedConversationSync] Conversation reconcile failed; retaining the known runtime and live projection:',
            redactErrorText(
              conversationResult.error instanceof Error
                ? conversationResult.error.message
                : String(conversationResult.error || 'conversation unavailable')
            )
          );
          scheduleRetry(
            getSynonBiomedConversationRetryDelay(consecutiveFailures + 1, pollIntervalMs),
            { kind: 'heartbeat' },
            { keepAlive: runtime.is_processing || runtime.has_task }
          );
        }
        if (readConversation) {
          const releasePendingLocalSend = !runtime.is_processing && observedProcessing;
          const runtimeView = getConversationRuntimeViewSnapshot(conversationId);
          const terminalProjectionPending =
            !runtime.is_processing &&
            !runtime.has_task &&
            observedProcessing &&
            (!runtimeView.hydrated ||
              runtimeView.terminalProjectionPending ||
              runtimeView.isProcessing ||
              runtimeView.hasTask);
          hydrateSucceeded(conversationId, runtime, {
            releasePendingLocalSend,
            terminalProjectionPending,
            ...(releasePendingLocalSend ? { completedTurnId: conversationId } : {}),
          });
          emitter.emit('synonbiomed.runtime.reconciled', conversationId, runtime);
          if (runtime.is_processing || runtime.has_task) observedProcessing = true;
        }
        // The message hook owns the initial compact history load. Re-fetching
        // the complete canonical window for every live publication used to
        // abort that initial request and start another full server projection
        // every two seconds. Live text arrives directly over the Transcript
        // WebSocket lane. Reconcile durable history only at user-visible
        // runtime/tool boundaries or after an observed run reaches terminal.
        const observedTerminal = readConversation && !runtime.is_processing && !runtime.has_task && observedProcessing;
        // Runtime snapshots may reach idle a few hundred milliseconds before
        // the ordered Transcript fanout delivers its final text and terminal
        // publication. Only that realtime terminal boundary may settle the
        // visible turn. A heartbeat that discovers terminal first schedules a
        // short authority recheck instead of replacing an active text tail.
        const reachedTerminal = observedTerminal && scope === 'reconcile';
        const heartbeatObservedTerminal = observedTerminal && scope === 'heartbeat';
        // Explicit tool boundaries are delivered as reconcile wakeups. Plain
        // start/lock/runtime wakes stay on the lightweight stream scope so
        // they cannot replace or fence the direct Transcript text tail.
        const reconcilePublicationNeedsRefresh =
          readConversation &&
          hadCurrentRuntime &&
          scope === 'reconcile' &&
          signal.kind === 'reconcile' &&
          signal.refreshHistory === true &&
          runtime.is_processing;
        const terminalMountNeedsReconciliation =
          readConversation &&
          !enabledRef.current &&
          !runtime.is_processing &&
          !runtime.has_task &&
          !terminalMountReconciled;
        const now = Date.now();
        const lastStreamActivityAt = getLastStreamActivityAtRef.current?.();
        const stalledLiveProjectionNeedsRefresh =
          scope === 'heartbeat' &&
          runtime.is_processing &&
          typeof lastStreamActivityAt === 'number' &&
          Number.isFinite(lastStreamActivityAt) &&
          now - lastStreamActivityAt >= SYNON_BIOMED_CONVERSATION_LIVE_RECONCILE_INTERVAL_MS &&
          now - lastFallbackHistoryRefreshAt >= SYNON_BIOMED_CONVERSATION_LIVE_RECONCILE_INTERVAL_MS;
        const shouldRefreshMessages =
          reachedTerminal ||
          reconcilePublicationNeedsRefresh ||
          terminalMountNeedsReconciliation ||
          stalledLiveProjectionNeedsRefresh;
        if (shouldRefreshMessages) {
          // Durable reconciliation is always an incremental merge. Runtime
          // status and transcript content are independent authorities: a
          // terminal status must never clear or replace an already-rendered
          // stream while the durable projector is still catching up. Explicit
          // branch navigation remains the only history path allowed to replace
          // the visible window.
          const refreshSource: MessageListLoadSource =
            terminalMountNeedsReconciliation && isInitialRouteSync
              ? 'terminal-mount'
              : runtime.is_processing
                ? 'live'
                : 'refresh';
          await refreshMessagesRef.current(false, refreshSource);
          if (stalledLiveProjectionNeedsRefresh) lastFallbackHistoryRefreshAt = Date.now();
        }
        if (terminalMountNeedsReconciliation) terminalMountReconciled = true;
        if (disposed) return;

        if (heartbeatObservedTerminal) {
          scheduleRetry(
            SYNON_BIOMED_CONVERSATION_TERMINAL_FALLBACK_GRACE_MS,
            { kind: 'reconcile' },
            { keepAlive: true }
          );
        }

        // A newly accepted local send can navigate to the conversation before
        // its realtime subscription is attached. If the first snapshot is
        // still idle, briefly reconcile until the backend run is observed.
        // Once processing is visible, normal event-driven streaming resumes;
        // healthy long-running tasks are never polled continuously.
        if (pendingLocalSendRef.current && !observedProcessing && !runtime.is_processing) {
          scheduleRetry(pollIntervalMs, { kind: 'heartbeat' });
        } else if (runtime.is_processing || runtime.has_task) {
          if (handoffPollsRemaining > 0) {
            // Keep a short bounded fallback for the startup handoff and the
            // first missed publications so a slow first token is visible.
            scheduleRetry(Math.min(pollIntervalMs * 2, 2000), {
              kind: 'heartbeat',
            });
          } else {
            // After the short fallback budget, read the authoritative runtime
            // at a bounded cadence. Include waiting-input states: resolving an
            // AskUser card can resume a detached runner while the terminal
            // realtime wake is unavailable, and a page that stops polling at
            // that boundary can remain visibly stuck after the runner ends.
            // This is not a per-token poll. Heartbeats update runtime only;
            // durable message history is refreshed at visible publications or
            // terminal state, so large conversations are not repeatedly read.
            if (scope !== 'stream' || !timer) {
              scheduleRetry(
                SYNON_BIOMED_CONVERSATION_LIVE_RECONCILE_INTERVAL_MS,
                { kind: 'heartbeat' },
                { keepAlive: true }
              );
            }
          }
        }

        if (
          readConversation &&
          !runtime.is_processing &&
          !runtime.has_task &&
          !(pendingLocalSendRef.current && !observedProcessing) &&
          (reachedTerminal || terminalMountNeedsReconciliation)
        ) {
          const terminalProjectionPending =
            getConversationRuntimeViewSnapshot(conversationId).terminalProjectionPending;
          if (terminalProjectionPending) {
            // The runner authority can become terminal just before the final
            // transcript annotation reaches the message window. Keep the
            // terminal reconciliation alive without replacing visible text;
            // MessageList clears this gate as soon as the durable terminal
            // assistant message is actually renderable.
            scheduleRetry(
              SYNON_BIOMED_CONVERSATION_TERMINAL_FALLBACK_GRACE_MS,
              { kind: 'reconcile', terminalBoundary: true },
              { keepAlive: true }
            );
            return;
          }
          // The in-app browser path may not receive the desktop IPC terminal
          // events that normally refresh the project artifact library and
          // sidebar read model. Reuse the existing refresh events so startup
          // failures cannot leave either surface visibly running.
          emitter.emit('acp.workspace.refresh');
          onSettledRef.current();
          observedProcessing = false;
        }
      } catch (error) {
        if (disposed) return;
        if (isMessageRequestAbort(error)) {
          // A concurrent canonical refresh superseded this request. This is
          // normal authority handoff, not service unavailability. Reconcile
          // again even after the short startup poll budget is exhausted so a
          // long-running or waiting-input task cannot stay visually stuck.
          scheduleRetry(
            pollIntervalMs,
            { kind: 'heartbeat' },
            {
              keepAlive:
                runtimeActiveRef.current || currentRuntime?.is_processing === true || currentRuntime?.has_task === true,
            }
          );
          return;
        }
        const diagnostic = redactErrorText(
          error instanceof Error ? error.message : String(error || 'conversation sync failed')
        );
        console.warn('[SynonBiomedConversationSync] Reconciliation failed:', diagnostic);
        consecutiveFailures += 1;
        const retryDelayMs = getSynonBiomedConversationRetryDelay(consecutiveFailures, pollIntervalMs);
        onIssueRef.current?.({
          kind: 'unavailable',
          retryDelayMs,
        });
        const terminalProjectionPending = getConversationRuntimeViewSnapshot(conversationId).terminalProjectionPending;
        scheduleRetry(
          retryDelayMs,
          terminalProjectionPending ? { kind: 'reconcile', terminalBoundary: true } : { kind: 'heartbeat' },
          { keepAlive: terminalProjectionPending }
        );
      }
    };

    const requestSync = (signal: SynonBiomedConversationInternalWakeSignal = { kind: 'reconcile' }) => {
      if (disposed) return;
      // Reconcile/heartbeat wakes supersede their scheduled read. Ordinary
      // stream wakes must not cancel the low-frequency authority heartbeat:
      // a busy text/artifact tail can otherwise postpone that read forever and
      // leave a terminal task visibly stuck in running state.
      if (timer && signal.kind !== 'stream') {
        clearTimeout(timer);
        timer = undefined;
      }
      // Keep the publication's semantic scope even while the route/runtime
      // snapshot is still hydrating. sync() already knows when it must read
      // the conversation to establish runtime authority. Promoting an
      // ordinary stream wake to "reconcile" here also promoted it to a full
      // message-history refresh, which repeatedly replaced the live text tail
      // during the first seconds of a newly created conversation.
      if (syncInFlight) {
        if (
          pendingSignal === null ||
          getSynonBiomedSyncSignalPriority(signal) > getSynonBiomedSyncSignalPriority(pendingSignal)
        ) {
          pendingSignal = signal;
        }
        return;
      }
      syncInFlight = true;
      void sync(signal).finally(() => {
        syncInFlight = false;
        if (pendingSignal === null || disposed) return;
        const nextSignal = pendingSignal;
        pendingSignal = null;
        requestSync(nextSignal);
      });
    };

    const unsubscribeWake = subscribeWake(conversationId, requestSync);
    const runtimeHandoffNeedsReconcile =
      (runtimeActiveRef.current || pendingLocalSendRef.current) && currentRuntime?.is_processing !== true;
    requestSync({
      kind: currentRuntime === null || !enabledRef.current || runtimeHandoffNeedsReconcile ? 'reconcile' : 'stream',
    });
    return () => {
      disposed = true;
      unsubscribeWake();
      if (timer) clearTimeout(timer);
    };
  }, [conversationId, backend, pollIntervalMs, subscribeWake, workspace]);
}
