/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Dropdown, Spin, Tooltip } from '@arco-design/web-react';
import { Close } from '@icon-park/react';
import React, { useId, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useContextUsage } from '@/renderer/hooks/synonBiomed/useContextUsage';
import { reconcileContextUsageBreakdown, type ContextUsageCategory } from '@/renderer/services/contextUsage';
import styles from './ContextUsagePanel.module.css';

const RING_SIZE = 16;
const RING_STROKE_WIDTH = 2.5;

const COLORS: Record<ContextUsageCategory, string> = {
  systemPrompt: '#6366f1',
  tools: '#10b981',
  messages: '#f59e0b',
  mcp: '#8b5cf6',
  skills: '#ec4899',
};

function formatTokenCount(count: number): string {
  if (count >= 1_000_000) {
    const value = count / 1_000_000;
    const formatted = value.toFixed(1);
    return `${formatted}M`;
  }
  if (count >= 1_000) {
    const value = count / 1_000;
    const formatted = value.toFixed(1);
    return `${formatted}K`;
  }
  return count.toString();
}

/**
 * Bare usage ring; the management card owns its popover behavior.
 */
const UsageRing: React.FC<{ usedTokens: number; limitTokens: number }> = ({ usedTokens, limitTokens }) => {
  const percent = limitTokens > 0 ? (usedTokens / limitTokens) * 100 : 0;
  const radius = (RING_SIZE - RING_STROKE_WIDTH) / 2;
  const circumference = 2 * Math.PI * radius;
  const strokeDashoffset = circumference - (Math.min(percent, 100) / 100) * circumference;
  return (
    <svg
      width={RING_SIZE}
      height={RING_SIZE}
      viewBox={`0 0 ${RING_SIZE} ${RING_SIZE}`}
      aria-hidden='true'
      style={{ transform: 'rotate(-90deg)', display: 'block' }}
    >
      <circle
        cx={RING_SIZE / 2}
        cy={RING_SIZE / 2}
        r={radius}
        fill='none'
        stroke='var(--color-fill-3)'
        strokeWidth={RING_STROKE_WIDTH}
      />
      <circle
        cx={RING_SIZE / 2}
        cy={RING_SIZE / 2}
        r={radius}
        fill='none'
        stroke='var(--color-text-3)'
        strokeWidth={RING_STROKE_WIDTH}
        strokeLinecap='round'
        strokeDasharray={circumference}
        strokeDashoffset={strokeDashoffset}
        style={{ transition: 'stroke-dashoffset 0.3s ease, stroke 0.3s ease' }}
      />
    </svg>
  );
};

type ContextUsagePanelProps = { conversationId: string; active?: boolean };

/** The server's latest main-agent request is the only usage authority. */
const ContextUsagePanel: React.FC<ContextUsagePanelProps> = ({ conversationId, active = false }) => {
  const { t } = useTranslation();
  const [visible, setVisible] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const detailsId = useId();
  const { state, retry } = useContextUsage(conversationId, visible, active);
  const usage = state.status === 'available' ? state.snapshot : null;
  const usagePercent = usage ? (usage.usedTokens / usage.limitTokens) * 100 : 0;
  const rows = usage ? reconcileContextUsageBreakdown(usage) : [];
  // Keep the semantic legend order stable, but make the visual bar readable:
  // tiny shares are not swallowed by a later large segment and ties retain
  // the authoritative category order from reconcileContextUsageBreakdown.
  const barRows = rows.filter((row) => row.tokens > 0).toSorted((left, right) => left.tokens - right.tokens);
  const barTotal = usage ? Math.max(usage.limitTokens, usage.usedTokens) : 1;
  const remaining = usage ? Math.max(0, barTotal - usage.usedTokens) : 0;
  const labels = {
    systemPrompt: t('conversation.contextUsage.systemPrompt'),
    tools: t('conversation.contextUsage.toolsAndSubagents'),
    messages: t('conversation.contextUsage.messages'),
    mcp: t('conversation.contextUsage.connectorsAndMcp'),
    skills: t('conversation.contextUsage.skills'),
  };
  const details = usage
    ? [
        usage.source === 'provider'
          ? t('conversation.contextUsage.providerTotal')
          : t('conversation.contextUsage.estimatedTotal'),
        usage.limitSource === 'configured'
          ? t('conversation.contextUsage.configuredLimit')
          : t('conversation.contextUsage.defaultLimit'),
        t('conversation.contextUsage.estimatedBreakdown'),
        t('conversation.contextUsage.breakdownNote'),
        t('conversation.contextUsage.budgetNote'),
        usage.hasMedia ? t('conversation.contextUsage.mediaNote') : '',
        usage.state === 'request' ? t('conversation.contextUsage.requestPending') : '',
        usage.state === 'failed' ? t('conversation.contextUsage.failedRequest') : '',
        usage.state === 'complete'
          ? `${t('conversation.contextUsage.output')}: ${formatTokenCount(usage.outputTokens)}`
          : '',
        usage.model,
        new Date(usage.observedAt).toLocaleString(),
      ].filter(Boolean)
    : [];

  return (
    <Dropdown
      trigger='click'
      position='tl'
      popupVisible={visible}
      onVisibleChange={setVisible}
      droplist={
        <div
          className={styles.root}
          data-testid='context-usage-panel'
          role='dialog'
          aria-label={t('conversation.contextUsage.title')}
        >
          <div className={styles.header}>
            <span className={styles.headerTitle}>{t('conversation.contextUsage.title')}</span>
            <button
              type='button'
              className={styles.closeButton}
              aria-label={t('common.close')}
              data-testid='context-usage-close'
              onClick={() => {
                setVisible(false);
                triggerRef.current?.focus();
              }}
            >
              <Close theme='outline' size={24} strokeWidth={2.4} />
            </button>
          </div>
          {usage ? (
            <>
              <Tooltip
                position='top'
                trigger={['hover', 'focus']}
                content={
                  <div className={styles.details}>
                    {details.map((line, index) => (
                      <p key={index}>{line}</p>
                    ))}
                  </div>
                }
              >
                <div
                  className={styles.statRow}
                  tabIndex={0}
                  role='group'
                  aria-label={t('conversation.contextUsage.title')}
                  aria-describedby={detailsId}
                >
                  <span className={styles.bigPercent} data-testid='context-usage-percent'>
                    {usagePercent.toFixed(1)}%
                  </span>
                  <span className={styles.usedText}>
                    {t('conversation.contextUsage.usedLabel')}{' '}
                    <strong>
                      {usage.source === 'estimated' ? '≈ ' : ''}
                      {formatTokenCount(usage.usedTokens)} / {formatTokenCount(usage.limitTokens)}
                    </strong>
                  </span>
                </div>
              </Tooltip>
              <span id={detailsId} className={styles.screenReaderOnly}>
                {details.join(' · ')}
              </span>
              <div className={styles.bar} aria-hidden='true' data-testid='context-usage-bar'>
                {barRows.map((row) => (
                  <span
                    key={row.key}
                    data-category={row.key}
                    className={styles.barSegment}
                    style={{ flexGrow: row.tokens / barTotal, background: COLORS[row.key] }}
                  />
                ))}
                {remaining > 0 && <span className={styles.barRemaining} style={{ flexGrow: remaining / barTotal }} />}
              </div>
              <div className={styles.legend} data-testid='context-usage-legend'>
                {rows.map((row) => (
                  <div key={row.key} className={styles.legendRow}>
                    <span className={styles.legendLabel}>
                      <i className={styles.legendDot} style={{ background: COLORS[row.key] }} aria-hidden='true' />
                      {labels[row.key]}
                    </span>
                    <span className={styles.legendPercent}>{((row.tokens / usage.limitTokens) * 100).toFixed(1)}%</span>
                  </div>
                ))}
              </div>
            </>
          ) : state.status === 'loading' ? (
            <div
              className={styles.loading}
              role='status'
              aria-label={t('conversation.contextUsage.loading')}
              data-testid='context-usage-loading'
            >
              <Spin size={16} />
            </div>
          ) : (
            <div className={styles.empty} role='status'>
              <span>
                {state.status === 'error'
                  ? t('conversation.contextUsage.loadFailed')
                  : t('conversation.contextUsage.unavailable')}
              </span>
              <button type='button' onClick={retry} className={styles.retry}>
                {t('conversation.contextUsage.retry')}
              </button>
            </div>
          )}
        </div>
      }
    >
      <button
        type='button'
        ref={triggerRef}
        data-testid='synon-biomed-context-usage-trigger'
        aria-label={t('conversation.contextUsage.title')}
        aria-expanded={visible}
        aria-haspopup='dialog'
        className='inline-flex items-center justify-center cursor-pointer border-0 bg-transparent p-0'
      >
        <UsageRing usedTokens={usage?.usedTokens ?? 0} limitTokens={usage?.limitTokens ?? 1} />
      </button>
    </Dropdown>
  );
};

export default ContextUsagePanel;
