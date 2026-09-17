import React, { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import SynonBiomedAvatar from '@/renderer/components/synonBiomed/SynonBiomedAvatar';
import type { SynonBiomedPlanDocument } from './runtimeOperationsModel';
import SynonBiomedPlanStepList from './SynonBiomedPlanStepList';
import './SynonBiomedPlanReviewContent.css';

const SynonBiomedPlanTree: React.FC<{
  document: SynonBiomedPlanDocument;
}> = ({ document }) => {
  const { t } = useTranslation();
  const phases = useMemo(
    () =>
      document.phases.map((phase) => {
        const delegatedIds = new Set(
          phase.delegations.flatMap((delegation) => delegation.steps.map((step) => step.id))
        );
        return {
          ...phase,
          steps: phase.steps.filter((step) => !delegatedIds.has(step.id)),
        };
      }),
    [document]
  );
  return (
    <div className='synon-biomed-plan-review__phases'>
      {phases.map((phase, phaseIndex) => (
        <section key={phase.id} className='synon-biomed-plan-review__phase' data-testid='synon-biomed-plan-phase'>
          <div className='synon-biomed-plan-review__phase-title'>
            {phaseIndex + 1}. {phase.name}
          </div>
          {phase.steps.length > 0 ? <SynonBiomedPlanStepList steps={phase.steps} /> : null}
          {phase.delegations.length > 0 ? (
            <div className='synon-biomed-plan-review__delegations'>
              {phase.delegations.map((delegation) => (
                <div
                  key={delegation.id}
                  className='synon-biomed-plan-review__delegation'
                  data-testid='synon-biomed-plan-delegation'
                >
                  <div className='synon-biomed-plan-review__delegation-heading'>
                    <SynonBiomedAvatar size={14} className='shrink-0' />
                    <span className='truncate'>{delegation.name}</span>
                    {delegation.agentName ? (
                      <span className='synon-biomed-plan-review__agent'>{delegation.agentName}</span>
                    ) : null}
                  </div>
                  <SynonBiomedPlanStepList steps={delegation.steps} />
                </div>
              ))}
            </div>
          ) : phase.steps.length === 0 ? (
            <div className='synon-biomed-plan-review__empty-steps'>
              {t('conversation.synonRuntime.runtimeOperations.noExecutionSteps')}
            </div>
          ) : null}
        </section>
      ))}
    </div>
  );
};

export default SynonBiomedPlanTree;
