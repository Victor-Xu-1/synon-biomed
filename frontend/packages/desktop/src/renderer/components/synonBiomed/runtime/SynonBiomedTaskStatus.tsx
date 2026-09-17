/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Spin, Tooltip, Trigger } from '@arco-design/web-react';
import { Attention, CheckOne, Close, CloseOne, PauseOne, Redo, Right } from '@icon-park/react';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { redactErrorText } from '@/renderer/pages/conversation/platforms/acp/errorDiagnostics';
import {
  isSynonBiomedLongTaskActive,
  projectSynonBiomedLongTaskStatus,
  type SynonBiomedLongTaskPhase,
  type SynonBiomedLongTaskStatus,
} from './longTaskStatusModel';
import type { SynonBiomedRuntimeFailureKind, SynonBiomedRuntimeSnapshot } from './runtimeOperationsModel';
import SynonBiomedTaskCenterPanel, { type SynonBiomedTaskCenterMetrics } from './SynonBiomedTaskCenterPanel';
import { useTaskCapsulePlacement } from './useTaskCapsulePlacement';
import './SynonBiomedTaskStatus.css';

export type SynonBiomedTaskStatusError = 'load' | 'refresh' | null;

type SynonBiomedTaskStatusProps = {
  snapshot: SynonBiomedRuntimeSnapshot | null;
  loading: boolean;
  snapshotError: SynonBiomedTaskStatusError;
  runtimeState?: string;
  terminalProjectionPending?: boolean;
  runtimeAuthorityUnavailable?: boolean;
  showLoadingStatus?: boolean;
  pausing?: boolean;
  resuming?: boolean;
  pendingInputCount?: number;
  taskCenterMetrics?: SynonBiomedTaskCenterMetrics;
  planAvailable?: boolean;
  recoveryModelLabel?: string | null;
  onResume?: () => void;
  onChooseModel?: () => void;
  onStop?: () => void | Promise<void>;
  onRefresh: () => void;
  onOpenReviewFindings?: () => void;
  onOpenPlan?: () => void;
  onOpenPendingInput?: () => void;
};

const RFC3339_TIMESTAMP = /^(\d{4})-(\d{2})-(\d{2})[Tt](\d{2}):(\d{2}):(\d{2})(?:\.\d+)?([Zz]|([+-])(\d{2}):(\d{2}))$/;

const parseRfc3339Timestamp = (value: string | null): number | null => {
  if (!value) return null;
  const match = RFC3339_TIMESTAMP.exec(value);
  if (!match) return null;
  const [, yearText, monthText, dayText, hourText, minuteText, secondText, zone, , offsetHourText, offsetMinuteText] =
    match;
  const year = Number(yearText);
  const month = Number(monthText);
  const day = Number(dayText);
  const hour = Number(hourText);
  const minute = Number(minuteText);
  const second = Number(secondText);
  const leapYear = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const daysInMonth = [31, leapYear ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
  if (month < 1 || month > 12 || day < 1 || day > daysInMonth[month - 1] || hour > 23 || minute > 59 || second > 59) {
    return null;
  }
  if (zone.toUpperCase() !== 'Z') {
    const offsetHour = Number(offsetHourText);
    const offsetMinute = Number(offsetMinuteText);
    if (offsetHour > 14 || offsetMinute > 59 || (offsetHour === 14 && offsetMinute !== 0)) return null;
  }
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? timestamp : null;
};

const elapsedMilliseconds = (status: SynonBiomedLongTaskStatus, now: number): number | null => {
  if (status.elapsedMs !== null) {
    if (!status.terminal && status.elapsedActive) {
      const observedAt = parseRfc3339Timestamp(status.elapsedObservedAt);
      return observedAt === null ? status.elapsedMs : status.elapsedMs + Math.max(0, now - observedAt);
    }
    return status.elapsedMs;
  }
  const start = parseRfc3339Timestamp(status.startedAt);
  const end = status.terminal ? parseRfc3339Timestamp(status.completedAt ?? status.updatedAt) : now;
  return start !== null && end !== null && end >= start ? end - start : null;
};

const taskElapsedMilliseconds = (status: SynonBiomedLongTaskStatus, now: number): number | null => {
  if (status.taskElapsedMs !== null) {
    if (!status.taskCompleted && !status.terminal && status.taskElapsedActive) {
      const observedAt = parseRfc3339Timestamp(status.taskElapsedObservedAt);
      return observedAt === null ? status.taskElapsedMs : status.taskElapsedMs + Math.max(0, now - observedAt);
    }
    return status.taskElapsedMs;
  }
  const start = parseRfc3339Timestamp(status.taskStartedAt);
  const end =
    status.taskCompleted || status.terminal ? parseRfc3339Timestamp(status.taskCompletedAt ?? status.updatedAt) : now;
  return start !== null && end !== null && end >= start ? end - start : null;
};

const hasLiveElapsedClock = (status: SynonBiomedLongTaskStatus): boolean => {
  if (status.taskCompleted || status.terminal) return false;
  if (status.taskElapsedMs !== null) {
    return status.taskElapsedActive === true && parseRfc3339Timestamp(status.taskElapsedObservedAt) !== null;
  }
  return parseRfc3339Timestamp(status.taskStartedAt) !== null;
};

const formatElapsed = (milliseconds: number): string => {
  const totalSeconds = Math.floor(milliseconds / 1_000);
  const hours = Math.floor(totalSeconds / 3_600);
  const minutes = Math.floor((totalSeconds % 3_600) / 60);
  const seconds = totalSeconds % 60;
  const minuteSecond = `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`;
  return hours > 0 ? `${String(hours).padStart(2, '0')}:${minuteSecond}` : minuteSecond;
};

const padTimestampPair = (part: number): string => String(part).padStart(2, '0');

const formatTaskTimestamp = (value: string | null): string | null => {
  const timestamp = parseRfc3339Timestamp(value);
  if (timestamp === null) return null;
  const date = new Date(timestamp);
  return `${padTimestampPair(date.getMonth() + 1)}/${padTimestampPair(date.getDate())} ${padTimestampPair(
    date.getHours()
  )}:${padTimestampPair(date.getMinutes())}:${padTimestampPair(date.getSeconds())}`;
};

const titleKeys: Record<SynonBiomedLongTaskPhase, string> = {
  queued: 'conversation.synonRuntime.runtimeOperations.taskQueued',
  starting: 'conversation.synonRuntime.runtimeOperations.taskStarting',
  finalizing: 'conversation.synonRuntime.runtimeOperations.taskFinalizing',
  running: 'conversation.synonRuntime.runtimeOperations.taskRunning',
  reviewing: 'conversation.synonRuntime.runtimeOperations.taskReviewing',
  scientific_reviewing: 'conversation.synonRuntime.runtimeOperations.taskScientificReviewing',
  waiting_input: 'conversation.synonRuntime.runtimeOperations.taskNeedsAction',
  waiting_approval: 'conversation.synonRuntime.runtimeOperations.taskWaitingApproval',
  paused: 'conversation.synonRuntime.runtimeOperations.taskPaused',
  cancelling: 'conversation.synonRuntime.runtimeOperations.taskCancelling',
  completed: 'conversation.synonRuntime.runtimeOperations.taskCompleted',
  failed: 'conversation.synonRuntime.runtimeOperations.taskFailed',
  cancelled: 'conversation.synonRuntime.runtimeOperations.taskCancelled',
};

const failureKindKeys: Partial<Record<NonNullable<SynonBiomedRuntimeFailureKind>, string>> = {
  safety_refusal: 'conversation.synonRuntime.runtimeOperations.safetyPaused',
  model_not_found: 'conversation.synonRuntime.runtimeOperations.modelUnavailable',
  model_overloaded: 'conversation.synonRuntime.runtimeOperations.modelBusy',
  service_interrupted: 'conversation.synonRuntime.runtimeOperations.runtimeInterrupted',
  not_found: 'conversation.synonRuntime.runtimeOperations.failureNotFound',
  unauthorized: 'conversation.synonRuntime.runtimeOperations.failureUnauthorized',
  rate_limited: 'conversation.synonRuntime.runtimeOperations.failureRateLimited',
  quota_exhausted: 'conversation.synonRuntime.runtimeOperations.failureQuotaExhausted',
  invalid_request: 'conversation.synonRuntime.runtimeOperations.failureInvalidRequest',
  transient: 'conversation.synonRuntime.runtimeOperations.failureTransient',
  image_build_failed: 'conversation.synonRuntime.runtimeOperations.failureImageBuildFailed',
  network_denied: 'conversation.synonRuntime.runtimeOperations.failureNetworkDenied',
  network_bridge_down: 'conversation.synonRuntime.runtimeOperations.failureNetworkBridgeDown',
  ownership_mismatch: 'conversation.synonRuntime.runtimeOperations.failureOwnershipMismatch',
  provider_degraded: 'conversation.synonRuntime.runtimeOperations.failureProviderDegraded',
  result_rejected: 'conversation.synonRuntime.runtimeOperations.failureResultRejected',
};

const ACTIVE_RUNTIME_STATES = new Set([
  'running',
  'starting',
  'waiting_approval',
  'waiting_confirmation',
  'waiting_input',
  'cancelling',
]);

const StatusIcon: React.FC<{
  phase: SynonBiomedLongTaskPhase | null;
  busy: boolean;
  reviewNeedsAttention?: boolean;
}> = ({ phase, busy, reviewNeedsAttention = false }) => {
  if (busy) {
    return (
      <span className='synon-biomed-task-center__icon' data-testid='synon-biomed-task-status-icon' aria-hidden='true'>
        <span className='synon-biomed-task-center__spinner' data-testid='synon-biomed-task-status-spinner'>
          <Spin size={14} />
        </span>
      </span>
    );
  }
  return (
    <span className='synon-biomed-task-center__icon' data-testid='synon-biomed-task-status-icon' aria-hidden='true'>
      {phase === 'failed' || phase === null || reviewNeedsAttention ? <Attention theme='outline' size={16} /> : null}
      {phase === 'cancelled' ? <CloseOne theme='outline' size={16} /> : null}
      {phase === 'completed' && !reviewNeedsAttention ? <CheckOne theme='outline' size={16} /> : null}
      {phase !== 'failed' &&
      phase !== null &&
      phase !== 'cancelled' &&
      phase !== 'completed' &&
      !reviewNeedsAttention ? (
        <PauseOne theme='outline' size={16} />
      ) : null}
    </span>
  );
};

const SynonBiomedTaskStatus: React.FC<SynonBiomedTaskStatusProps> = ({
  snapshot,
  loading,
  snapshotError,
  runtimeState = 'idle',
  terminalProjectionPending = false,
  runtimeAuthorityUnavailable = false,
  showLoadingStatus = false,
  pausing = false,
  resuming = false,
  pendingInputCount = 0,
  taskCenterMetrics,
  planAvailable = false,
  recoveryModelLabel = null,
  onResume,
  onChooseModel,
  onStop,
  onOpenReviewFindings,
  onRefresh,
  onOpenPlan,
  onOpenPendingInput,
}) => {
  const { t } = useTranslation();
  const [now, setNow] = useState(() => Date.now());
  const [detailsOpen, setDetailsOpen] = useState(false);
  const closeDetailsForDrag = useCallback(() => setDetailsOpen(false), []);
  const { capsuleRef, capsuleStyle, minimized, setMinimized, dragging, detached, capsulePointerProps } =
    useTaskCapsulePlacement(snapshot?.rootFrameId ?? snapshot?.frameId, closeDetailsForDrag);
  const projected = useMemo(() => (snapshot ? projectSynonBiomedLongTaskStatus(snapshot) : null), [snapshot]);
  const status = projected?.ok ? projected.value : null;
  const failureKindKey = snapshot?.failureKind ? failureKindKeys[snapshot.failureKind] : undefined;
  const failureKindLabel = failureKindKey ? t(failureKindKey) : null;
  const publicFailureLabel =
    failureKindLabel ??
    (snapshot?.status === 'failed' ? t('conversation.synonRuntime.runtimeOperations.taskFailed') : null);
  const guidedFailureDetail =
    snapshot?.failureKind === 'model_not_found'
      ? recoveryModelLabel
        ? t('conversation.synonRuntime.runtimeOperations.modelUnavailableWithRecovery', {
            model: recoveryModelLabel,
          })
        : t('conversation.synonRuntime.runtimeOperations.modelUnavailableDescription')
      : snapshot?.failureKind === 'safety_refusal'
        ? recoveryModelLabel
          ? t('conversation.synonRuntime.runtimeOperations.safetyPausedWithRecovery', {
              model: recoveryModelLabel,
            })
          : t('conversation.synonRuntime.runtimeOperations.safetyPausedDescription')
        : snapshot?.failureKind === 'service_interrupted'
          ? t('conversation.synonRuntime.runtimeOperations.runtimeInterruptedDescription')
          : snapshot?.failureKind === 'model_overloaded'
            ? t('conversation.synonRuntime.runtimeOperations.modelBusyDescription')
            : null;
  // Runtime reason codes and backend diagnostics remain available to recovery
  // logic, but are not user-facing copy. The capsule presents the closed,
  // localized failure category from the authoritative runtime projection.
  const failureDetailSource = guidedFailureDetail ?? publicFailureLabel;
  const failureDetail = failureDetailSource ? redactErrorText(failureDetailSource).trim().slice(0, 500) || null : null;
  const unsupported = Boolean(snapshot && projected && !projected.ok);
  const busy = loading && !snapshot;
  const phase = status?.phase ?? null;
  const specializedFailure =
    snapshot?.failureKind === 'model_not_found' ||
    snapshot?.failureKind === 'safety_refusal' ||
    snapshot?.failureKind === 'model_overloaded' ||
    snapshot?.failureKind === 'service_interrupted';
  const recoveryResumeLabel =
    snapshot?.failureKind === 'model_overloaded'
      ? t('conversation.synonRuntime.runtimeOperations.retryLater')
      : (snapshot?.failureKind === 'model_not_found' || snapshot?.failureKind === 'safety_refusal') &&
          recoveryModelLabel
        ? t('conversation.synonRuntime.runtimeOperations.continueWithModel', { model: recoveryModelLabel })
        : t('conversation.synonRuntime.runtimeOperations.continueRunning');
  const chooseModelAction =
    (snapshot?.failureKind === 'model_not_found' || snapshot?.failureKind === 'safety_refusal') && onChooseModel
      ? onChooseModel
      : undefined;
  const taskActive = status ? isSynonBiomedLongTaskActive(status) : false;
  // A cached active snapshot is not proof of a live task after the runtime
  // authority becomes unreachable. Keep the capsule in a neutral reconnecting
  // state until a fresh authoritative snapshot arrives instead of continuing
  // to present the stale phase as "running".
  const authorityPending =
    !busy &&
    snapshotError !== null &&
    (runtimeAuthorityUnavailable || ACTIVE_RUNTIME_STATES.has(runtimeState) || taskActive);
  const resumableRun = Boolean(status?.canResume && onResume);
  const waitingForModel = status?.phase === 'paused' && status.failureReason === 'model_provider_unavailable';
  const resumablePause =
    resumableRun && !waitingForModel && (status?.phase === 'cancelled' || status?.phase === 'paused');
  const reviewInterrupted = Boolean(
    status?.taskCompleted && ['failed', 'cancelled', 'canceled'].includes(status.reviewStatus ?? '')
  );
  const reviewHasFindings = Boolean(
    status?.taskCompleted && status.reviewStatus === 'completed' && status.reviewVerdict === 'revise'
  );
  const reviewHasWarnings = Boolean(
    status?.taskCompleted && status.reviewStatus === 'completed' && status.reviewVerdict === 'pass_with_warnings'
  );
  const reviewCompleted = Boolean(
    status?.taskCompleted &&
    status.reviewStatus === 'completed' &&
    status.reviewVerdict !== 'revise' &&
    status.reviewVerdict !== 'pass_with_warnings'
  );
  const reviewNeedsAttention = reviewInterrupted || reviewHasFindings || reviewHasWarnings;
  const reviewFindingsAvailable = Boolean(
    status && status.reviewIssueCount > 0 && (reviewHasFindings || reviewHasWarnings) && onOpenReviewFindings
  );
  const reviewAvailable = Boolean(
    status?.taskCompleted && status.reviewStatus === 'completed' && snapshot?.reviewFrameId && onOpenReviewFindings
  );
  const pausePending = pausing && taskActive;
  const inlinePause = Boolean(status?.canCancel && status.phase !== 'cancelling' && onStop && !pausing);

  useEffect(() => {
    setNow(Date.now());
    if (!status || !hasLiveElapsedClock(status)) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1_000);
    return () => window.clearInterval(timer);
  }, [
    status?.frameId,
    status?.startedAt,
    status?.terminal,
    status?.elapsedMs,
    status?.elapsedObservedAt,
    status?.elapsedActive,
    status?.taskStartedAt,
    status?.taskCompletedAt,
    status?.taskElapsedMs,
    status?.taskElapsedObservedAt,
    status?.taskElapsedActive,
  ]);

  const latestElapsed = status ? elapsedMilliseconds(status, now) : null;
  const taskElapsed = status ? taskElapsedMilliseconds(status, now) : null;
  let title = t('conversation.synonRuntime.sendBox.runtimeStatusUnavailable');
  let dataState = phase ?? 'unavailable';
  if (busy || authorityPending) {
    title = t('conversation.synonRuntime.runtimeOperations.taskStatusLoading');
    dataState = 'loading';
  } else if (terminalProjectionPending) {
    title = t('conversation.synonRuntime.runtimeOperations.taskFinalizing');
    dataState = 'finalizing';
  } else if (unsupported) {
    title = t('conversation.synonRuntime.runtimeOperations.runtimeStatusUnsupported');
  } else if (pausePending) {
    title = t('conversation.synonRuntime.runtimeOperations.taskCancelling');
    dataState = 'cancelling';
  } else if (status) {
    const reviewRunning = status.phase === 'reviewing' || status.phase === 'scientific_reviewing';
    if (reviewInterrupted) {
      title = t('conversation.synonRuntime.runtimeOperations.taskCompletedReviewInterrupted');
      dataState = 'completed-review-interrupted';
    } else if (reviewHasFindings) {
      title = t('conversation.synonRuntime.runtimeOperations.taskCompletedReviewFindings', {
        count: status.reviewIssueCount,
      });
      dataState = 'completed-review-findings';
    } else if (reviewHasWarnings) {
      title = t('conversation.synonRuntime.runtimeOperations.taskCompletedReviewWarnings', {
        count: status.reviewIssueCount,
      });
      dataState = 'completed-review-warnings';
    } else if (reviewCompleted) {
      title = t('conversation.synonRuntime.runtimeOperations.taskCompletedReviewCompleted');
      dataState = 'completed-reviewed';
    } else if (reviewRunning && status.reviewTrigger === 'manual') {
      title = t('conversation.synonRuntime.runtimeOperations.taskManualReviewing');
    } else if (reviewRunning && status.reviewTrigger === 'auto') {
      title = t('conversation.synonRuntime.runtimeOperations.taskAutoReviewing');
    } else if (phase === 'failed' && specializedFailure && failureKindLabel) {
      title = failureKindLabel;
    } else if (waitingForModel) {
      title = t('conversation.synonRuntime.runtimeOperations.taskWaitingModel');
      dataState = 'waiting_input';
    } else if (resumablePause) {
      title = t('conversation.synonRuntime.runtimeOperations.taskPaused');
    } else {
      title = t(titleKeys[status.phase]);
    }
  }
  const failure = phase === 'failed' || unsupported || (!snapshot && !busy && !authorityPending);
  const elapsedValue = taskElapsed === null ? null : formatElapsed(taskElapsed);
  const taskElapsedValue = taskElapsed === null ? null : formatElapsed(taskElapsed);
  const latestElapsedValue = latestElapsed === null ? null : formatElapsed(latestElapsed);
  const startedAtValue = formatTaskTimestamp(status?.taskStartedAt ?? null);
  const finishedAtValue = formatTaskTimestamp(status?.taskCompletedAt ?? null);
  const elapsedLabel =
    taskElapsed === null
      ? null
      : t('conversation.synonRuntime.runtimeOperations.taskTotalElapsed', { duration: elapsedValue });
  const attentionCount = pendingInputCount + (snapshot?.planApproval ? 1 : 0);
  const secondaryLabel =
    attentionCount > 0
      ? t('conversation.synonRuntime.runtimeOperations.taskPendingCount', { count: attentionCount })
      : phase === 'failed' && publicFailureLabel
        ? specializedFailure
          ? elapsedLabel
          : publicFailureLabel
        : elapsedLabel;
  const taskCenterAvailable = !busy && Boolean(status || unsupported || authorityPending || failure);

  useEffect(() => {
    setDetailsOpen(false);
  }, [snapshot?.frameId]);

  // Initial hydration has no authoritative task state yet. Do not paint a
  // synthetic status capsule that flashes during refresh; real runtime states
  // render as soon as the authority responds.
  if (busy && !showLoadingStatus) return null;

  const statusIcon = (
    <StatusIcon
      phase={phase}
      busy={busy || authorityPending || terminalProjectionPending || resuming || taskActive || pausePending}
      reviewNeedsAttention={reviewNeedsAttention}
    />
  );

  return (
    <div
      className='synon-biomed-task-center mb-8px'
      data-testid='synon-biomed-runtime-status'
      data-state={dataState}
      data-detached={detached ? 'true' : 'false'}
      data-minimized={minimized ? 'true' : 'false'}
      data-failure-kind={snapshot?.failureKind ?? undefined}
      aria-label={t('conversation.synonRuntime.runtimeOperations.runtimeControls')}
    >
      <div
        ref={capsuleRef}
        className={`synon-biomed-task-center__pill${minimized ? ' synon-biomed-task-center__pill--minimized' : ''}`}
        data-testid='synon-biomed-task-status-indicator'
        data-state={resumablePause ? 'paused' : dataState}
        data-open={detailsOpen ? 'true' : 'false'}
        data-dragging={dragging ? 'true' : 'false'}
        data-detached={detached ? 'true' : 'false'}
        data-minimized={minimized ? 'true' : 'false'}
        style={capsuleStyle}
        role={failure ? 'alert' : 'status'}
        aria-live={failure ? 'assertive' : 'polite'}
        aria-atomic='true'
        {...capsulePointerProps}
      >
        {minimized ? (
          <Tooltip content={title} position='top' mini disabled={dragging}>
            <button
              type='button'
              className='synon-biomed-task-center__orb'
              data-testid='synon-biomed-task-status-restore'
              aria-label={t('conversation.synonRuntime.runtimeOperations.restoreTaskStatus', { status: title })}
              onClick={() => setMinimized(false)}
            >
              {statusIcon}
            </button>
          </Tooltip>
        ) : taskCenterAvailable ? (
          <Trigger
            className='synon-biomed-task-center-popover'
            trigger='click'
            position='top'
            popupVisible={detailsOpen}
            onVisibleChange={setDetailsOpen}
            clickToClose
            clickOutsideToClose
            escToClose
            containerScrollToClose
            updateOnScroll
            autoFitPosition={false}
            autoFixPosition
            popupAlign={{ top: 8 }}
            duration={120}
            unmountOnExit
            popup={() => (
              <SynonBiomedTaskCenterPanel
                phase={phase}
                statusTitle={title}
                startedAtValue={startedAtValue}
                finishedAtValue={finishedAtValue}
                taskElapsedValue={taskElapsedValue}
                latestElapsedValue={latestElapsedValue}
                metrics={taskCenterMetrics}
                planAvailable={planAvailable && Boolean(onOpenPlan)}
                planNeedsApproval={Boolean(snapshot?.planApproval && onOpenPlan)}
                reviewAvailable={reviewAvailable}
                reviewFindingsAvailable={reviewFindingsAvailable}
                reviewVerdict={status?.reviewVerdict ?? null}
                reviewIssueCount={status?.reviewIssueCount ?? 0}
                pendingInputCount={pendingInputCount}
                recoveryAvailable={failure || authorityPending || unsupported}
                recoveryResumeLabel={resumableRun ? recoveryResumeLabel : null}
                failureDetail={failureDetail}
                onClose={() => setDetailsOpen(false)}
                onRefresh={onRefresh}
                onResume={resumableRun ? onResume : undefined}
                onChooseModel={chooseModelAction}
                onOpenReviewFindings={onOpenReviewFindings}
                onOpenPlan={onOpenPlan}
                onOpenPendingInput={onOpenPendingInput}
              />
            )}
          >
            <button
              type='button'
              className='synon-biomed-task-center__summary-trigger'
              data-testid='synon-biomed-task-details-trigger'
              aria-haspopup='dialog'
              aria-expanded={detailsOpen}
              aria-label={t('conversation.synonRuntime.runtimeOperations.openTaskCenter', {
                status: title,
                detail: secondaryLabel ?? '',
              })}
              title={t('conversation.synonRuntime.runtimeOperations.taskCenterTitle')}
            >
              {statusIcon}
              <span className='synon-biomed-task-center__title' title={title}>
                {title}
              </span>
              {secondaryLabel ? (
                <span
                  className='synon-biomed-task-center__metric'
                  title={secondaryLabel}
                  data-testid={
                    phase === 'failed' && status?.failureReason
                      ? 'synon-biomed-task-failure-reason'
                      : secondaryLabel === elapsedLabel
                        ? 'synon-biomed-task-elapsed'
                        : undefined
                  }
                >
                  {secondaryLabel}
                </span>
              ) : null}
              <span
                className='synon-biomed-task-center__chevron'
                data-open={detailsOpen ? 'true' : 'false'}
                aria-hidden='true'
              >
                <Right theme='outline' size={12} />
              </span>
            </button>
          </Trigger>
        ) : (
          <div className='synon-biomed-task-center__summary-trigger synon-biomed-task-center__summary-trigger--static'>
            {statusIcon}
            <span className='synon-biomed-task-center__title' title={title}>
              {title}
            </span>
            {secondaryLabel ? (
              <span className='synon-biomed-task-center__metric' title={secondaryLabel}>
                {secondaryLabel}
              </span>
            ) : null}
          </div>
        )}
        {!minimized && resumableRun ? (
          <button
            type='button'
            className='synon-biomed-task-center__control'
            data-testid='synon-biomed-task-status-action'
            data-task-capsule-action='true'
            aria-label={recoveryResumeLabel}
            title={recoveryResumeLabel}
            disabled={resuming}
            onClick={() => void onResume?.()}
          >
            <Redo theme='outline' size={14} aria-hidden='true' />
          </button>
        ) : !minimized && inlinePause ? (
          <button
            type='button'
            className='synon-biomed-task-center__control'
            data-testid='synon-biomed-task-pause'
            data-task-capsule-action='true'
            aria-label={t('conversation.synonRuntime.runtimeOperations.pauseTask')}
            title={t('conversation.synonRuntime.runtimeOperations.pauseTask')}
            onClick={() => void onStop?.()}
          >
            <Close theme='outline' size={14} aria-hidden='true' />
          </button>
        ) : null}
      </div>
    </div>
  );
};

export default SynonBiomedTaskStatus;
