import { Button, Empty, Spin } from '@arco-design/web-react';
import { Attention, CheckOne, Redo } from '@icon-park/react';
import React from 'react';
import { useTranslation } from 'react-i18next';
import type {
  SynonBiomedVerificationCheck,
  SynonBiomedVerificationStatus,
  SynonBiomedVerificationVerdict,
} from '@/renderer/services/synonBiomedAnnotations';
import './SynonBiomedReviewFindingsContent.css';

type SynonBiomedReviewFindingsContentProps = {
  checks: SynonBiomedVerificationCheck[];
  loading: boolean;
  error: string | null;
  onRetry: () => void;
  onRepairAndRegenerate?: () => void | Promise<void>;
  repairing?: boolean;
};

const ISSUE_VERDICTS = new Set<SynonBiomedVerificationVerdict>(['fail', 'warn']);

const SynonBiomedReviewFindingsContent: React.FC<SynonBiomedReviewFindingsContentProps> = ({
  checks,
  loading,
  error,
  onRetry,
  onRepairAndRegenerate,
  repairing = false,
}) => {
  const { t } = useTranslation();
  if (loading) {
    return (
      <div className='h-240px flex-center'>
        <Spin tip={t('conversation.synonRuntime.runtimeOperations.loadingReviewResult')} />
      </div>
    );
  }

  if (error) {
    return (
      <div className='px-12px py-20px flex flex-col items-center gap-10px text-12px text-t-secondary' role='alert'>
        <span>{error}</span>
        <Button size='small' icon={<Redo theme='outline' size={14} />} onClick={onRetry}>
          {t('conversation.synonRuntime.runtimeOperations.reloadReviewResult')}
        </Button>
      </div>
    );
  }

  if (checks.length === 0) {
    return <Empty description={t('conversation.synonRuntime.runtimeOperations.noReviewResult')} />;
  }
  const hasActionableChecks = checks.some((check) => ISSUE_VERDICTS.has(check.verdict));
  const fullSessionScope = manualFullSessionReviewScope(checks);
  const automaticScope = automaticReviewScope(checks);

  return (
    <section
      data-testid='synon-biomed-review-findings'
      className='synon-biomed-review-findings'
      aria-label={t('conversation.synonRuntime.runtimeOperations.reviewResult')}
    >
      <header className='synon-biomed-review-findings__header'>
        <p className='synon-biomed-review-findings__description'>
          {t('conversation.synonRuntime.runtimeOperations.reviewResultDescription')}
        </p>
        {fullSessionScope ? (
          <p
            data-testid='synon-biomed-review-scope'
            className='synon-biomed-review-findings__description synon-biomed-review-findings__scope'
          >
            {t('conversation.synonRuntime.runtimeOperations.manualReviewFullSessionScope', fullSessionScope)}
          </p>
        ) : automaticScope ? (
          <p
            data-testid='synon-biomed-review-scope'
            className='synon-biomed-review-findings__description synon-biomed-review-findings__scope'
          >
            {t('conversation.synonRuntime.runtimeOperations.automaticReviewScope', automaticScope)}
          </p>
        ) : null}
      </header>
      <ol className='synon-biomed-review-findings__list'>
        {checks.map((check, index) => (
          <ReviewCheckRow key={check.id} check={check} index={index} />
        ))}
      </ol>
      {hasActionableChecks && onRepairAndRegenerate ? (
        <footer className='synon-biomed-review-findings__footer'>
          <Button
            type='primary'
            data-testid='synon-biomed-review-findings-repair'
            loading={repairing}
            disabled={repairing}
            onClick={() => void onRepairAndRegenerate()}
          >
            {t('conversation.synonRuntime.runtimeOperations.repairAndRegenerate')}
          </Button>
        </footer>
      ) : null}
    </section>
  );
};

function manualFullSessionReviewScope(
  checks: SynonBiomedVerificationCheck[]
): { messages: number; chunks: number } | null {
  for (const check of checks) {
    const sourceRef = check.sourceRef;
    if (!sourceRef || sourceRef.review_scope !== 'full_root_session') continue;
    const messages = positiveInteger(sourceRef.message_count);
    const chunks = positiveInteger(sourceRef.review_chunk_count);
    if (messages !== null && chunks !== null) return { messages, chunks };
  }
  return null;
}

function automaticReviewScope(checks: SynonBiomedVerificationCheck[]): { checkpoints: number; chunks: number } | null {
  for (const check of checks) {
    const sourceRef = check.sourceRef;
    if (!sourceRef || sourceRef.review_scope !== 'logical_task_terminal') continue;
    const checkpoints = positiveInteger(sourceRef.automatic_checkpoint_count);
    const chunks = positiveInteger(sourceRef.review_chunk_count);
    if (checkpoints !== null && chunks !== null) return { checkpoints, chunks };
  }
  return null;
}

function positiveInteger(value: unknown): number | null {
  return typeof value === 'number' && Number.isInteger(value) && value > 0 ? value : null;
}

const ReviewCheckRow: React.FC<{ check: SynonBiomedVerificationCheck; index: number }> = ({ check, index }) => {
  const { t } = useTranslation();
  const passed = check.verdict === 'pass';
  return (
    <li>
      <article data-testid='synon-biomed-review-finding' className='synon-biomed-review-findings__item'>
        <div className='flex items-start gap-8px'>
          <span
            className={`synon-biomed-review-findings__icon${passed ? ' synon-biomed-review-findings__icon--passed' : ''}`}
            aria-hidden='true'
          >
            {passed ? <CheckOne theme='outline' size={15} /> : <Attention theme='outline' size={15} />}
          </span>
          <div className='min-w-0 flex-1'>
            <div className='flex flex-wrap items-center gap-6px text-12px leading-18px'>
              <span className='font-[600] text-t-primary'>
                {t('conversation.synonRuntime.runtimeOperations.reviewCheckLabel', { index: index + 1 })}
              </span>
              <span className='text-t-secondary'>
                {t(`conversation.synonRuntime.runtimeOperations.reviewVerdict${capitalize(check.verdict)}`)}
              </span>
              {check.severity ? <span className='text-t-tertiary'>{check.severity}</span> : null}
              <span className='ml-auto text-t-tertiary'>
                {t(`conversation.synonRuntime.runtimeOperations.reviewCheckStatus${capitalize(check.status)}`)}
              </span>
            </div>
            {check.claim ? <p className='mt-6px mb-0 text-13px leading-21px text-t-primary'>{check.claim}</p> : null}
            {check.evidence ? (
              <div className='mt-6px text-12px leading-20px text-t-secondary'>
                <span className='font-[600] text-t-primary'>
                  {t('conversation.synonRuntime.runtimeOperations.reviewFindingEvidence')}
                </span>
                <span className='ml-6px'>{check.evidence}</span>
              </div>
            ) : null}
            {check.rebuttal ? (
              <div className='mt-6px text-12px leading-20px text-t-secondary'>
                <span className='font-[600] text-t-primary'>
                  {t('conversation.synonRuntime.runtimeOperations.reviewFindingNote')}
                </span>
                <span className='ml-6px'>{check.rebuttal}</span>
              </div>
            ) : null}
          </div>
        </div>
      </article>
    </li>
  );
};

function capitalize(value: SynonBiomedVerificationVerdict | SynonBiomedVerificationStatus): string {
  return value.charAt(0).toUpperCase() + value.slice(1);
}

export default SynonBiomedReviewFindingsContent;
