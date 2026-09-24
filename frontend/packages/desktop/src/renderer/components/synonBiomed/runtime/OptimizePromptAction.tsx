/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Message, Spin } from '@arco-design/web-react';
import { Undo } from '@icon-park/react';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { optimizeSynonBiomedPrompt } from '@/renderer/services/synonBiomedLlm';

const SparkleIcon: React.FC = () => (
  <svg
    aria-hidden='true'
    className='h-16px w-16px text-t-secondary'
    viewBox='0 0 24 24'
    fill='none'
    stroke='currentColor'
    strokeWidth='1.5'
  >
    <path
      strokeLinecap='round'
      strokeLinejoin='round'
      d='M9.813 15.904 9 18.75l-.813-2.846a4.5 4.5 0 0 0-3.09-3.09L2.25 12l2.846-.813a4.5 4.5 0 0 0 3.09-3.09L9 5.25l.813 2.846a4.5 4.5 0 0 0 3.09 3.09l2.847.814-2.846.813a4.5 4.5 0 0 0-3.09 3.09ZM18.259 8.715 18 9.75l-.259-1.035a3.375 3.375 0 0 0-2.455-2.456L14.25 6l1.036-.259a3.375 3.375 0 0 0 2.455-2.456L18 2.25l.259 1.035a3.375 3.375 0 0 0 2.456 2.456L21.75 6l-1.035.259a3.375 3.375 0 0 0-2.456 2.456Z'
    />
  </svg>
);

export type OptimizePromptActionProps = {
  draft: string;
  conversationId: string;
  ownerId: string;
  disabled?: boolean;
  /** Atomically replaces the expected draft; false means the user or another tab changed it. */
  replaceIfCurrent: (expected: string, replacement: string) => boolean;
};

type RevertDraft = { identity: string; original: string; optimized: string };

/**
 * Composer action for rewriting a draft through the current conversation's
 * model. A revert is available only while the optimized text is still the
 * current draft; a later edit always wins over an older model response.
 */
const OptimizePromptAction: React.FC<OptimizePromptActionProps> = ({
  draft,
  conversationId,
  ownerId,
  disabled,
  replaceIfCurrent,
}) => {
  const { t } = useTranslation();
  const [optimizing, setOptimizing] = useState(false);
  const [revert, setRevert] = useState<RevertDraft | null>(null);
  const revertRef = useRef<RevertDraft | null>(null);
  const inFlightRef = useRef(false);
  const requestSequenceRef = useRef(0);
  const abortRef = useRef<AbortController | null>(null);
  const mountedRef = useRef(false);
  const identity = `${ownerId}\u0000${conversationId}`;
  const identityRef = useRef(identity);
  identityRef.current = identity;

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      requestSequenceRef.current += 1;
      abortRef.current?.abort();
      inFlightRef.current = false;
    };
  }, []);

  useEffect(() => {
    revertRef.current = null;
    setRevert(null);
    setOptimizing(false);
    return () => {
      requestSequenceRef.current += 1;
      abortRef.current?.abort();
      abortRef.current = null;
      inFlightRef.current = false;
    };
  }, [identity]);

  const handleRevert = useCallback(() => {
    const previous = revertRef.current;
    if (!previous || previous.identity !== identityRef.current) return;
    revertRef.current = null;
    setRevert(null);
    if (!replaceIfCurrent(previous.optimized, previous.original)) {
      Message.info(t('conversation.synonRuntime.sendBox.optimizePrompt.stale'));
    }
  }, [replaceIfCurrent, t]);

  const handleOptimize = useCallback(async () => {
    if (inFlightRef.current || disabled || draft.trim() === '' || conversationId.trim() === '') return;
    inFlightRef.current = true;
    setOptimizing(true);
    const sequence = ++requestSequenceRef.current;
    const requestedIdentity = identity;
    const original = draft;
    const controller = new AbortController();
    abortRef.current = controller;
    try {
      const result = await optimizeSynonBiomedPrompt({
        text: original,
        conversationId,
        signal: controller.signal,
      });
      if (!mountedRef.current || sequence !== requestSequenceRef.current || identityRef.current !== requestedIdentity) {
        return;
      }
      if (result.text.trim() === '') throw new Error('model provider returned an empty response');
      if (!replaceIfCurrent(original, result.text)) {
        revertRef.current = null;
        setRevert(null);
        Message.info(t('conversation.synonRuntime.sendBox.optimizePrompt.stale'));
        return;
      }
      const nextRevert = { identity: requestedIdentity, original, optimized: result.text };
      revertRef.current = nextRevert;
      setRevert(nextRevert);
      Message.success({
        id: 'synon-biomed-prompt-optimized',
        content: t('conversation.synonRuntime.sendBox.optimizePrompt.applied'),
        duration: 6000,
      });
    } catch {
      if (!mountedRef.current || sequence !== requestSequenceRef.current || controller.signal.aborted) return;
      Message.error(t('conversation.synonRuntime.sendBox.optimizePrompt.failed'));
    } finally {
      if (mountedRef.current && sequence === requestSequenceRef.current) {
        abortRef.current = null;
        inFlightRef.current = false;
        setOptimizing(false);
      }
    }
  }, [conversationId, disabled, draft, identity, replaceIfCurrent, t]);

  return (
    <>
      {revert?.identity === identity && (
        <button
          type='button'
          data-testid='synon-biomed-optimize-prompt-revert'
          aria-label={t('conversation.synonRuntime.sendBox.optimizePrompt.revert')}
          title={t('conversation.synonRuntime.sendBox.optimizePrompt.revert')}
          disabled={disabled || optimizing}
          onClick={handleRevert}
          className='inline-flex h-24px w-24px items-center justify-center cursor-pointer border-0 bg-transparent rounded-6px p-0 hover:bg-fill-2 disabled:cursor-not-allowed disabled:opacity-50'
        >
          <Undo theme='outline' size={16} className='text-t-secondary' />
        </button>
      )}
      <button
        type='button'
        data-testid='synon-biomed-optimize-prompt-trigger'
        aria-label={t('conversation.synonRuntime.sendBox.optimizePrompt.label')}
        title={t('conversation.synonRuntime.sendBox.optimizePrompt.label')}
        disabled={disabled || optimizing}
        onClick={handleOptimize}
        className='inline-flex h-24px w-24px items-center justify-center cursor-pointer border-0 bg-transparent rounded-6px p-0 hover:bg-fill-2 disabled:cursor-not-allowed disabled:opacity-50'
      >
        {optimizing ? (
          <span data-testid='synon-biomed-optimize-prompt-spinner' className='inline-flex'>
            <Spin size={14} />
          </span>
        ) : (
          <SparkleIcon />
        )}
      </button>
    </>
  );
};

export default OptimizePromptAction;
