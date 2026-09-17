import { Attention, CheckOne, MessageOne, Right } from '@icon-park/react';
import SynonBiomedAvatar from '@/renderer/components/synonBiomed/SynonBiomedAvatar';
import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import type { IMessageToolCall, IToolCallSubagentEvent } from '@/common/chat/chatLib';
import './MessageSubagentEvents.css';

function formatDuration(seconds: number | undefined, locale: string, unit: string): string {
  if (seconds === undefined) return '';
  return `${new Intl.NumberFormat(locale).format(Math.max(0, Math.round(seconds)))}${unit}`;
}

const MessageSubagentEvents: React.FC<{ message: IMessageToolCall }> = ({ message }) => {
  const navigate = useNavigate();
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const events = message.content.subagentEvents ?? [];
  const questions = events.filter((event) => event.kind === 'question');
  const notes = events.filter((event) => event.kind !== 'question');
  const completionCount = notes.filter((event) => event.kind === 'completion').length;
  const messageCount = notes.filter((event) => event.kind === 'info').length;
  const header = t('conversation.subagentEvents.summary', {
    messages: t(
      messageCount === 1 ? 'conversation.subagentEvents.messageCountOne' : 'conversation.subagentEvents.messageCount',
      { count: messageCount }
    ),
    completed: t(
      completionCount === 1
        ? 'conversation.subagentEvents.completionCountOne'
        : 'conversation.subagentEvents.completionCount',
      { count: completionCount }
    ),
  });

  if (events.length === 0) return null;

  return (
    <div className='subagent-events'>
      {notes.length === 1 ? (
        <SubagentNote event={notes[0]} onOpen={() => navigate(`/conversation/${notes[0].frameId}`)} />
      ) : notes.length > 1 ? (
        <div className='subagent-events__group'>
          <button
            type='button'
            className='subagent-events__group-header'
            aria-label={header}
            aria-expanded={expanded}
            onClick={() => setExpanded((value) => !value)}
          >
            <SynonBiomedAvatar size={14} />
            <span>{header}</span>
            <Right
              theme='outline'
              size='12'
              className={
                expanded
                  ? 'subagent-events__group-arrow subagent-events__group-arrow--open'
                  : 'subagent-events__group-arrow'
              }
            />
          </button>
          {expanded && (
            <div className='subagent-events__group-body'>
              {notes.map((event, index) => (
                <SubagentNote
                  key={`${event.frameId}-${event.kind}-${index}`}
                  event={event}
                  onOpen={() => navigate(`/conversation/${event.frameId}`)}
                />
              ))}
            </div>
          )}
        </div>
      ) : null}

      {questions.map((event, index) => {
        const title = t('conversation.subagentEvents.questionTitle', { name: event.childName });
        const ariaLabel = t('conversation.subagentEvents.openQuestion', { name: event.childName });
        return (
          <button
            type='button'
            key={`${event.frameId}-question-${index}`}
            className='subagent-question'
            aria-label={ariaLabel}
            onClick={() => navigate(`/conversation/${event.frameId}`)}
          >
            <span className='subagent-question__header'>
              <Attention theme='outline' size='14' />
              <span>{title}</span>
              <Right theme='outline' size='12' className='subagent-question__arrow' />
            </span>
            <span className='subagent-question__text'>{event.text}</span>
          </button>
        );
      })}
    </div>
  );
};

const SubagentNote: React.FC<{
  event: IToolCallSubagentEvent;
  onOpen: () => void;
}> = ({ event, onOpen }) => {
  const { t, i18n } = useTranslation();
  const title = t('conversation.subagentEvents.noteTitle', { index: event.ordinal, name: event.childName });
  const duration = formatDuration(
    event.wallSeconds,
    i18n.resolvedLanguage || i18n.language,
    t('common.unit.second_short')
  );
  return (
    <button
      type='button'
      className='subagent-note'
      aria-label={`${title}: ${event.text ?? event.bullets?.join(' · ') ?? ''}`}
      onClick={onOpen}
    >
      {event.kind === 'completion' ? (
        <CheckOne theme='outline' size='14' className='subagent-note__icon subagent-note__icon--success' />
      ) : (
        <MessageOne theme='outline' size='14' className='subagent-note__icon' />
      )}
      <span className='subagent-note__content'>
        <span className='subagent-note__title'>{title}</span>
        {event.text && <span className='subagent-note__text'>{event.text}</span>}
        {event.bullets && event.bullets.length > 0 && (
          <span className='subagent-note__bullets'>
            {event.bullets.map((bullet) => (
              <span key={bullet}>{bullet}</span>
            ))}
          </span>
        )}
      </span>
      {duration && <span className='subagent-note__duration'>{duration}</span>}
      <Right theme='outline' size='12' className='subagent-note__arrow' />
    </button>
  );
};

export default React.memo(MessageSubagentEvents);
