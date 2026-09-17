import { IconRight } from '@arco-design/web-react/icon';
import React, { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import type { IMessagePlan } from '@/common/chat/chatLib';
import type { SynonBiomedPlanDocument, SynonBiomedPlanStep } from './runtimeOperationsModel';
import SynonBiomedPlanTree from './SynonBiomedPlanTree';
import { useConversationDisclosure } from '@/renderer/services/runtime/conversationDisclosureStore';
import { getSelectedSynonBiomedBranch } from '@/renderer/services/synonBiomedConversationBranches';
import { toolPublicDetailText } from '@/renderer/services/i18n/toolPublicDetailLocale';
import './SynonBiomedPlanReviewContent.css';

const SynonBiomedTimelinePlan: React.FC<{ message: IMessagePlan }> = ({ message }) => {
  const { i18n } = useTranslation();
  const chinese = i18n.resolvedLanguage?.toLowerCase().startsWith('zh') ?? false;
  const { expanded, toggle } = useConversationDisclosure(
    {
      conversationId: message.conversation_id,
      branchId: getSelectedSynonBiomedBranch(message.conversation_id) ?? undefined,
      operationId: message.content.session_id || message.id,
      path: 'timeline-plan',
    },
    { defaultExpanded: true }
  );
  const document = useMemo(
    (): SynonBiomedPlanDocument => ({
      version: 1,
      taskSummary: toolPublicDetailText(chinese, 'planTitle'),
      phases: [
        {
          id: `timeline-plan-${message.content.session_id || message.id}`,
          name: toolPublicDetailText(chinese, 'planSteps'),
          delegations: [],
          steps: message.content.entries.map(
            (entry, index): SynonBiomedPlanStep => ({
              id: `timeline-plan-step-${message.content.session_id || message.id}-${index}`,
              title: entry.content,
              description: '',
              status:
                entry.status === 'completed' ? 'completed' : entry.status === 'in_progress' ? 'in_progress' : 'pending',
            })
          ),
        },
      ],
      feasibility: null,
    }),
    [chinese, message]
  );

  return (
    <section className='synon-biomed-plan-review synon-biomed-plan-review--timeline' data-testid='timeline-plan'>
      <button
        type='button'
        className='synon-biomed-plan-review__timeline-trigger'
        aria-expanded={expanded}
        onClick={toggle}
      >
        <IconRight
          aria-hidden='true'
          className={`synon-biomed-plan-review__timeline-chevron${
            expanded ? ' synon-biomed-plan-review__timeline-chevron--expanded' : ''
          }`}
        />
        <span>{document.taskSummary}</span>
        <span className='synon-biomed-plan-review__timeline-count'>
          {toolPublicDetailText(chinese, 'stepCount', {
            count: message.content.entries.length,
          })}
        </span>
      </button>
      {expanded ? <SynonBiomedPlanTree document={document} /> : null}
    </section>
  );
};

export default SynonBiomedTimelinePlan;
