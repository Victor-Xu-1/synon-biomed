import React, { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { SynonBiomedKernel, SynonBiomedKernelStopRequest } from '@/renderer/services/synonBiomedNotebook';
import { formatKernelEnvironment, formatMemory, sumNullable } from './computeRuntimeModel';

type KernelStopDrawerProps = {
  kernels: SynonBiomedKernel[];
  scope?: 'kernel' | 'session';
  onClose: () => void;
  onSubmit: (request: SynonBiomedKernelStopRequest) => Promise<void>;
};

const KernelStopDrawer: React.FC<KernelStopDrawerProps> = ({ kernels, scope = 'kernel', onClose, onSubmit }) => {
  const { t } = useTranslation();
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const [reason, setReason] = useState('');
  const [submitting, setSubmitting] = useState<'interrupt' | 'clear' | null>(null);
  const [error, setError] = useState<string | null>(null);
  const hasBusyKernel = kernels.some((kernel) => kernel.busy && kernel.currentCell !== null);
  const memory = formatMemory(sumNullable(kernels.map((kernel) => kernel.rssBytes)));
  const isSession = scope === 'session';
  const environmentLabel = formatKernelEnvironment(
    kernels[0]?.environment || '',
    t('conversation.synonRuntime.computeRuntime.softwareRuntime')
  );

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  const submit = async (mode: 'interrupt' | 'clear') => {
    if (submitting || kernels.length === 0) return;
    setSubmitting(mode);
    setError(null);
    try {
      await onSubmit({ mode, reason: reason.trim() || null });
      onClose();
    } catch (submitError) {
      setSubmitting(null);
      setError(
        submitError instanceof Error ? submitError.message : t('conversation.synonRuntime.computeRuntime.stopFailed')
      );
    }
  };

  return (
    <div
      className='min-w-0'
      data-testid='stop-kernel-drawer'
      onKeyDown={(event) => {
        if (event.key === 'Escape') {
          event.stopPropagation();
          onClose();
        }
      }}
      onMouseDown={(event) => event.stopPropagation()}
      onClick={(event) => event.stopPropagation()}
    >
      <div className='flex items-baseline gap-6px text-13px font-500 text-t-primary'>
        <span>
          {t(
            isSession
              ? 'conversation.synonRuntime.computeRuntime.stopDrawer.sessionTitle'
              : 'conversation.synonRuntime.computeRuntime.stopDrawer.kernelTitle'
          )}
        </span>
        <span className='font-normal text-t-tertiary'>
          {isSession
            ? t('conversation.synonRuntime.computeRuntime.stopDrawer.sessionMeta', {
                kernels: kernels.length,
                memory,
              })
            : t('conversation.synonRuntime.computeRuntime.stopDrawer.kernelMeta', {
                language: kernels[0]?.language === 'r' ? 'R' : 'Python',
                environment: environmentLabel,
                memory,
              })}
        </span>
      </div>
      <div className='mt-2px text-12px leading-18px text-t-tertiary'>
        {t(
          hasBusyKernel
            ? 'conversation.synonRuntime.computeRuntime.stopDrawer.busyHelp'
            : 'conversation.synonRuntime.computeRuntime.stopDrawer.idleHelp',
          { kernels: kernels.length, memory }
        )}
      </div>
      <textarea
        ref={inputRef}
        value={reason}
        maxLength={500}
        rows={2}
        placeholder={t('conversation.synonRuntime.computeRuntime.stopDrawer.reasonPlaceholder')}
        className='mt-8px box-border min-h-56px w-full resize-none rounded-8px border border-solid border-[var(--color-border-2)] bg-fill-2 px-10px py-8px text-13px text-t-primary outline-none placeholder:text-t-tertiary focus:border-primary-5'
        data-testid='stop-reason-input'
        onChange={(event) => setReason(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === 'Enter' && !event.shiftKey) {
            event.preventDefault();
            void submit(hasBusyKernel ? 'interrupt' : 'clear');
          }
        }}
      />
      {error && (
        <div className='mt-6px text-12px text-danger-6' role='alert'>
          {error}
        </div>
      )}
      <div className='mt-10px flex flex-wrap items-center gap-8px'>
        {hasBusyKernel && (
          <button
            type='button'
            data-testid='stop-interrupt'
            disabled={submitting !== null}
            className='synon-compute-stop-interrupt inline-flex h-28px items-center rounded-8px border-0 px-10px text-12px font-500 text-white transition-colors disabled:pointer-events-none disabled:opacity-45'
            onClick={() => void submit('interrupt')}
          >
            {submitting === 'interrupt'
              ? t('conversation.synonRuntime.computeRuntime.stopDrawer.stopping')
              : t('conversation.synonRuntime.computeRuntime.stopDrawer.interrupt')}
          </button>
        )}
        <button
          type='button'
          data-testid='stop-kill'
          disabled={submitting !== null}
          className='synon-compute-stop-kill inline-flex h-28px items-center gap-4px rounded-8px border-0 px-10px text-12px font-500 text-white transition-colors disabled:pointer-events-none disabled:opacity-45'
          onClick={() => void submit('clear')}
        >
          {submitting === 'clear'
            ? t('conversation.synonRuntime.computeRuntime.stopDrawer.killing')
            : t(
                isSession
                  ? 'conversation.synonRuntime.computeRuntime.stopDrawer.killAll'
                  : 'conversation.synonRuntime.computeRuntime.stopDrawer.kill'
              )}
          {!submitting && memory !== '—' && (
            <span className='font-normal opacity-80'>
              ·{' '}
              {t('conversation.synonRuntime.computeRuntime.stopDrawer.frees', {
                memory,
              })}
            </span>
          )}
        </button>
        <button
          type='button'
          className='inline-flex h-28px items-center rounded-8px border-0 bg-transparent px-8px text-12px text-t-tertiary transition-colors hover:bg-fill-2 hover:text-t-primary'
          onClick={onClose}
        >
          {t('common.cancel')}
        </button>
        <span className='ml-auto text-right text-10px leading-14px text-t-tertiary'>
          {t('conversation.synonRuntime.computeRuntime.stopDrawer.reasonDelivered')}
        </span>
      </div>
    </div>
  );
};

export default KernelStopDrawer;
