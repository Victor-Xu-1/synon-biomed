/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { IMessageThinking } from '@/common/chat/chatLib';
import React, { useLayoutEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import styles from './MessageThinking.module.css';

const PREVIEW_LENGTH = 80;

const SparklesIcon: React.FC = () => (
  <svg aria-hidden='true' viewBox='0 0 24 24' fill='none' stroke='currentColor' strokeWidth='1.5'>
    <path
      strokeLinecap='round'
      strokeLinejoin='round'
      d='M9.813 15.904 9 18.75l-.813-2.846a4.5 4.5 0 0 0-3.09-3.09L2.25 12l2.846-.813a4.5 4.5 0 0 0 3.09-3.09L9 5.25l.813 2.846a4.5 4.5 0 0 0 3.09 3.09l2.847.814-2.846.813a4.5 4.5 0 0 0-3.09 3.09ZM18.259 8.715 18 9.75l-.259-1.035a3.375 3.375 0 0 0-2.455-2.456L14.25 6l1.036-.259a3.375 3.375 0 0 0 2.455-2.456L18 2.25l.259 1.035a3.375 3.375 0 0 0 2.456 2.456L21.75 6l-1.035.259a3.375 3.375 0 0 0-2.456 2.456Z'
    />
  </svg>
);

const MessageThinking: React.FC<{ message: IMessageThinking }> = ({ message }) => {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const [mounted, setMounted] = useState(false);
  const bodyRef = useRef<HTMLDivElement>(null);
  const text = message.content.content.trim();
  const preview = text.length > PREVIEW_LENGTH ? `${text.slice(0, PREVIEW_LENGTH).trim()}...` : text;
  const label = t('conversation.thinking.title');
  const active = message.content.status !== 'done';

  useLayoutEffect(() => {
    const body = bodyRef.current as (HTMLDivElement & { inert?: boolean }) | null;
    if (body) body.inert = !expanded;
  }, [expanded]);

  if (!text) return null;

  const toggle = () => {
    const next = !expanded;
    if (next) setMounted(true);
    setExpanded(next);
  };

  return (
    <section className={styles.container} data-testid='thinking-block' data-active={active ? 'true' : 'false'}>
      <button type='button' className={styles.header} aria-expanded={expanded} onClick={toggle}>
        <span className={`${styles.disclosure} ${expanded ? styles.disclosureExpanded : ''}`} aria-hidden='true'>
          <svg viewBox='0 0 24 24' fill='none' stroke='currentColor' strokeWidth='2.5'>
            <path strokeLinecap='round' strokeLinejoin='round' d='m9 5 7 7-7 7' />
          </svg>
        </span>
        <span className={styles.sparkles} aria-hidden='true'>
          <SparklesIcon />
        </span>
        <span className={`${styles.label} ${active ? styles.labelActive : ''}`}>{label}</span>
        {!expanded && (
          <span className={styles.preview} data-testid='thinking-preview'>
            {preview}
          </span>
        )}
      </button>
      <div
        ref={bodyRef}
        className={styles.collapse}
        aria-hidden={!expanded}
        data-expanded={expanded ? 'true' : 'false'}
        data-testid='thinking-body'
      >
        <div className={styles.collapseInner}>
          {mounted && (
            <div className={styles.bodyFrame}>
              <span className={styles.bodyRail} aria-hidden='true' />
              <div className={styles.body} data-testid='thinking-content'>
                {text}
              </div>
            </div>
          )}
        </div>
      </div>
      {!expanded && <span className={styles.divider} aria-hidden='true' />}
    </section>
  );
};

export default React.memo(MessageThinking);
