import { Right } from '@icon-park/react';
import SynonBiomedAvatar from '@/renderer/components/synonBiomed/SynonBiomedAvatar';
import React from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import type { IMessageToolCall, IToolCallSubagent } from '@/common/chat/chatLib';
import './MessageSubagentTurn.css';

type SubagentVisualState = 'running' | 'completed' | 'failed' | 'canceled';

function normalizeSubagentState(subagent: IToolCallSubagent): SubagentVisualState {
  if (subagent.superseded) return 'completed';
  const status = subagent.status?.toLowerCase() ?? '';
  if (['processing', 'running', 'executing', 'in_progress', 'in-progress', 'queued', 'pending'].includes(status)) {
    return 'running';
  }
  if (['awaiting_user_response', 'awaiting_plan_approval', 'needs_input', 'awaiting_input'].includes(status)) {
    // Delegate payloads do not carry the authoritative pending-request or plan
    // evidence. Keep legacy status hints active until the root conversation's
    // canonical task projection supplies a real user-action state.
    return 'running';
  }
  if (['failed', 'error'].includes(status)) return 'failed';
  if (['cancelled', 'canceled'].includes(status)) return 'canceled';
  return 'completed';
}

function subagentName(subagent: IToolCallSubagent): string {
  return subagent.delegateName || subagent.agentName || 'Synon Biomed';
}

const MessageSubagentTurn: React.FC<{ message: IMessageToolCall }> = ({ message }) => {
  const navigate = useNavigate();
  const { t } = useTranslation();
  const subagent = message.content.subagent;

  if (!subagent) return null;

  const state = normalizeSubagentState(subagent);
  const name = subagentName(subagent);
  const title = t('conversation.subagentTurn.title', { index: subagent.ordinal, name });
  const activity =
    subagent.latestAction || subagent.statusDescription || subagent.taskSummary || message.content.description || name;
  const count = subagent.messageCount ?? 0;
  const messageMeta = `${t(`conversation.subagentTurn.status.${state}`)}${
    count > 0
      ? ` · ${t(count === 1 ? 'conversation.subagentTurn.messageCountOne' : 'conversation.subagentTurn.messageCount', {
          count,
        })}`
      : ''
  }`;
  const linked = Boolean(subagent.frameId);
  const ariaLabel = linked
    ? t('conversation.subagentTurn.open', { index: subagent.ordinal, name })
    : t('conversation.subagentTurn.creating', { index: subagent.ordinal, name });

  return (
    <button
      type='button'
      className='subagent-turn'
      aria-label={ariaLabel}
      disabled={!linked}
      data-subagent-state={state}
      onClick={() => subagent.frameId && navigate(`/conversation/${subagent.frameId}`)}
    >
      <span className={`subagent-turn__status subagent-turn__status--${state}`} aria-hidden='true' />
      <SynonBiomedAvatar size={15} className='subagent-turn__icon' />
      <span className='subagent-turn__content'>
        <span className='subagent-turn__title'>{title}</span>
        <span
          className={
            state === 'running' ? 'subagent-turn__activity subagent-turn__activity--running' : 'subagent-turn__activity'
          }
        >
          {activity}
        </span>
      </span>
      <span className='subagent-turn__meta'>{messageMeta}</span>
      {linked && <Right theme='outline' size='12' className='subagent-turn__arrow' />}
    </button>
  );
};

export default React.memo(MessageSubagentTurn);
