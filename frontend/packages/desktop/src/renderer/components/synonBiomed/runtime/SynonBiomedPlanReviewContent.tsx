import { Button, Empty, Spin } from '@arco-design/web-react';
import { Redo } from '@icon-park/react';
import React from 'react';
import { useTranslation } from 'react-i18next';
import type { SynonBiomedPlanDocument, SynonBiomedPlanReference } from './runtimeOperationsModel';
import SynonBiomedPlanTree from './SynonBiomedPlanTree';
import './SynonBiomedPlanReviewContent.css';

const SynonBiomedPlanReviewContent: React.FC<{
  document: SynonBiomedPlanDocument | null;
  reference: SynonBiomedPlanReference | null;
  loading: boolean;
  error: string | null;
  onRetry: () => void;
}> = ({ document, reference, loading, error, onRetry }) => {
  const { t, i18n } = useTranslation();
  if (loading) {
    return (
      <div className='h-240px flex-center'>
        <Spin tip={t('conversation.synonRuntime.runtimeOperations.loadingPlan')} />
      </div>
    );
  }
  if (error) {
    return (
      <div className='px-12px py-20px flex flex-col items-center gap-10px text-12px text-danger-6'>
        <span>{error}</span>
        <Button size='small' icon={<Redo theme='outline' size={14} />} onClick={onRetry}>
          {t('conversation.synonRuntime.runtimeOperations.reloadPlan')}
        </Button>
      </div>
    );
  }
  if (!document) return <Empty description={t('conversation.synonRuntime.runtimeOperations.noPlan')} />;
  const revisionNumber = reference?.revisionNumber ?? null;
  const revisionCount = reference?.revisionCount ?? null;
  const generatedAt = reference?.generatedAt
    ? new Intl.DateTimeFormat(i18n.resolvedLanguage || i18n.language, {
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
      }).format(new Date(reference.generatedAt))
    : null;
  const confidence = document.feasibility?.confidence?.toLowerCase() ?? null;
  const confidenceLabel =
    confidence === 'high'
      ? t('conversation.synonRuntime.runtimeOperations.planAssessment.high')
      : confidence === 'medium'
        ? t('conversation.synonRuntime.runtimeOperations.planAssessment.medium')
        : confidence === 'low'
          ? t('conversation.synonRuntime.runtimeOperations.planAssessment.low')
          : confidence;
  return (
    <div className='synon-biomed-plan-review' data-testid='synon-biomed-plan-review'>
      <div className='synon-biomed-plan-review__meta'>
        <span className='font-[500] text-t-secondary'>
          {t('conversation.synonRuntime.runtimeOperations.currentExecutionPlan')}
        </span>
        {revisionNumber && revisionCount ? (
          <span>
            {t('conversation.synonRuntime.runtimeOperations.planRevision', {
              revision: revisionNumber,
              count: revisionCount,
            })}
          </span>
        ) : null}
        {generatedAt ? (
          <span>
            {t('conversation.synonRuntime.runtimeOperations.planGeneratedAt', {
              time: generatedAt,
            })}
          </span>
        ) : null}
        {confidenceLabel ? (
          <span>
            {t('conversation.synonRuntime.runtimeOperations.planAssessment.label', {
              confidence: confidenceLabel,
            })}
          </span>
        ) : null}
      </div>
      <h3 className='synon-biomed-plan-review__summary'>{document.taskSummary}</h3>
      {document.feasibility?.rationale ? (
        <p className='synon-biomed-plan-review__rationale'>{document.feasibility.rationale}</p>
      ) : null}
      <SynonBiomedPlanTree document={document} />
    </div>
  );
};

export default SynonBiomedPlanReviewContent;
