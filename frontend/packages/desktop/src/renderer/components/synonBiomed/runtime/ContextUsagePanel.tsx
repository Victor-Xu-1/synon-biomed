/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Dropdown, Spin } from '@arco-design/web-react';
import { Close } from '@icon-park/react';
import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useContextUsage } from '@/renderer/hooks/synonBiomed/useContextUsage';
import styles from './ContextUsagePanel.module.css';

const RING_SIZE = 16;
const RING_STROKE_WIDTH = 2.5;

function formatTokenCount(count: number, hideZeroDecimals = false): string {
  if (count >= 1_000_000) {
    const value = count / 1_000_000;
    const formatted = value.toFixed(1);
    return hideZeroDecimals && formatted.endsWith('.0') ? `${Math.floor(value)}M` : `${formatted}M`;
  }
  if (count >= 1_000) {
    const value = count / 1_000;
    const formatted = value.toFixed(1);
    return hideZeroDecimals && formatted.endsWith('.0') ? `${Math.floor(value)}K` : `${formatted}K`;
  }
  return count.toString();
}

/**
 * Bare usage ring; the management card owns its popover behavior.
 */
const UsageRing: React.FC<{ usedTokens: number; limitTokens: number; showRatio: boolean }> = ({
  usedTokens,
  limitTokens,
  showRatio,
}) => {
  const percent = limitTokens > 0 ? (usedTokens / limitTokens) * 100 : 0;
  const radius = (RING_SIZE - RING_STROKE_WIDTH) / 2;
  const circumference = 2 * Math.PI * radius;
  const strokeDashoffset = circumference - (Math.min(percent, 100) / 100) * circumference;
  const strokeColor =
    percent > 90 ? 'rgb(var(--danger-6))' : percent > 70 ? 'rgb(var(--warning-6))' : 'rgb(var(--primary-6))';
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
        stroke={showRatio ? strokeColor : 'var(--color-text-3)'}
        strokeWidth={RING_STROKE_WIDTH}
        strokeLinecap='round'
        strokeDasharray={showRatio ? circumference : '2 3'}
        strokeDashoffset={showRatio ? strokeDashoffset : 0}
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
  const { state, retry } = useContextUsage(conversationId, visible, active);
  const usage = state.status === 'available' ? state.snapshot : null;
  const showRatio = usage?.limitSource === 'configured';
  const usagePercent = usage ? (usage.usedTokens / usage.limitTokens) * 100 : 0;
  const labels = {
    systemPrompt: t('conversation.contextUsage.systemPrompt'),
    messages: t('conversation.contextUsage.messages'),
    toolDefinitions: t('conversation.contextUsage.toolDefinitions'),
  };

  return (
    <Dropdown
      trigger='click'
      position='tl'
      popupVisible={visible}
      onVisibleChange={setVisible}
      droplist={
        <div className={`app-overlay-menu composer-control-menu ${styles.root}`} data-testid='context-usage-panel'>
          <div className={styles.header}>
            <span className={styles.headerTitle}>{t('conversation.contextUsage.title')}</span>
            <button
              type='button'
              className={styles.closeButton}
              aria-label={t('common.close')}
              data-testid='context-usage-close'
              onClick={() => setVisible(false)}
            >
              <Close theme='outline' size={14} strokeWidth={3.6} />
            </button>
          </div>
          {usage ? (
            <>
              <div className={styles.statRow}>
                {showRatio && (
                  <span className={styles.bigPercent} data-testid='context-usage-percent'>
                    {usagePercent.toFixed(1)}%
                  </span>
                )}
                <span className={styles.usedText}>
                  {t('conversation.contextUsage.usedLabel')}{' '}
                  <strong>
                    {usage.source === 'estimated' ? '≈ ' : ''}
                    {formatTokenCount(usage.usedTokens)} / {formatTokenCount(usage.limitTokens, true)}
                  </strong>
                </span>
              </div>
              {showRatio && (
                <div className={styles.bar} aria-hidden='true'>
                  <span
                    className={styles.barSegment}
                    style={{ width: `${Math.min(usagePercent, 100)}%`, background: 'rgb(var(--primary-6))' }}
                  />
                </div>
              )}
              <p className={styles.note} data-testid='context-usage-source'>
                {usage.source === 'provider'
                  ? t('conversation.contextUsage.providerTotal')
                  : t('conversation.contextUsage.estimatedTotal')}
                {' · '}
                {usage.limitSource === 'configured'
                  ? t('conversation.contextUsage.configuredLimit')
                  : t('conversation.contextUsage.defaultLimit')}
              </p>
              <p className={styles.note}>{t('conversation.contextUsage.estimatedBreakdown')}</p>
              <div className={styles.legend} data-testid='context-usage-legend'>
                {usage.inputEstimates.map((row) => (
                  <div key={row.key} className={styles.legendRow}>
                    <span>{labels[row.key]}</span>
                    <span className={styles.legendPercent}>≈ {formatTokenCount(row.tokens)}</span>
                  </div>
                ))}
                {usage.state === 'complete' && (
                  <div className={styles.legendRow}>
                    <span>{t('conversation.contextUsage.output')}</span>
                    <span className={styles.legendPercent}>
                      {usage.source === 'estimated' ? '≈ ' : ''}
                      {formatTokenCount(usage.outputTokens)}
                    </span>
                  </div>
                )}
              </div>
              <p className={styles.note}>{t('conversation.contextUsage.breakdownNote')}</p>
              <p className={styles.note}>{t('conversation.contextUsage.budgetNote')}</p>
              {usage.hasMedia && <p className={styles.note}>{t('conversation.contextUsage.mediaNote')}</p>}
              {usage.state === 'request' && (
                <p className={styles.note}>{t('conversation.contextUsage.requestPending')}</p>
              )}
              {usage.state === 'failed' && (
                <p className={styles.note}>{t('conversation.contextUsage.failedRequest')}</p>
              )}
              <p className={styles.note} data-testid='context-usage-observed'>
                {usage.model && <>{usage.model} · </>}
                <time dateTime={usage.observedAt}>{new Date(usage.observedAt).toLocaleString()}</time>
              </p>
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
        data-testid='synon-biomed-context-usage-trigger'
        aria-label={t('conversation.contextUsage.title')}
        aria-expanded={visible}
        aria-haspopup='menu'
        className='inline-flex items-center justify-center cursor-pointer border-0 bg-transparent p-0'
      >
        <UsageRing usedTokens={usage?.usedTokens ?? 0} limitTokens={usage?.limitTokens ?? 1} showRatio={showRatio} />
      </button>
    </Dropdown>
  );
};

export default ContextUsagePanel;
