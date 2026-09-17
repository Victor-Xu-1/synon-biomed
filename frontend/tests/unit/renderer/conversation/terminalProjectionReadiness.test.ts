import type { TMessage } from '@/common/chat/chatLib';
import { hasRenderableTerminalAnswerForLatestUserTurn } from '@/renderer/pages/conversation/Messages/terminalProjectionReadiness';
import { describe, expect, it } from 'vitest';

const textMessage = (
  id: string,
  position: 'left' | 'right',
  terminal_status?: 'completed' | 'failed' | 'cancelled',
  terminal_superseded = false,
  status: 'work' | 'finish' = terminal_status ? 'finish' : 'work'
): TMessage =>
  ({
    id,
    conversation_id: 'conversation-1',
    type: 'text',
    position,
    status,
    content: { content: id },
    ...(terminal_status ? { terminal_status } : {}),
    ...(terminal_superseded ? { terminal_superseded: true } : {}),
  }) as TMessage;

const toolMessage = (id: string): TMessage =>
  ({
    id,
    conversation_id: 'conversation-1',
    type: 'tool',
    position: 'left',
    status: 'finish',
    content: { content: id },
  }) as TMessage;

describe('terminalProjectionReadiness', () => {
  it('settles an explicitly terminal latest window after the user row has paged out', () => {
    const tail = [toolMessage('last-tool'), textMessage('answer', 'left', 'completed')];
    expect(
      hasRenderableTerminalAnswerForLatestUserTurn(tail, {
        isLatestWindow: true,
      })
    ).toBe(true);
    expect(
      hasRenderableTerminalAnswerForLatestUserTurn(tail, {
        isLatestWindow: false,
      })
    ).toBe(false);
    expect(hasRenderableTerminalAnswerForLatestUserTurn(tail)).toBe(false);
  });

  it('does not use an earlier terminal or unannotated prose as proof for a paged turn', () => {
    expect(
      hasRenderableTerminalAnswerForLatestUserTurn(
        [textMessage('old-answer', 'left', 'completed'), toolMessage('new-work')],
        { isLatestWindow: true }
      )
    ).toBe(false);
    expect(
      hasRenderableTerminalAnswerForLatestUserTurn([textMessage('unannotated', 'left', undefined, false, 'finish')], {
        isLatestWindow: true,
      })
    ).toBe(false);
    expect(
      hasRenderableTerminalAnswerForLatestUserTurn([textMessage('superseded', 'left', 'completed', true)], {
        isLatestWindow: true,
      })
    ).toBe(false);
  });
  it('requires a terminal assistant answer after the latest user turn', () => {
    expect(
      hasRenderableTerminalAnswerForLatestUserTurn([
        textMessage('user-1', 'right'),
        textMessage('answer-1', 'left', 'completed'),
        textMessage('user-2', 'right'),
        textMessage('progress-2', 'left'),
      ])
    ).toBe(false);

    expect(
      hasRenderableTerminalAnswerForLatestUserTurn([
        textMessage('user-1', 'right'),
        textMessage('answer-1', 'left', 'completed'),
        textMessage('user-2', 'right'),
        textMessage('answer-2', 'left', 'completed'),
      ])
    ).toBe(true);
  });

  it('ignores superseded terminal attempts for the current logical task', () => {
    expect(
      hasRenderableTerminalAnswerForLatestUserTurn([
        textMessage('user', 'right'),
        textMessage('superseded', 'left', 'failed', true),
      ])
    ).toBe(false);
  });

  it('accepts the final assistant row when terminal metadata arrives on the runtime event first', () => {
    expect(
      hasRenderableTerminalAnswerForLatestUserTurn([
        textMessage('user', 'right'),
        textMessage('progress', 'left'),
        toolMessage('save-artifacts'),
        textMessage('complete-answer', 'left', undefined, false, 'finish'),
      ])
    ).toBe(true);

    expect(
      hasRenderableTerminalAnswerForLatestUserTurn([
        textMessage('user', 'right'),
        textMessage('progress', 'left'),
        toolMessage('still-running'),
      ])
    ).toBe(false);
  });
});
