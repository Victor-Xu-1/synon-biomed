/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Message, Spin } from '@arco-design/web-react';
import { Undo } from '@icon-park/react';
import React, { useCallback, useRef, useState } from 'react';
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
  disabled?: boolean;
  /** Replaces the composer draft; also used by the revert action to restore the previous text. */
  onReplace: (text: string) => void;
};

/**
 * WorkBuddy-style composer action: one click rewrites the draft into a
 * clearer scientific task instruction through the active model provider.
 * After a rewrite a persistent revert button appears to the left of the
 * sparkle and restores the original wording until the next optimization.
 */
const OptimizePromptAction: React.FC<OptimizePromptActionProps> = ({ draft, disabled, onReplace }) => {
  const { t } = useTranslation();
  const [optimizing, setOptimizing] = useState(false);
  const [canRevert, setCanRevert] = useState(false);
  // The revert affordance outlives re-renders, so the pre-optimization text
  // must be read through a ref instead of a captured state value.
  const previousDraftRef = useRef<string | null>(null);

  const handleRevert = useCallback(() => {
    const previous = previousDraftRef.current;
    if (previous === null) return;
    previousDraftRef.current = null;
    setCanRevert(false);
    onReplace(previous);
  }, [onReplace]);

  const handleOptimize = useCallback(async () => {
    if (optimizing || disabled || draft.trim() === '') return;
    setOptimizing(true);
    try {
      const result = await optimizeSynonBiomedPrompt(draft);
      if (result.text.trim() === '') throw new Error('model provider returned an empty response');
      previousDraftRef.current = draft;
      setCanRevert(true);
      onReplace(result.text);
      Message.success({
        id: 'synon-biomed-prompt-optimized',
        content: t('conversation.synonRuntime.sendBox.optimizePrompt.applied'),
        duration: 6000,
      });
    } catch {
      Message.error(t('conversation.synonRuntime.sendBox.optimizePrompt.failed'));
    } finally {
      setOptimizing(false);
    }
  }, [disabled, draft, onReplace, optimizing, t]);

  return (
    <>
      {canRevert && (
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
