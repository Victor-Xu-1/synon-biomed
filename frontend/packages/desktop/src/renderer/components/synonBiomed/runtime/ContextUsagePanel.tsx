/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Popover, Spin } from '@arco-design/web-react';
import { Close, Dashboard } from '@icon-park/react';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TokenUsageData } from '@/common/config/storage';
import {
  buildContextUsageBreakdown,
  CONTEXT_USAGE_COLORS,
  CONTEXT_USAGE_LABEL_KEYS,
  estimateConversationMessagesTokens,
} from '@/renderer/services/contextUsage';
import { formatTokenCount } from './ContextUsageIndicator';
import styles from './ContextUsagePanel.module.css';

type ContextUsagePanelProps = {
  conversationId: string;
  tokenUsage: TokenUsageData | null;
  contextLimit: number;
};

type DurableMessagePayload = {
  items?: Array<Record<string, unknown>>;
};

const MESSAGE_SAMPLE_LIMIT = 200;

/**
 * Composer context-usage management card. The used/limit figures come from the
 * conversation's authoritative usage record; the message share is estimated
 * with the server-side accounting heuristic over the durable history, and the
 * capability rows split the documented residual.
 */
const ContextUsagePanel: React.FC<ContextUsagePanelProps> = ({ conversationId, tokenUsage, contextLimit }) => {
  const { t } = useTranslation();
  const [visible, setVisible] = useState(false);
  const [messageItems, setMessageItems] = useState<Array<Record<string, unknown>> | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);

  useEffect(() => {
    if (!visible || !conversationId) return;
    let cancelled = false;
    setMessageItems(null);
    setLoadFailed(false);
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
        if (!cancelled) setLoadFailed(true);
      });
    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [visible, conversationId]);

  const usedTokens = tokenUsage?.total_tokens ?? 0;
  const rows = useMemo(() => {
    if (usedTokens <= 0 || contextLimit <= 0) return null;
    const messageEstimate = messageItems === null ? null : estimateConversationMessagesTokens(messageItems);
    return buildContextUsageBreakdown({ usedTokens, limitTokens: contextLimit, messagesTokens: messageEstimate });
  }, [messageItems, usedTokens, contextLimit]);

  const usagePercent = contextLimit > 0 ? (usedTokens / contextLimit) * 100 : 0;
  const ready = usedTokens > 0 && contextLimit > 0 && rows !== null;

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
          {ready && rows ? (
            <>
              <div className={styles.statRow}>
                <span className={styles.bigPercent} data-testid='context-usage-percent'>
                  {usagePercent.toFixed(1)}%
                </span>
                <span className={styles.usedText}>
                  {t('conversation.contextUsage.usedLabel')}{' '}
                  <strong>
                    {formatTokenCount(usedTokens)} / {formatTokenCount(contextLimit, true)}
                  </strong>
                </span>
              </div>
              <div className={styles.bar} aria-hidden='true'>
                {rows.map((row) => {
                  const width = (row.tokens / contextLimit) * 100;
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
                    <span className={styles.legendPercent}>{((row.tokens / contextLimit) * 100).toFixed(1)}%</span>
                  </div>
                ))}
              </div>
            </>
          ) : tokenUsage === null ? (
            <div className={styles.loading}>
              <span className='text-13px text-t-secondary'>{t('conversation.contextUsage.unavailable')}</span>
            </div>
          ) : (
            <div className={styles.loading}>
              {loadFailed ? (
                <span className='text-13px text-t-secondary'>{t('conversation.contextUsage.unavailable')}</span>
              ) : (
                <Spin size={16} />
              )}
            </div>
          )}
        </div>
      }
    >
      <button
        type='button'
        aria-label={t('conversation.contextUsage.title')}
        data-testid='synon-biomed-context-usage-trigger'
        className='composer-icon-control relative'
      >
        <Dashboard theme='outline' size={17} strokeWidth={3.6} />
      </button>
    </Popover>
  );
};

export default ContextUsagePanel;
