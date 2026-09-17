import type { TConversationRuntimeSummary } from '@/common/config/storage';
import { useLayoutContext } from '@/renderer/hooks/context/LayoutContext';
import {
  approveSynonBiomedPlan,
  discardSynonBiomedPlan,
  loadSynonBiomedPlanDocument,
  loadSynonBiomedRuntimeSnapshot,
  resumeSynonBiomedFrame,
  resolveSynonBiomedAskUserRequest,
  resolveSynonBiomedInputRequest,
  subscribeSynonBiomedRuntimeInvalidation,
  type SynonBiomedRuntimeInvalidation,
} from '@/renderer/services/synonBiomedRuntimeOperations';
import { isSynonBiomedHttpError } from '@/renderer/services/synonBiomedHttp';
import { invalidateSynonBiomedFrameReads } from '@/renderer/services/synonBiomedFrameReads';
import {
  loadSynonBiomedFrameVerification,
  type SynonBiomedVerificationCheck,
} from '@/renderer/services/synonBiomedAnnotations';
import type {
  SynonBiomedApprovalScope,
  SynonBiomedPendingInputRequest,
  SynonBiomedPlanDocument,
  SynonBiomedRuntimeSnapshot,
  SynonBiomedRuntimeModelOption,
} from './runtimeOperationsModel';
import {
  chooseSynonBiomedRecoveryModel,
  doesSynonBiomedRuntimeSnapshotNeedActiveRefresh,
  toSynonAIRuntimeSummary,
} from './runtimeOperationsModel';
import type { SynonBiomedAskUserResponse } from '@/renderer/services/synonBiomedRuntimeOperations';
import { Message } from '@arco-design/web-react';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useAddEventListener } from '@/renderer/utils/emitter';
import { redactErrorText } from '@/renderer/pages/conversation/platforms/acp/errorDiagnostics';
import { useRealtime } from '@/renderer/hooks/context/RealtimeContext';
import type { SynonBiomedTaskStatusError } from './SynonBiomedTaskStatus';
import type { SynonBiomedTaskCenterMetrics } from './SynonBiomedTaskCenterPanel';
import SynonBiomedRuntimeDrawers from './SynonBiomedRuntimeDrawers';
import SynonBiomedRuntimeStatusSurface from './SynonBiomedRuntimeStatusSurface';
import {
  ACTIVE_RUNTIME_SNAPSHOT_INTERVAL_MS,
  ACTIVE_RUNTIME_STATES,
  TERMINAL_RUNTIME_STATUSES,
  isRuntimePauseTransitionSettled,
  offlineRecoveryDelay,
  projectAcceptedRuntimeStart,
  runtimeSummaryChanged,
} from './runtimeOperationsControllerModel';

type SynonBiomedRuntimeOperationsProps = {
  conversationId: string;
  runtimeState: string;
  terminalProjectionPending?: boolean;
  onResumeStarted?: () => void;
  onResumeFailed?: (reason: string) => void;
  onResumed: (turnId: string, runtime: TConversationRuntimeSummary) => void;
  onRuntimeUpdated?: (turnId: string, runtime: TConversationRuntimeSummary, source: 'initial' | 'refresh') => void;
  runtimeAuthorityUnavailable?: boolean;
  onRetryRuntimeAuthority?: () => void;
  onStop?: () => void | Promise<void>;
  onReviewActivityChange?: (active: boolean) => void;
  onRepairAndRegenerate?: (checks: SynonBiomedVerificationCheck[]) => void | Promise<void>;
  taskCenterMetrics?: SynonBiomedTaskCenterMetrics;
  modelInfo?: {
    currentModelId: string | null;
    availableModels: SynonBiomedRuntimeModelOption[];
  } | null;
  onChooseModel?: () => void;
};

const SynonBiomedRuntimeOperationsController: React.FC<SynonBiomedRuntimeOperationsProps> = ({
  conversationId,
  runtimeState,
  terminalProjectionPending = false,
  onResumeStarted,
  onResumeFailed,
  onResumed,
  onRuntimeUpdated,
  runtimeAuthorityUnavailable = false,
  onRetryRuntimeAuthority,
  onStop,
  onReviewActivityChange,
  onRepairAndRegenerate,
  taskCenterMetrics,
  modelInfo,
  onChooseModel,
}) => {
  const layout = useLayoutContext();
  const { snapshot: realtimeSnapshot } = useRealtime();
  const { t } = useTranslation();
  const [snapshot, setSnapshot] = useState<SynonBiomedRuntimeSnapshot | null>(null);
  const snapshotRef = useRef<SynonBiomedRuntimeSnapshot | null>(null);
  const publishedRuntimeRef = useRef<TConversationRuntimeSummary | null>(null);
  const acceptedStartPendingRef = useRef(runtimeState === 'starting' || runtimeState === 'running');
  const previousRuntimeAuthorityRef = useRef({ conversationId, runtimeState });
  const onRuntimeUpdatedRef = useRef(onRuntimeUpdated);
  onRuntimeUpdatedRef.current = onRuntimeUpdated;
  const onReviewActivityChangeRef = useRef(onReviewActivityChange);
  onReviewActivityChangeRef.current = onReviewActivityChange;
  const conversationAuthorityRef = useRef(conversationId);
  conversationAuthorityRef.current = conversationId;
  const pendingInputRef = useRef<HTMLDivElement>(null);
  const loadedConversationRef = useRef<string | null>(null);
  const snapshotRequestRef = useRef<{
    owner: string;
    revision: number;
    controller: AbortController;
    promise: Promise<SynonBiomedRuntimeSnapshot>;
  } | null>(null);
  const openPlanReviewRef = useRef<() => void>(() => {});
  const [loading, setLoading] = useState(true);
  const [ownerTransitionLoading, setOwnerTransitionLoading] = useState(false);
  const [snapshotError, setSnapshotError] = useState<SynonBiomedTaskStatusError>(null);
  const [snapshotRevision, setSnapshotRevision] = useState(0);
  const realtimeRefreshTimerRef = useRef<number | null>(null);
  const [resolvingRequestId, setResolvingRequestId] = useState<string | null>(null);
  const [reviewDrawerVisible, setReviewDrawerVisible] = useState(false);
  const reviewOwnerRef = useRef<string | null>(null);
  const reviewRequestRef = useRef(0);
  const reviewControllerRef = useRef<AbortController | null>(null);
  const [reviewChecks, setReviewChecks] = useState<SynonBiomedVerificationCheck[]>([]);
  const [reviewLoading, setReviewLoading] = useState(false);
  const [reviewError, setReviewError] = useState<string | null>(null);
  const [reviewTrigger, setReviewTrigger] = useState<'auto' | 'manual' | null>(null);
  const [reviewRepairing, setReviewRepairing] = useState(false);
  const [planDrawerVisible, setPlanDrawerVisible] = useState(false);
  const planOwnerRef = useRef<string | null>(null);
  const planRequestRef = useRef(0);
  const planControllerRef = useRef<AbortController | null>(null);
  const inputRequestRef = useRef(0);
  const inputControllerRef = useRef<AbortController | null>(null);
  const resumeRequestRef = useRef(0);
  const resumeControllerRef = useRef<AbortController | null>(null);
  const [resumeInFlight, setResumeInFlight] = useState(false);
  const [pauseInFlight, setPauseInFlight] = useState(false);
  const planActionRequestRef = useRef(0);
  const planActionControllerRef = useRef<AbortController | null>(null);
  const [planDocument, setPlanDocument] = useState<SynonBiomedPlanDocument | null>(null);
  const [planLoading, setPlanLoading] = useState(false);
  const [planError, setPlanError] = useState<string | null>(null);
  const [planAction, setPlanAction] = useState<'approve' | 'discard' | null>(null);
  const [messageApi, messageContextHolder] = Message.useMessage();
  const snapshotReportsActiveRuntime = doesSynonBiomedRuntimeSnapshotNeedActiveRefresh(snapshot);
  const shouldRefreshActiveRuntime = ACTIVE_RUNTIME_STATES.has(runtimeState) || snapshotReportsActiveRuntime;
  const applySnapshot = useCallback((nextSnapshot: SynonBiomedRuntimeSnapshot | null) => {
    snapshotRef.current = nextSnapshot;
    setSnapshot(nextSnapshot);
    onReviewActivityChangeRef.current?.(
      nextSnapshot?.runtimeStage === 'reviewing' || nextSnapshot?.runtimeStage === 'scientific_reviewing'
    );
  }, []);

  useEffect(() => {
    if (!snapshot || !resolvingRequestId) return;
    // A resumed runner may publish the next AskUser request before the
    // resolution request's finally block runs. Once the authoritative
    // snapshot no longer contains the request that was being submitted, that
    // request is durably settled and must not disable the new card.
    if (!snapshot.pendingInputRequests.some((request) => request.requestId === resolvingRequestId)) {
      setResolvingRequestId(null);
    }
  }, [resolvingRequestId, snapshot]);

  useEffect(() => {
    if (pauseInFlight && isRuntimePauseTransitionSettled(snapshot)) {
      setPauseInFlight(false);
    }
  }, [pauseInFlight, snapshot]);

  const requestSnapshot = useCallback((owner: string, revision: number): Promise<SynonBiomedRuntimeSnapshot> => {
    const current = snapshotRequestRef.current;
    if (current?.owner === owner && current.revision === revision) return current.promise;
    current?.controller.abort();
    const controller = new AbortController();
    const promise = Promise.resolve()
      .then(() => loadSynonBiomedRuntimeSnapshot(owner, { signal: controller.signal }))
      .finally(() => {
        if (snapshotRequestRef.current?.promise === promise) snapshotRequestRef.current = null;
      });
    snapshotRequestRef.current = { owner, revision, controller, promise };
    return promise;
  }, []);

  const applyAuthoritativeSnapshot = useCallback(
    (nextSnapshot: SynonBiomedRuntimeSnapshot, publishRuntime: boolean, source: 'initial' | 'refresh' = 'refresh') => {
      applySnapshot(nextSnapshot);
      let runtime: TConversationRuntimeSummary;
      try {
        runtime = toSynonAIRuntimeSummary(nextSnapshot);
      } catch (reason) {
        console.error(
          '[SynonBiomedRuntimeOperations] Failed to derive runtime summary',
          redactErrorText(reason instanceof Error ? reason.message : String(reason))
        );
        return;
      }
      const previous = publishedRuntimeRef.current;
      publishedRuntimeRef.current = runtime;
      if (publishRuntime && runtimeSummaryChanged(previous, runtime)) {
        onRuntimeUpdatedRef.current?.(runtime.turn_id ?? nextSnapshot.rootFrameId, runtime, source);
      }
    },
    [applySnapshot]
  );
  useEffect(() => {
    let active = true;
    const release = subscribeSynonBiomedRuntimeInvalidation(
      conversationId,
      (event?: SynonBiomedRuntimeInvalidation) => {
        if (!active) return;
        if (event?.terminal_status && TERMINAL_RUNTIME_STATUSES.has(event.terminal_status)) {
          if (realtimeRefreshTimerRef.current !== null) {
            window.clearTimeout(realtimeRefreshTimerRef.current);
            realtimeRefreshTimerRef.current = null;
          }
          setSnapshotRevision((value) => value + 1);
          return;
        }
        if (realtimeRefreshTimerRef.current !== null) return;
        realtimeRefreshTimerRef.current = window.setTimeout(() => {
          realtimeRefreshTimerRef.current = null;
          if (active) setSnapshotRevision((value) => value + 1);
        }, 80);
      }
    );
    return () => {
      active = false;
      release();
      if (realtimeRefreshTimerRef.current !== null) {
        window.clearTimeout(realtimeRefreshTimerRef.current);
        realtimeRefreshTimerRef.current = null;
      }
    };
  }, [conversationId]);

  useEffect(() => {
    let active = true;
    const initialLoad = loadedConversationRef.current !== conversationId;
    if (initialLoad) {
      setOwnerTransitionLoading(loadedConversationRef.current !== null);
      loadedConversationRef.current = conversationId;
      publishedRuntimeRef.current = null;
      applySnapshot(null);
      setLoading(true);
      planRequestRef.current += 1;
      planControllerRef.current?.abort();
      planControllerRef.current = null;
      planOwnerRef.current = null;
      inputRequestRef.current += 1;
      inputControllerRef.current?.abort();
      inputControllerRef.current = null;
      planActionRequestRef.current += 1;
      planActionControllerRef.current?.abort();
      planActionControllerRef.current = null;
      setResolvingRequestId(null);
      setPlanAction(null);
      resumeRequestRef.current += 1;
      resumeControllerRef.current?.abort();
      resumeControllerRef.current = null;
      setResumeInFlight(false);
      setPauseInFlight(false);
      setSnapshotError(null);
    }
    setPlanDrawerVisible(false);
    setPlanDocument(null);
    setPlanError(null);
    void requestSnapshot(conversationId, snapshotRevision)
      .then((nextSnapshot) => {
        if (active) {
          setSnapshotError(null);
          // The task snapshot is the transcript-aware authority. Publish the
          // first load too: the collection row can still carry a processing
          // Frame after the runner has entered an explicit paused boundary.
          applyAuthoritativeSnapshot(nextSnapshot, true, initialLoad ? 'initial' : 'refresh');
        }
      })
      .catch((reason: unknown) => {
        if (active) {
          console.error(
            '[SynonBiomedRuntimeOperations] Failed to load runtime snapshot',
            redactErrorText(reason instanceof Error ? reason.message : String(reason))
          );
          setSnapshotError(snapshotRef.current ? 'refresh' : 'load');
          if (snapshotRef.current === null) applySnapshot(null);
        }
      })
      .finally(() => {
        if (
          active &&
          conversationAuthorityRef.current === conversationId &&
          loadedConversationRef.current === conversationId
        ) {
          setLoading(false);
          setOwnerTransitionLoading(false);
        }
      });
    return () => {
      active = false;
    };
  }, [applyAuthoritativeSnapshot, applySnapshot, conversationId, requestSnapshot, snapshotRevision]);

  useEffect(() => {
    if (!['offline', 'reconnecting', 'protocol-error'].includes(realtimeSnapshot.status)) return;
    if (!shouldRefreshActiveRuntime) return;
    let active = true;
    let timer: number | null = null;
    let consecutiveFailures = 0;
    const schedule = () => {
      timer = window.setTimeout(() => {
        timer = null;
        void refresh();
      }, offlineRecoveryDelay(consecutiveFailures));
    };
    const refresh = async () => {
      invalidateSynonBiomedFrameReads(conversationId);
      void requestSnapshot(conversationId, snapshotRevision)
        .then((nextSnapshot) => {
          if (active) {
            consecutiveFailures = 0;
            setSnapshotError(null);
            applyAuthoritativeSnapshot(nextSnapshot, true);
          }
        })
        .catch((reason: unknown) => {
          if (!active) return;
          consecutiveFailures = Math.min(consecutiveFailures + 1, 8);
          console.error(
            '[SynonBiomedRuntimeOperations] Failed to refresh runtime snapshot',
            redactErrorText(reason instanceof Error ? reason.message : String(reason))
          );
          setSnapshotError('refresh');
        })
        .finally(() => {
          if (active) schedule();
        });
    };
    schedule();
    return () => {
      active = false;
      if (timer !== null) window.clearTimeout(timer);
    };
  }, [
    applyAuthoritativeSnapshot,
    conversationId,
    realtimeSnapshot.status,
    requestSnapshot,
    shouldRefreshActiveRuntime,
    snapshotRevision,
  ]);

  useEffect(() => {
    // The parent conversation gate can observe a terminal realtime event
    // before this component receives the corresponding frame snapshot. Keep
    // reconciling while either authority still reports an active task so the
    // capsule cannot remain stuck on "running" until a full page reload.
    if (!shouldRefreshActiveRuntime) return;
    if (['offline', 'reconnecting', 'protocol-error'].includes(realtimeSnapshot.status)) return;
    let active = true;
    let timer: number | null = null;
    const refresh = () => {
      invalidateSynonBiomedFrameReads(conversationId);
      void requestSnapshot(conversationId, snapshotRevision)
        .then((nextSnapshot) => {
          if (active) {
            setSnapshotError(null);
            applyAuthoritativeSnapshot(nextSnapshot, true);
          }
        })
        .catch((reason: unknown) => {
          if (!active) return;
          console.error(
            '[SynonBiomedRuntimeOperations] Active runtime heartbeat failed',
            redactErrorText(reason instanceof Error ? reason.message : String(reason))
          );
          setSnapshotError('refresh');
        })
        .finally(() => {
          if (active) {
            timer = window.setTimeout(refresh, ACTIVE_RUNTIME_SNAPSHOT_INTERVAL_MS);
          }
        });
    };
    timer = window.setTimeout(refresh, ACTIVE_RUNTIME_SNAPSHOT_INTERVAL_MS);
    return () => {
      active = false;
      if (timer !== null) window.clearTimeout(timer);
    };
  }, [
    applyAuthoritativeSnapshot,
    conversationId,
    realtimeSnapshot.status,
    requestSnapshot,
    shouldRefreshActiveRuntime,
    snapshotRevision,
  ]);

  useEffect(
    () => () => {
      snapshotRequestRef.current?.controller.abort();
      snapshotRequestRef.current = null;
      reviewControllerRef.current?.abort();
      reviewControllerRef.current = null;
      planControllerRef.current?.abort();
      planControllerRef.current = null;
      inputControllerRef.current?.abort();
      inputControllerRef.current = null;
      resumeControllerRef.current?.abort();
      resumeControllerRef.current = null;
      planActionControllerRef.current?.abort();
      planActionControllerRef.current = null;
    },
    []
  );

  const openReviewFindings = () => {
    const currentSnapshot = snapshotRef.current;
    const rootFrameId = currentSnapshot?.rootFrameId;
    if (!rootFrameId) {
      messageApi.info(t('conversation.synonRuntime.runtimeOperations.noReviewResult'));
      return;
    }
    const owner = conversationId;
    const request = ++reviewRequestRef.current;
    const reviewerFrameId = currentSnapshot.reviewFrameId;
    reviewControllerRef.current?.abort();
    const controller = new AbortController();
    reviewControllerRef.current = controller;
    reviewOwnerRef.current = owner;
    setReviewDrawerVisible(true);
    setReviewTrigger(currentSnapshot.reviewTrigger);
    setReviewChecks([]);
    setReviewLoading(true);
    setReviewError(null);
    const fetchWithSignal: typeof fetch = (input, init) => fetch(input, { ...init, signal: controller.signal });
    void loadSynonBiomedFrameVerification(rootFrameId, undefined, fetchWithSignal)
      .then((result) => {
        if (conversationAuthorityRef.current !== owner || reviewRequestRef.current !== request) return;
        const checks = reviewerFrameId
          ? result.checks.filter((check) => check.reviewerFrameId === reviewerFrameId)
          : result.checks;
        setReviewChecks(checks);
      })
      .catch((reason: unknown) => {
        if (conversationAuthorityRef.current !== owner || reviewRequestRef.current !== request) return;
        console.error(
          '[SynonBiomedRuntimeOperations] Failed to load review findings',
          redactErrorText(reason instanceof Error ? reason.message : String(reason))
        );
        setReviewError(t('conversation.synonRuntime.runtimeOperations.reviewResultLoadFailed'));
      })
      .finally(() => {
        if (conversationAuthorityRef.current === owner && reviewRequestRef.current === request) {
          if (reviewControllerRef.current === controller) reviewControllerRef.current = null;
          setReviewLoading(false);
        }
      });
  };

  const closeReviewFindings = () => {
    reviewRequestRef.current += 1;
    reviewOwnerRef.current = null;
    reviewControllerRef.current?.abort();
    reviewControllerRef.current = null;
    setReviewDrawerVisible(false);
    setReviewChecks([]);
    setReviewLoading(false);
    setReviewError(null);
    setReviewTrigger(null);
    setReviewRepairing(false);
  };

  const repairAndRegenerate = async () => {
    if (!onRepairAndRegenerate || reviewRepairing || reviewChecks.length === 0) return;
    setReviewRepairing(true);
    try {
      await onRepairAndRegenerate(reviewChecks);
      closeReviewFindings();
    } catch (reason) {
      console.error(
        '[SynonBiomedRuntimeOperations] Failed to repair and regenerate from review findings',
        redactErrorText(reason instanceof Error ? reason.message : String(reason))
      );
      messageApi.error(t('conversation.synonRuntime.runtimeOperations.repairAndRegenerateFailed'));
    } finally {
      setReviewRepairing(false);
    }
  };

  const refreshRuntime = () => {
    invalidateSynonBiomedFrameReads(conversationId);
    setSnapshotError(null);
    if (snapshotRef.current === null) setLoading(true);
    onRetryRuntimeAuthority?.();
    setSnapshotRevision((value) => value + 1);
  };

  const pauseTask = async () => {
    if (!onStop || pauseInFlight) return;
    const owner = conversationId;
    setPauseInFlight(true);
    try {
      await onStop();
      if (conversationAuthorityRef.current !== owner) return;
      invalidateSynonBiomedFrameReads(owner);
      setSnapshotRevision((value) => value + 1);
    } catch {
      // The owner handles the user-facing failure message. Keep this wrapper
      // settled so the icon-only control cannot create an unhandled rejection.
      if (conversationAuthorityRef.current === owner) {
        setPauseInFlight(false);
      }
    }
  };

  const resumeTask = async (modelOverride?: string) => {
    if (!snapshotRef.current?.canResume || resumeInFlight) return;
    const owner = conversationId;
    const operation = ++resumeRequestRef.current;
    resumeControllerRef.current?.abort();
    const controller = new AbortController();
    resumeControllerRef.current = controller;
    setResumeInFlight(true);
    onResumeStarted?.();
    try {
      // Ordinary continuation resolves the currently selected model at call
      // time. A typed unavailable/safety recovery may explicitly choose the
      // verified alternative shown to the user; both paths still use the same
      // backend resume authority.
      const result = await resumeSynonBiomedFrame(owner, modelOverride ? { model: modelOverride } : {}, {
        signal: controller.signal,
      });
      if (conversationAuthorityRef.current !== owner || resumeRequestRef.current !== operation) return;
      setSnapshotError(null);
      applyAuthoritativeSnapshot(result.snapshot, false);
      onResumed(result.snapshot.rootFrameId, result.runtime);
      messageApi.success(t('conversation.synonRuntime.runtimeOperations.resumeSucceeded'));
    } catch (reason) {
      if (conversationAuthorityRef.current !== owner || resumeRequestRef.current !== operation) return;
      console.error(
        '[SynonBiomedRuntimeOperations] Failed to resume task',
        redactErrorText(reason instanceof Error ? reason.message : String(reason))
      );
      onResumeFailed?.(reason instanceof Error ? reason.message : String(reason));
      messageApi.error(t('conversation.synonRuntime.runtimeOperations.resumeFailed'));
    } finally {
      if (conversationAuthorityRef.current === owner && resumeRequestRef.current === operation) {
        if (resumeControllerRef.current === controller) resumeControllerRef.current = null;
        setResumeInFlight(false);
      }
    }
  };

  const resolveInput = async (
    request: SynonBiomedPendingInputRequest,
    decision: 'allow' | 'deny',
    scope: SynonBiomedApprovalScope = 'once'
  ) => {
    if (inputControllerRef.current && !inputControllerRef.current.signal.aborted) return;
    const owner = conversationId;
    const operation = ++inputRequestRef.current;
    inputControllerRef.current?.abort();
    const controller = new AbortController();
    inputControllerRef.current = controller;
    setResolvingRequestId(request.requestId);
    onResumeStarted?.();
    try {
      const result = await resolveSynonBiomedInputRequest(conversationId, request, decision, scope, {
        signal: controller.signal,
      });
      if (conversationAuthorityRef.current !== owner || inputRequestRef.current !== operation) return;
      applyAuthoritativeSnapshot(result.snapshot, false);
      onResumed(result.snapshot.rootFrameId, result.runtime);
      const allowMessages: Record<SynonBiomedApprovalScope, string> = {
        once: t('conversation.synonRuntime.runtimeOperations.approvalAllowedOnce'),
        conversation: t('conversation.synonRuntime.runtimeOperations.approvalAllowedConversation'),
        project: t('conversation.synonRuntime.runtimeOperations.approvalAllowedProject'),
        always: t('conversation.synonRuntime.runtimeOperations.approvalAllowedAlways'),
      };
      messageApi.success(
        decision === 'allow' ? allowMessages[scope] : t('conversation.synonRuntime.runtimeOperations.approvalDenied')
      );
    } catch (reason) {
      if (conversationAuthorityRef.current !== owner || inputRequestRef.current !== operation) return;
      let reportedReason = reason;
      if (isSynonBiomedHttpError(reason) && reason.status === 409) {
        try {
          // A 409 means the visible approval request lost its execution
          // waiter (for example, the kernel completed or failed while the
          // card was on screen). Re-read authoritative state so a stale card
          // cannot trap the user behind a generic "submit failed" toast.
          invalidateSynonBiomedFrameReads(owner);
          const refreshedSnapshot = await loadSynonBiomedRuntimeSnapshot(owner, {
            signal: controller.signal,
          });
          if (conversationAuthorityRef.current !== owner || inputRequestRef.current !== operation) return;
          applyAuthoritativeSnapshot(refreshedSnapshot, false);
          onResumed(refreshedSnapshot.rootFrameId, toSynonAIRuntimeSummary(refreshedSnapshot));
          return;
        } catch (refreshReason) {
          reportedReason = refreshReason;
        }
      }
      console.error(
        '[SynonBiomedRuntimeOperations] Failed to resolve approval request',
        redactErrorText(reportedReason instanceof Error ? reportedReason.message : String(reportedReason))
      );
      onResumeFailed?.(reportedReason instanceof Error ? reportedReason.message : String(reportedReason));
      messageApi.error(t('conversation.synonRuntime.runtimeOperations.approvalSubmitFailed'));
    } finally {
      if (conversationAuthorityRef.current === owner && inputRequestRef.current === operation) {
        if (inputControllerRef.current === controller) inputControllerRef.current = null;
        setResolvingRequestId(null);
      }
    }
  };

  const resolveAskUser = async (request: SynonBiomedPendingInputRequest, response: SynonBiomedAskUserResponse) => {
    if (inputControllerRef.current && !inputControllerRef.current.signal.aborted) return;
    const owner = conversationId;
    const operation = ++inputRequestRef.current;
    inputControllerRef.current?.abort();
    const controller = new AbortController();
    inputControllerRef.current = controller;
    setResolvingRequestId(request.requestId);
    onResumeStarted?.();
    try {
      const result = await resolveSynonBiomedAskUserRequest(conversationId, request, response, {
        signal: controller.signal,
      });
      if (conversationAuthorityRef.current !== owner || inputRequestRef.current !== operation) return;
      applyAuthoritativeSnapshot(result.snapshot, false);
      onResumed(result.snapshot.rootFrameId, result.runtime);
      messageApi.success(
        response.action === 'cancel'
          ? t('conversation.synonRuntime.runtimeOperations.answerSkipped')
          : response.action === 'discuss'
            ? t('conversation.synonRuntime.runtimeOperations.discussionRequested')
            : response.action === 'decide_for_me'
              ? t('conversation.synonRuntime.runtimeOperations.delegatedDecision')
              : t('conversation.synonRuntime.runtimeOperations.answerSubmitted')
      );
    } catch (reason) {
      if (conversationAuthorityRef.current !== owner || inputRequestRef.current !== operation) return;
      console.error(
        '[SynonBiomedRuntimeOperations] Failed to resolve user question',
        redactErrorText(reason instanceof Error ? reason.message : String(reason))
      );
      onResumeFailed?.(reason instanceof Error ? reason.message : String(reason));
      messageApi.error(t('conversation.synonRuntime.runtimeOperations.answerSubmitFailed'));
    } finally {
      if (conversationAuthorityRef.current === owner && inputRequestRef.current === operation) {
        if (inputControllerRef.current === controller) inputControllerRef.current = null;
        setResolvingRequestId(null);
      }
    }
  };

  const openPlanReview = () => {
    const artifactId = snapshot?.taskPlan?.artifactId;
    if (!artifactId) {
      messageApi.info(t('conversation.synonRuntime.runtimeOperations.noPlanAvailable'));
      return;
    }
    const owner = conversationId;
    const request = ++planRequestRef.current;
    planControllerRef.current?.abort();
    const controller = new AbortController();
    planControllerRef.current = controller;
    planOwnerRef.current = owner;
    setPlanDrawerVisible(true);
    setPlanDocument(null);
    setPlanLoading(true);
    setPlanError(null);
    void loadSynonBiomedPlanDocument(artifactId, { signal: controller.signal })
      .then((document) => {
        if (conversationAuthorityRef.current === owner && planRequestRef.current === request) setPlanDocument(document);
      })
      .catch((reason: unknown) => {
        if (conversationAuthorityRef.current !== owner || planRequestRef.current !== request) return;
        console.error(
          '[SynonBiomedRuntimeOperations] Failed to load plan document',
          redactErrorText(reason instanceof Error ? reason.message : String(reason))
        );
        setPlanError(t('conversation.synonRuntime.runtimeOperations.planLoadFailed'));
      })
      .finally(() => {
        if (conversationAuthorityRef.current === owner && planRequestRef.current === request) {
          if (planControllerRef.current === controller) planControllerRef.current = null;
          setPlanLoading(false);
        }
      });
  };
  openPlanReviewRef.current = openPlanReview;
  const closePlanReview = () => {
    planRequestRef.current += 1;
    planOwnerRef.current = null;
    planControllerRef.current?.abort();
    planControllerRef.current = null;
    setPlanDrawerVisible(false);
    setPlanDocument(null);
    setPlanLoading(false);
    setPlanError(null);
  };
  useAddEventListener(
    'synonbiomed.runtime.plan.open',
    (frameId) => {
      if (frameId === conversationAuthorityRef.current) openPlanReviewRef.current();
    },
    []
  );

  const completePlanReview = async (action: 'approve' | 'discard') => {
    const owner = conversationId;
    const request = ++planActionRequestRef.current;
    planActionControllerRef.current?.abort();
    const controller = new AbortController();
    planActionControllerRef.current = controller;
    setPlanAction(action);
    onResumeStarted?.();
    try {
      const result =
        action === 'approve'
          ? await approveSynonBiomedPlan(conversationId, { signal: controller.signal })
          : await discardSynonBiomedPlan(conversationId, { signal: controller.signal });
      if (conversationAuthorityRef.current !== owner || planActionRequestRef.current !== request) return;
      applyAuthoritativeSnapshot(result.snapshot, false);
      setPlanDrawerVisible(false);
      onResumed(result.snapshot.rootFrameId, result.runtime);
      messageApi.success(
        action === 'approve'
          ? t('conversation.synonRuntime.runtimeOperations.planApproved')
          : t('conversation.synonRuntime.runtimeOperations.planDiscarded')
      );
    } catch (reason) {
      if (conversationAuthorityRef.current !== owner || planActionRequestRef.current !== request) return;
      console.error(
        '[SynonBiomedRuntimeOperations] Failed to complete plan review',
        redactErrorText(reason instanceof Error ? reason.message : String(reason))
      );
      onResumeFailed?.(reason instanceof Error ? reason.message : String(reason));
      messageApi.error(t('conversation.synonRuntime.runtimeOperations.planActionFailed'));
    } finally {
      if (conversationAuthorityRef.current === owner && planActionRequestRef.current === request) {
        if (planActionControllerRef.current === controller) planActionControllerRef.current = null;
        setPlanAction(null);
      }
    }
  };

  const ownsConversation = loadedConversationRef.current === conversationId;
  const visibleSnapshot = ownsConversation ? snapshot : null;
  const previousRuntimeAuthority = previousRuntimeAuthorityRef.current;
  if (previousRuntimeAuthority.conversationId !== conversationId) {
    acceptedStartPendingRef.current = runtimeState === 'starting' || runtimeState === 'running';
  } else if (
    (runtimeState === 'starting' || runtimeState === 'running') &&
    previousRuntimeAuthority.runtimeState !== 'starting' &&
    previousRuntimeAuthority.runtimeState !== 'running'
  ) {
    acceptedStartPendingRef.current = true;
  }
  if (
    visibleSnapshot &&
    visibleSnapshot.status !== 'completed' &&
    visibleSnapshot.status !== 'failed' &&
    visibleSnapshot.status !== 'cancelled'
  ) {
    acceptedStartPendingRef.current = false;
  } else if (visibleSnapshot && visibleSnapshot.runtimeAttempt !== null) {
    acceptedStartPendingRef.current = false;
  }
  previousRuntimeAuthorityRef.current = { conversationId, runtimeState };
  const statusSnapshot = projectAcceptedRuntimeStart(visibleSnapshot, acceptedStartPendingRef.current);
  const visibleTaskCenterMetrics: SynonBiomedTaskCenterMetrics = {
    total: {
      tokenUsage: statusSnapshot?.taskMetrics?.total?.totalTokens ?? taskCenterMetrics?.total.tokenUsage ?? null,
      modelCallCount:
        statusSnapshot?.taskMetrics?.total?.modelCallCount ?? taskCenterMetrics?.total.modelCallCount ?? null,
      toolCallCount:
        statusSnapshot?.taskMetrics?.total?.toolCallCount ?? taskCenterMetrics?.total.toolCallCount ?? null,
    },
    latest: {
      tokenUsage: statusSnapshot?.taskMetrics?.latest?.totalTokens ?? taskCenterMetrics?.latest.tokenUsage ?? null,
      modelCallCount:
        statusSnapshot?.taskMetrics?.latest?.modelCallCount ?? taskCenterMetrics?.latest.modelCallCount ?? null,
      toolCallCount:
        statusSnapshot?.taskMetrics?.latest?.toolCallCount ?? taskCenterMetrics?.latest.toolCallCount ?? null,
    },
  };
  const planAvailable = Boolean(visibleSnapshot?.taskPlan);
  const recoveryModel = visibleSnapshot
    ? chooseSynonBiomedRecoveryModel(
        visibleSnapshot.modelId,
        modelInfo?.currentModelId ?? null,
        modelInfo?.availableModels ?? []
      )
    : null;
  const requiresAlternativeModel =
    visibleSnapshot?.failureKind === 'model_not_found' || visibleSnapshot?.failureKind === 'safety_refusal';
  const recoveryResumeModelId = requiresAlternativeModel ? recoveryModel?.id : undefined;
  const recoveryCanResume = !requiresAlternativeModel || Boolean(recoveryResumeModelId);
  const visibleLoading = !ownsConversation || loading;
  const pendingRequest = visibleSnapshot?.pendingInputRequests[0];
  const askUserQuestion = pendingRequest?.questions[0] ?? null;
  const openPendingInput = () => {
    const target = pendingInputRef.current;
    if (!target) return;
    target.scrollIntoView?.({ behavior: 'smooth', block: 'nearest' });
    target.focus({ preventScroll: true });
  };

  return (
    <>
      {messageContextHolder}
      <SynonBiomedRuntimeStatusSurface
        snapshot={statusSnapshot}
        loading={visibleLoading}
        snapshotError={ownsConversation ? snapshotError : null}
        runtimeState={runtimeState}
        terminalProjectionPending={terminalProjectionPending}
        runtimeAuthorityUnavailable={runtimeAuthorityUnavailable}
        showLoadingStatus={ownerTransitionLoading}
        pausing={pauseInFlight}
        resuming={resumeInFlight}
        pendingInputCount={visibleSnapshot?.pendingInputRequests.length ?? 0}
        taskCenterMetrics={visibleTaskCenterMetrics}
        planAvailable={planAvailable}
        recoveryModelLabel={recoveryModel?.label ?? null}
        onResume={recoveryCanResume ? () => void resumeTask(recoveryResumeModelId) : undefined}
        onChooseModel={onChooseModel}
        onStop={onStop ? () => void pauseTask() : undefined}
        onRefresh={refreshRuntime}
        onOpenReviewFindings={openReviewFindings}
        onOpenPlan={planAvailable ? openPlanReview : undefined}
        onOpenPendingInput={pendingRequest ? openPendingInput : undefined}
        pendingRequest={pendingRequest}
        pendingInputRef={pendingInputRef}
        resolvingRequestId={resolvingRequestId}
        pendingAriaLabel={
          askUserQuestion
            ? t('conversation.synonRuntime.runtimeOperations.waitingForAnswer')
            : t('conversation.synonRuntime.runtimeOperations.taskWaitingApproval')
        }
        onResolveAskUser={resolveAskUser}
        onResolveInput={resolveInput}
      />
      <SynonBiomedRuntimeDrawers
        mobile={Boolean(layout?.isMobile)}
        reviewTitle={
          reviewTrigger === 'manual'
            ? t('conversation.synonRuntime.runtimeOperations.manualReviewResult')
            : t('conversation.synonRuntime.runtimeOperations.reviewResult')
        }
        reviewVisible={reviewDrawerVisible && reviewOwnerRef.current === conversationId}
        reviewChecks={reviewChecks}
        reviewLoading={reviewLoading}
        reviewError={reviewError}
        reviewRepairing={reviewRepairing}
        onCloseReview={closeReviewFindings}
        onRetryReview={openReviewFindings}
        onRepairAndRegenerate={onRepairAndRegenerate ? repairAndRegenerate : undefined}
        planTitle={t('conversation.synonRuntime.runtimeOperations.taskPlan')}
        planVisible={planDrawerVisible && planOwnerRef.current === conversationId}
        planDocument={planDocument}
        planReference={visibleSnapshot?.taskPlan ?? null}
        planLoading={planLoading}
        planError={planError}
        planApprovalAvailable={Boolean(snapshot?.planApproval)}
        planAction={planAction}
        returnPlanLabel={t('conversation.synonRuntime.runtimeOperations.returnPlan')}
        approvePlanLabel={t('conversation.synonRuntime.runtimeOperations.approveAndExecute')}
        onClosePlan={closePlanReview}
        onRetryPlan={openPlanReview}
        onCompletePlan={(action) => void completePlanReview(action)}
      />
    </>
  );
};

export default SynonBiomedRuntimeOperationsController;
