/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Popover, Spin } from '@arco-design/web-react';
import { Close } from '@icon-park/react';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { DEFAULT_CONTEXT_LIMIT } from '@/renderer/utils/model/modelContextLimits';
import {
  buildContextUsageBreakdown,
  CONTEXT_USAGE_COLORS,
  CONTEXT_USAGE_LABEL_KEYS,
  estimateConversationMessagesTokens,
} from '@/renderer/services/contextUsage';
import ContextUsageIndicator, { formatTokenCount } from './ContextUsageIndicator';
import styles from './ContextUsagePanel.module.css';

type ContextUsagePanelProps = {
  conversationId: string;
};

type FrameUsagePayload = {
  context_limit?: unknown;
  runtime_input_tokens?: unknown;
};

type DurableMessagePayload = {
  items?: Array<Record<string, unknown>>;
};

const MESSAGE_SAMPLE_LIMIT = 200;

function positiveNumber(value: unknown): number | null {
  const parsed = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN;
  return Number.isFinite(parsed) && parsed > 0 ? parsed : null;
}

/**
 * Composer context-usage management card. Used/limit figures come from the
 * conversation's authoritative frame projection (`context_limit` plus the
 * runner's `runtime_input_tokens`); the message share is estimated with the
 * server-side accounting heuristic over the durable history, and the
 * capability rows split the documented residual.
 */
const ContextUsagePanel: React.FC<ContextUsagePanelProps> = ({ conversationId }) => {
  const { t } = useTranslation();
  const [visible, setVisible] = useState(false);
  const [usage, setUsage] = useState<{ usedTokens: number; limitTokens: number } | null>(null);
  const [usageLoadFailed, setUsageLoadFailed] = useState(false);
  const [messageItems, setMessageItems] = useState<Array<Record<string, unknown>> | null>(null);
  const [messageLoadFailed, setMessageLoadFailed] = useState(false);

  useEffect(() => {
    if (!visible || !conversationId) return;
    let cancelled = false;
    setUsage(null);
    setUsageLoadFailed(false);
    const controller = new AbortController();
    // Same authoritative frame projection the session options menu reads.
    fetch(`/api/frames/${encodeURIComponent(conversationId)}`, {
      credentials: 'include',
      signal: controller.signal,
      headers: { Accept: 'application/json' },
    })
      .then((response) => {
        if (!response.ok) throw new Error(`frame usage request failed: ${response.status}`);
        return response.json() as Promise<FrameUsagePayload>;
      })
      .then((payload) => {
        if (!cancelled) {
          const usedTokens = positiveNumber(payload?.runtime_input_tokens);
          const limitTokens = positiveNumber(payload?.context_limit);
          if (usedTokens !== null && limitTokens !== null) {
            setUsage({ usedTokens, limitTokens });
          } else {
            setUsageLoadFailed(true);
          }
        }
      })
      .catch(() => {
        if (!cancelled) setUsageLoadFailed(true);
      });
    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [visible, conversationId]);

  useEffect(() => {
    if (!visible || !conversationId) return;
    let cancelled = false;
    setMessageItems(null);
    setMessageLoadFailed(false);
    const controller = new AbortController();
    fetch(`/api/conversations/${encodeURIComponent(conversationId)}/messages?limit=${MESSAGE_SAMPLE_LIMIT}`, {
      credentials: 'include',
      signal: controller.signal,
      headers: { Accept: 'application/json' },
    })
      .then((response) => {
        if (!response.ok) throw new Error(`messages request failed: ${response.status}`);
        return response.json() as Promise<DurableMessagePayload>;
      })
      .then((payload) => {
        if (!cancelled) setMessageItems(Array.isArray(payload?.items) ? payload.items : []);
      })
      .catch(() => {
        if (!cancelled) setMessageLoadFailed(true);
      });
    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [visible, conversationId]);

  const rows = useMemo(() => {
    if (!usage) return null;
    const messageEstimate = messageItems === null ? null : estimateConversationMessagesTokens(messageItems);
    return buildContextUsageBreakdown({
      usedTokens: usage.usedTokens,
      limitTokens: usage.limitTokens,
      messagesTokens: messageEstimate,
    });
  }, [messageItems, usage]);

  const usagePercent = usage ? (usage.usedTokens / usage.limitTokens) * 100 : 0;
  const ready = Boolean(usage && rows);

  return (
    <Popover
      trigger='click'
      position='tl'
      popupVisible={visible}
      onVisibleChange={(next) => setVisible(next)}
      content={
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
          {ready && rows && usage ? (
            <>
              <div className={styles.statRow}>
                <span className={styles.bigPercent} data-testid='context-usage-percent'>
                  {usagePercent.toFixed(1)}%
                </span>
                <span className={styles.usedText}>
                  {t('conversation.contextUsage.usedLabel')}{' '}
                  <strong>
                    {formatTokenCount(usage.usedTokens)} / {formatTokenCount(usage.limitTokens, true)}
                  </strong>
                </span>
              </div>
              <div className={styles.bar} aria-hidden='true'>
                {rows.map((row) => {
                  const width = (row.tokens / usage.limitTokens) * 100;
                  if (width <= 0) return null;
                  return (
                    <span
                      key={row.key}
                      className={styles.barSegment}
                      style={{ width: `${Math.min(width, 100)}%`, background: CONTEXT_USAGE_COLORS[row.key] }}
                    />
                  );
                })}
              </div>
              <div className={styles.legend} data-testid='context-usage-legend'>
                {rows.map((row) => (
                  <div key={row.key} className={styles.legendRow}>
                    <span>
                      <i className={styles.legendDot} style={{ background: CONTEXT_USAGE_COLORS[row.key] }} />
                      {t(CONTEXT_USAGE_LABEL_KEYS[row.key])}
                    </span>
                    <span className={styles.legendPercent}>{((row.tokens / usage.limitTokens) * 100).toFixed(1)}%</span>
                  </div>
                ))}
              </div>
            </>
          ) : usageLoadFailed || messageLoadFailed ? (
            <div className={styles.loading}>
              <span className='text-13px text-t-secondary'>{t('conversation.contextUsage.unavailable')}</span>
            </div>
          ) : (
            <div className={styles.loading}>
              <Spin size={16} />
            </div>
          )}
        </div>
      }
    >
      <span
        data-testid='synon-biomed-context-usage-trigger'
        aria-label={t('conversation.contextUsage.title')}
        className='inline-flex items-center justify-center cursor-pointer'
      >
        <ContextUsageIndicator
          tokenUsage={{ total_tokens: usage?.usedTokens ?? 0 }}
          context_limit={usage?.limitTokens ?? DEFAULT_CONTEXT_LIMIT}
          size={16}
          className='composer-control-context-ring'
        />
      </span>
    </Popover>
  );
};

export default ContextUsagePanel;
