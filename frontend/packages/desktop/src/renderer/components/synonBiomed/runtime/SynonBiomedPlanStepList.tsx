import React from 'react';
import { useTranslation } from 'react-i18next';
import type { SynonBiomedPlanDocument } from './runtimeOperationsModel';
import { toolPublicDetailText } from '@/renderer/services/i18n/toolPublicDetailLocale';

const SynonBiomedPlanStepList: React.FC<{
  steps: SynonBiomedPlanDocument['phases'][number]['steps'];
}> = ({ steps }) => {
  const { t, i18n } = useTranslation();
  const chinese = i18n?.resolvedLanguage?.toLowerCase().startsWith('zh') ?? false;
  return steps.length > 0 ? (
    <ol className='synon-biomed-plan-review__steps'>
      {steps.map((step) => (
        <li
          key={step.id}
          className='synon-biomed-plan-review__step'
          data-plan-step-id={step.id}
          data-plan-step-status={step.status}
        >
          <div className='synon-biomed-plan-review__step-row'>
            <span
              className={`synon-biomed-plan-review__step-status synon-biomed-plan-review__step-status--${
                step.status ?? 'pending'
              }`}
              aria-label={planStepStatusLabel(step.status, chinese)}
            >
              {planStepStatusSymbol(step.status)}
            </span>
            <div className='synon-biomed-plan-review__step-content'>
              <div className='synon-biomed-plan-review__step-title'>{step.title}</div>
              {step.description ? (
                <div className='synon-biomed-plan-review__step-description'>{step.description}</div>
              ) : null}
            </div>
          </div>
        </li>
      ))}
    </ol>
  ) : (
    <div className='synon-biomed-plan-review__empty-steps'>
      {t('conversation.synonRuntime.runtimeOperations.noExecutionSteps')}
    </div>
  );
};

function planStepStatusSymbol(status: SynonBiomedPlanDocument['phases'][number]['steps'][number]['status']): string {
  if (status === 'completed') return '✓';
  if (status === 'failed' || status === 'cancelled') return '×';
  if (status === 'blocked') return '!';
  if (status === 'in_progress') return '●';
  if (status === 'unknown') return '?';
  return '○';
}

function planStepStatusLabel(
  status: SynonBiomedPlanDocument['phases'][number]['steps'][number]['status'],
  chinese: boolean
): string {
  const labels = {
    pending: 'planPending',
    in_progress: 'planInProgress',
    completed: 'planCompleted',
    blocked: 'planBlocked',
    failed: 'planFailed',
    cancelled: 'planCancelled',
    unknown: 'planUnknown',
  } as const;
  return toolPublicDetailText(chinese, labels[status ?? 'pending']);
}

export default SynonBiomedPlanStepList;
