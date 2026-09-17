/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Attention, CheckOne, CloseOne, PauseOne, Right } from '@icon-park/react';
import React from 'react';
import { useTranslation } from 'react-i18next';
import type { SynonBiomedLongTaskPhase } from './longTaskStatusModel';
import type { SynonBiomedReviewVerdict } from './runtimeOperationsModel';

export type SynonBiomedTaskCenterMetricScope = {
  tokenUsage: number | null;
  modelCallCount: number | null;
  toolCallCount: number | null;
};

export type SynonBiomedTaskCenterMetrics = {
  total: SynonBiomedTaskCenterMetricScope;
  latest: SynonBiomedTaskCenterMetricScope;
};

type SynonBiomedTaskCenterPanelProps = {
  phase: SynonBiomedLongTaskPhase | null;
  statusTitle: string;
  startedAtValue: string | null;
  finishedAtValue: string | null;
  taskElapsedValue: string | null;
  latestElapsedValue: string | null;
  metrics?: SynonBiomedTaskCenterMetrics;
  planAvailable: boolean;
  planNeedsApproval: boolean;
  reviewAvailable: boolean;
  reviewFindingsAvailable: boolean;
  reviewVerdict?: SynonBiomedReviewVerdict;
  reviewIssueCount?: number;
  pendingInputCount?: number;
  recoveryAvailable?: boolean;
  recoveryResumeLabel?: string | null;
  failureDetail?: string | null;
  onClose: () => void;
  onRefresh?: () => void;
  onResume?: () => void;
  onChooseModel?: () => void;
  onOpenReviewFindings?: () => void;
  onOpenPlan?: () => void;
  onOpenPendingInput?: () => void;
};

type TaskCenterActionRowProps = {
  label: string;
  meta?: string | null;
  count?: number;
  attention?: boolean;
  testId?: string;
  onClick: () => void;
};

type TaskCenterMetricProps = {
  label: string;
  value: string;
  detail: string;
  title?: string;
  testId: string;
};

type TaskCenterRecordRowProps = {
  label: string;
  meta: string;
  testId: string;
};

const StatusGlyph: React.FC<{ phase: SynonBiomedLongTaskPhase | null }> = ({ phase }) => {
  if (phase === 'completed') return <CheckOne theme='outline' size={16} aria-hidden='true' />;
  if (phase === 'failed') return <Attention theme='outline' size={16} aria-hidden='true' />;
  if (phase === 'cancelled') return <CloseOne theme='outline' size={16} aria-hidden='true' />;
  return <PauseOne theme='outline' size={16} aria-hidden='true' />;
};

const TaskCenterMetric: React.FC<TaskCenterMetricProps> = ({ label, value, detail, title, testId }) => (
  <div className='synon-biomed-task-center-panel__metric-card' data-testid={testId} title={title}>
    <dt>{label}</dt>
    <dd>{value}</dd>
    <span>{detail}</span>
  </div>
);

const formatCompactTokenUsage = (value: number | null, locale: string): string | null => {
  if (value === null || !Number.isFinite(value) || value < 0) return null;
  const formatter = new Intl.NumberFormat(locale, { maximumFractionDigits: 1 });
  if (value >= 1_000_000) return `${formatter.format(value / 1_000_000)}M`;
  if (value >= 1_000) return `${formatter.format(value / 1_000)}K`;
  return formatter.format(value);
};

const TaskCenterActionRow: React.FC<TaskCenterActionRowProps> = ({
  label,
  meta,
  count,
  attention = false,
  testId,
  onClick,
}) => (
  <button
    type='button'
    className={`synon-biomed-task-center-panel__action${attention ? ' synon-biomed-task-center-panel__action--attention' : ''}`}
    data-testid={testId}
    onClick={onClick}
  >
    <span className='synon-biomed-task-center-panel__action-copy'>
      <span className='synon-biomed-task-center-panel__action-label'>{label}</span>
      {meta ? <span className='synon-biomed-task-center-panel__action-meta'>{meta}</span> : null}
    </span>
    {typeof count === 'number' && count > 0 ? (
      <span className='synon-biomed-task-center-panel__count' aria-label={String(count)}>
        {count}
      </span>
    ) : null}
    <Right theme='outline' size={14} aria-hidden='true' />
  </button>
);

const TaskCenterRecordRow: React.FC<TaskCenterRecordRowProps> = ({ label, meta, testId }) => (
  <div
    className='synon-biomed-task-center-panel__action synon-biomed-task-center-panel__action--static'
    data-testid={testId}
  >
    <span className='synon-biomed-task-center-panel__action-copy'>
      <span className='synon-biomed-task-center-panel__action-label'>{label}</span>
      <span className='synon-biomed-task-center-panel__action-meta'>{meta}</span>
    </span>
  </div>
);

const SynonBiomedTaskCenterPanel: React.FC<SynonBiomedTaskCenterPanelProps> = ({
  phase,
  statusTitle,
  startedAtValue,
  finishedAtValue,
  taskElapsedValue,
  latestElapsedValue,
  metrics,
  planAvailable,
  planNeedsApproval,
  reviewAvailable,
  reviewFindingsAvailable,
  reviewVerdict = null,
  reviewIssueCount = 0,
  pendingInputCount = 0,
  recoveryAvailable = false,
  recoveryResumeLabel = null,
  failureDetail = null,
  onClose,
  onRefresh,
  onResume,
  onChooseModel,
  onOpenReviewFindings,
  onOpenPlan,
  onOpenPendingInput,
}) => {
  const { t, i18n } = useTranslation();
  const numberFormatter = new Intl.NumberFormat(i18n.language);
  const taskTokenUsage = metrics?.total.tokenUsage ?? null;
  const latestTokenUsage = metrics?.latest.tokenUsage ?? null;
  const taskModelCallCount = metrics?.total.modelCallCount ?? null;
  const latestModelCallCount = metrics?.latest.modelCallCount ?? null;
  const taskToolCallCount = metrics?.total.toolCallCount ?? null;
  const latestToolCallCount = metrics?.latest.toolCallCount ?? null;
  const compactTaskTokenUsage = formatCompactTokenUsage(taskTokenUsage, i18n.language);
  const compactLatestTokenUsage = formatCompactTokenUsage(latestTokenUsage, i18n.language) ?? '—';
  const exactTaskTokenUsage = taskTokenUsage === null ? null : numberFormatter.format(taskTokenUsage);
  const exactTaskModelCalls = taskModelCallCount === null ? null : numberFormatter.format(taskModelCallCount);
  const exactLatestModelCalls = latestModelCallCount === null ? '—' : numberFormatter.format(latestModelCallCount);
  const exactTaskToolCalls = taskToolCallCount === null ? null : numberFormatter.format(taskToolCallCount);
  const exactLatestToolCalls = latestToolCallCount === null ? '—' : numberFormatter.format(latestToolCallCount);
  const latestDetail = (value: string | null): string =>
    t('conversation.synonRuntime.runtimeOperations.taskMetricLatestRound', { value: value ?? '—' });
  const attentionActionCount =
    (pendingInputCount > 0 && onOpenPendingInput ? pendingInputCount : 0) +
    (planNeedsApproval && onOpenPlan ? 1 : 0) +
    (reviewFindingsAvailable && onOpenReviewFindings ? 1 : 0);
  const reviewResultMeta =
    reviewVerdict === 'pass'
      ? t('conversation.synonRuntime.runtimeOperations.reviewPassed')
      : reviewVerdict === 'pass_with_warnings'
        ? t('conversation.synonRuntime.runtimeOperations.reviewPassedWithWarnings')
        : reviewVerdict === 'revise'
          ? t('conversation.synonRuntime.runtimeOperations.reviewNeedsChanges')
          : t('conversation.synonRuntime.runtimeOperations.reviewCompleted');
  const showTaskOverview =
    (planAvailable && !planNeedsApproval && Boolean(onOpenPlan)) ||
    (reviewAvailable && !reviewFindingsAvailable && Boolean(onOpenReviewFindings)) ||
    (phase === 'completed' && !planAvailable);

  const runAction = (action?: () => void) => {
    onClose();
    action?.();
  };

  return (
    <div
      className='synon-biomed-task-center-panel'
      data-testid='synon-biomed-task-status-panel'
      data-phase={phase ?? 'unavailable'}
      role='dialog'
      aria-modal='false'
      aria-label={t('conversation.synonRuntime.runtimeOperations.taskDetails')}
    >
      <header className='synon-biomed-task-center-panel__summary'>
        <div className='synon-biomed-task-center-panel__summary-icon' aria-hidden='true'>
          <StatusGlyph phase={phase} />
        </div>
        <h2 className='synon-biomed-task-center-panel__title'>{statusTitle}</h2>
      </header>

      <div className='synon-biomed-task-center-panel__body'>
        <div className='synon-biomed-task-center-panel__snapshot' data-testid='synon-biomed-task-status-snapshot'>
          <dl className='synon-biomed-task-center-panel__timeline'>
            <div>
              <dt>{t('conversation.synonRuntime.runtimeOperations.taskMetricStartedAt')}</dt>
              <dd data-testid='synon-biomed-task-metric-started' title={startedAtValue ?? undefined}>
                {startedAtValue ?? '—'}
              </dd>
            </div>
            <div>
              <dt>{t('conversation.synonRuntime.runtimeOperations.taskMetricFinishedAt')}</dt>
              <dd data-testid='synon-biomed-task-metric-finished' title={finishedAtValue ?? undefined}>
                {finishedAtValue ?? '—'}
              </dd>
            </div>
          </dl>

          <dl
            className='synon-biomed-task-center-panel__metrics'
            aria-label={t('conversation.synonRuntime.runtimeOperations.taskMetrics')}
          >
            <TaskCenterMetric
              label={t('conversation.synonRuntime.runtimeOperations.taskMetricElapsed')}
              value={taskElapsedValue ?? '—'}
              detail={latestDetail(latestElapsedValue)}
              testId='synon-biomed-task-metric-elapsed'
            />
            <TaskCenterMetric
              label={t('conversation.synonRuntime.runtimeOperations.taskMetricTokenUsage')}
              value={compactTaskTokenUsage ?? '—'}
              detail={latestDetail(compactLatestTokenUsage)}
              title={exactTaskTokenUsage ?? undefined}
              testId='synon-biomed-task-metric-tokens'
            />
            <TaskCenterMetric
              label={t('conversation.synonRuntime.runtimeOperations.taskMetricModelCalls')}
              value={exactTaskModelCalls ?? '—'}
              detail={latestDetail(exactLatestModelCalls)}
              testId='synon-biomed-task-metric-model-calls'
            />
            <TaskCenterMetric
              label={t('conversation.synonRuntime.runtimeOperations.taskMetricToolCalls')}
              value={exactTaskToolCalls ?? '—'}
              detail={latestDetail(exactLatestToolCalls)}
              testId='synon-biomed-task-metric-tools'
            />
          </dl>
        </div>

        {phase === 'failed' && failureDetail ? (
          <section
            className='synon-biomed-task-center-panel__section synon-biomed-task-center-panel__section--attention'
            aria-labelledby='synon-task-center-failure-reason'
          >
            <h3 id='synon-task-center-failure-reason'>
              {t('conversation.synonRuntime.runtimeOperations.taskFailureReason')}
            </h3>
            <p
              className='m-0 whitespace-pre-wrap break-words text-12px leading-19px text-t-secondary'
              data-testid='synon-biomed-task-center-failure-detail'
              role='alert'
            >
              {failureDetail}
            </p>
          </section>
        ) : null}

        {attentionActionCount > 0 ? (
          <section
            className='synon-biomed-task-center-panel__section synon-biomed-task-center-panel__section--attention'
            aria-labelledby='synon-task-center-attention'
          >
            <div className='synon-biomed-task-center-panel__section-heading'>
              <h3 id='synon-task-center-attention'>{t('conversation.synonRuntime.runtimeOperations.taskAttention')}</h3>
              <span className='synon-biomed-task-center-panel__count'>{attentionActionCount}</span>
            </div>
            <div className='synon-biomed-task-center-panel__actions'>
              {pendingInputCount > 0 && onOpenPendingInput ? (
                <TaskCenterActionRow
                  label={
                    phase === 'waiting_input'
                      ? t('conversation.synonRuntime.runtimeOperations.waitingForAnswer')
                      : t('conversation.synonRuntime.runtimeOperations.taskWaitingApproval')
                  }
                  meta={t('conversation.synonRuntime.runtimeOperations.taskPendingCount', {
                    count: pendingInputCount,
                  })}
                  count={pendingInputCount}
                  attention
                  testId='synon-biomed-task-open-pending-input'
                  onClick={() => runAction(onOpenPendingInput)}
                />
              ) : null}
              {planNeedsApproval && onOpenPlan ? (
                <TaskCenterActionRow
                  label={t('conversation.synonRuntime.runtimeOperations.taskPlan')}
                  meta={t('conversation.synonRuntime.runtimeOperations.taskWaitingApproval')}
                  attention
                  testId='synon-biomed-task-open-plan'
                  onClick={() => runAction(onOpenPlan)}
                />
              ) : null}
              {reviewFindingsAvailable && onOpenReviewFindings ? (
                <TaskCenterActionRow
                  label={t('conversation.synonRuntime.runtimeOperations.viewReviewFindings')}
                  meta={t('conversation.synonRuntime.runtimeOperations.taskIssueCount', {
                    count: reviewIssueCount,
                  })}
                  count={reviewIssueCount}
                  attention
                  testId='synon-biomed-task-open-review'
                  onClick={() => runAction(onOpenReviewFindings)}
                />
              ) : null}
            </div>
          </section>
        ) : null}

        {showTaskOverview ? (
          <section className='synon-biomed-task-center-panel__section' aria-labelledby='synon-task-center-overview'>
            <h3 id='synon-task-center-overview'>{t('conversation.synonRuntime.runtimeOperations.taskOverview')}</h3>
            <div className='synon-biomed-task-center-panel__actions'>
              {planAvailable && !planNeedsApproval && onOpenPlan ? (
                <TaskCenterActionRow
                  label={t('conversation.synonRuntime.runtimeOperations.taskPlan')}
                  meta={t('conversation.synonRuntime.runtimeOperations.viewPlan')}
                  testId='synon-biomed-task-open-plan'
                  onClick={() => runAction(onOpenPlan)}
                />
              ) : phase === 'completed' && !planAvailable ? (
                <TaskCenterRecordRow
                  label={t('conversation.synonRuntime.runtimeOperations.taskPlan')}
                  meta={t('conversation.synonRuntime.runtimeOperations.taskPlanUnavailable')}
                  testId='synon-biomed-task-plan-unavailable'
                />
              ) : null}
              {reviewAvailable && !reviewFindingsAvailable && onOpenReviewFindings ? (
                <TaskCenterActionRow
                  label={t('conversation.synonRuntime.runtimeOperations.viewReviewResult')}
                  meta={reviewResultMeta}
                  testId='synon-biomed-task-open-review'
                  onClick={() => runAction(onOpenReviewFindings)}
                />
              ) : null}
            </div>
          </section>
        ) : null}

        {recoveryAvailable && onRefresh ? (
          <section className='synon-biomed-task-center-panel__section' aria-labelledby='synon-task-center-recovery'>
            <h3 id='synon-task-center-recovery'>{t('conversation.synonRuntime.runtimeOperations.taskRecovery')}</h3>
            <div className='synon-biomed-task-center-panel__actions'>
              {onResume && recoveryResumeLabel ? (
                <TaskCenterActionRow
                  label={recoveryResumeLabel}
                  testId='synon-biomed-task-resume'
                  onClick={() => runAction(onResume)}
                />
              ) : null}
              {onChooseModel ? (
                <TaskCenterActionRow
                  label={t('conversation.synonRuntime.runtimeOperations.chooseAnotherModel')}
                  testId='synon-biomed-task-choose-model'
                  onClick={() => runAction(onChooseModel)}
                />
              ) : null}
              <TaskCenterActionRow
                label={t('conversation.synonRuntime.runtimeOperations.reloadRuntime')}
                testId='synon-biomed-task-refresh'
                onClick={() => runAction(onRefresh)}
              />
            </div>
          </section>
        ) : null}
      </div>
    </div>
  );
};

export default SynonBiomedTaskCenterPanel;
