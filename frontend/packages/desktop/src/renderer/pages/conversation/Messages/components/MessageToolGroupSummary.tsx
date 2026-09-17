import { IconRight } from '@arco-design/web-react/icon';
import React, { useContext, useLayoutEffect, useMemo, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import type { NormalizedToolCall, NormalizedToolStatus, ToolMessage } from '@/common/chat/normalizeToolCall';
import { isActiveToolStatus, normalizeToolMessages } from '@/common/chat/normalizeToolCall';
import { useConversationDisclosure } from '@/renderer/services/runtime/conversationDisclosureStore';
import { isPublicToolActivity } from '../toolActivityPresentationRegistry';
import { collapseNestedConnectorDispatches } from '../toolSummaryGroupingModel';
import './MessageToolGroupSummary.css';
import { groupRepeatedNonExecutingTools, toolWasNotExecuted } from './toolExecutionDisposition';
import ToolOperationDetail from './ToolOperationDetail';
import TranscriptActivity, { TranscriptActivityContext } from './TranscriptActivity';
import { buildToolStepGroupSummary } from './toolStepSummaryModel';

const aggregateStatus = (tools: NormalizedToolCall[]): NormalizedToolStatus => {
  if (tools.some((tool) => tool.status === 'running')) return 'running';
  if (tools.some((tool) => tool.status === 'waiting')) return 'waiting';
  if (tools.some((tool) => tool.status === 'blocked')) return 'blocked';
  if (tools.some((tool) => tool.status === 'error' && !toolWasNotExecuted(tool))) return 'error';
  if (tools.some((tool) => tool.status === 'interrupted')) return 'interrupted';
  if (tools.some((tool) => tool.status === 'unknown')) return 'unknown';
  if (tools.some((tool) => tool.status === 'canceled')) return 'canceled';
  if (tools.some((tool) => tool.status === 'pending')) return 'pending';
  return 'completed';
};

const MessageToolGroupSummary: React.FC<{
  messages: ToolMessage[];
}> = ({ messages }) => {
  const activity = useContext(TranscriptActivityContext);
  const { i18n } = useTranslation();
  const tools = useMemo(
    () =>
      collapseNestedConnectorDispatches(
        normalizeToolMessages(messages).filter((tool) => isPublicToolActivity(tool.name))
      ),
    [messages]
  );
  const groupStatus = useMemo(() => aggregateStatus(tools), [tools]);
  const presentationGroups = useMemo(() => groupRepeatedNonExecutingTools(tools), [tools]);
  const isActive = tools.some((tool) => isActiveToolStatus(tool.status));
  const disclosure = useConversationDisclosure(
    {
      conversationId: tools[0]?.conversationId ?? 'unknown-conversation',
      branchId: tools[0]?.branchId,
      operationId: tools[0]?.key ?? 'empty-group',
      path: 'group',
    },
    { defaultExpanded: true }
  );
  const expanded = disclosure.expanded;
  const detailsRef = useRef<HTMLDivElement>(null);
  const detailsId = useMemo(() => `tool-group-summary-details-${tools[0]?.key ?? 'empty'}`, [tools]);
  const groupSummary = useMemo(
    () => buildToolStepGroupSummary(tools, i18n?.language ?? 'en-US'),
    [i18n?.language, tools]
  );

  useLayoutEffect(() => {
    const details = detailsRef.current as (HTMLDivElement & { inert?: boolean }) | null;
    if (details) details.inert = !expanded;
  }, [expanded]);

  if (tools.length === 0) return null;
  if (tools.length === 1) {
    return (
      <div className='tool-group-summary tool-group-summary--single'>
        <ToolOperationDetail key={tools[0].key} item={tools[0]} nested activity={activity} />
      </div>
    );
  }

  return (
    <div className={`tool-group-summary${expanded ? ' tool-group-summary--expanded' : ''}`}>
      <button
        type='button'
        data-testid='tool-group-header'
        className={`tool-group-summary__header tool-step--${groupStatus}`}
        aria-expanded={expanded}
        aria-controls={detailsId}
        onClick={disclosure.toggle}
      >
        <span className='tool-group-summary__disclosure' aria-hidden='true'>
          {!expanded && activity?.spinning ? (
            <TranscriptActivity activity={activity} />
          ) : (
            <IconRight
              className={`tool-group-summary__disclosure-icon${
                expanded ? ' tool-group-summary__disclosure-icon--expanded' : ''
              }`}
              style={{ fontSize: 13 }}
            />
          )}
        </span>
        <span className={`tool-group-summary__headline${isActive ? ' tool-group-summary__headline--active' : ''}`}>
          {groupSummary.headline}
          {!expanded && groupSummary.subject ? ` · ${groupSummary.subject}` : ''}
        </span>
        <span className='tool-group-summary__meta'>{groupSummary.meta}</span>
      </button>
      <div
        ref={detailsRef}
        id={detailsId}
        className='tool-group-summary__details'
        aria-hidden={!expanded}
        data-expanded={expanded ? 'true' : 'false'}
      >
        {expanded ? (
          <div className='tool-group-summary__details-inner'>
            {presentationGroups.map(({ item, repeatCount }, index) => (
              <ToolOperationDetail
                key={item.key}
                item={item}
                nested
                repeatCount={repeatCount}
                activity={index === presentationGroups.length - 1 ? activity : null}
              />
            ))}
          </div>
        ) : null}
      </div>
    </div>
  );
};

export default React.memo(MessageToolGroupSummary);
