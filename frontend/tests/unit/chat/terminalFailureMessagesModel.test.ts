import { describe, expect, it } from 'vitest';
import type { IMessageText, IMessageTips, TMessage } from '@/common/chat/chatLib';
import {
  markTerminalFailuresSuperseded,
  projectTerminalFailuresForDisplay,
  restoreTerminalFailures,
} from '@/renderer/pages/conversation/runtime/terminalFailureMessagesModel';

const failedMessage = (msgId: string): IMessageText => ({
  id: msgId,
  msg_id: msgId,
  conversation_id: 'conversation-1',
  type: 'text',
  position: 'left',
  status: 'error',
  terminal_status: 'failed',
  content: { content: 'partial response' },
});

const failedTip = (msgId: string): IMessageTips => ({
  id: msgId,
  msg_id: msgId,
  conversation_id: 'conversation-1',
  type: 'tips',
  position: 'center',
  status: 'error',
  terminal_status: 'failed',
  content: { content: 'response incomplete', type: 'error' },
});

describe('terminal failure continuation state', () => {
  it('supersedes every visible historical failure when continuation starts', () => {
    const completed: IMessageText = {
      ...failedMessage('completed'),
      status: 'finish',
      terminal_status: 'completed',
    };
    const original: TMessage[] = [failedMessage('failed-1'), completed, failedMessage('failed-2')];

    const marked = markTerminalFailuresSuperseded(original);

    expect(marked.rollback.map((entry) => entry.msgId)).toEqual(['failed-1', 'failed-2']);
    expect(marked.messages[0]).toMatchObject({ status: 'finish', terminal_superseded: true });
    expect(marked.messages[1]).toBe(completed);
    expect(marked.messages[2]).toMatchObject({ status: 'finish', terminal_superseded: true });
    expect(restoreTerminalFailures(marked.messages, marked.rollback)).toEqual(original);
  });

  it('supersedes and hides a rendered terminal error card when continuation starts', () => {
    const original: TMessage[] = [failedTip('failed-tip')];

    const marked = markTerminalFailuresSuperseded(original);
    const projected = projectTerminalFailuresForDisplay(marked.messages, true);

    expect(marked.rollback).toEqual([{ msgId: 'failed-tip', status: 'error', terminalSuperseded: undefined }]);
    expect(projected[0]).toMatchObject({
      type: 'tips',
      status: 'finish',
      terminal_superseded: true,
      hidden: true,
    });
    expect(restoreTerminalFailures(marked.messages, marked.rollback)).toEqual(original);
  });

  it('does not revive a failure after a newer durable state replaced the optimistic marker', () => {
    const marked = markTerminalFailuresSuperseded([failedMessage('failed-1')]);
    const newer = [{ ...marked.messages[0], status: 'work' as const, terminal_superseded: false }];

    expect(restoreTerminalFailures(newer, marked.rollback)).toBe(newer);
  });

  it('supersedes an error projection even when its terminal status was normalized to cancellation', () => {
    const normalized = {
      ...failedMessage('normalized-failure'),
      terminal_status: 'cancelled' as const,
    };

    const marked = markTerminalFailuresSuperseded([normalized]);

    expect(marked.messages[0]).toMatchObject({ status: 'finish', terminal_superseded: true });
    expect(restoreTerminalFailures(marked.messages, marked.rollback)).toEqual([normalized]);
  });

  it('hides every historical failure while a continuation is active', () => {
    const original = [failedMessage('failed-1'), failedMessage('failed-2')];

    const projected = projectTerminalFailuresForDisplay(original, true);

    expect(projected).not.toBe(original);
    expect(projected).toEqual([
      expect.objectContaining({ status: 'finish', terminal_superseded: true, hidden: true }),
      expect.objectContaining({ status: 'finish', terminal_superseded: true, hidden: true }),
    ]);
  });

  it('hides a failure already superseded by an accepted continuation', () => {
    const marked = markTerminalFailuresSuperseded([failedMessage('failed-1')]);

    const projected = projectTerminalFailuresForDisplay(marked.messages, true);

    expect(projected[0]).toMatchObject({
      status: 'finish',
      terminal_superseded: true,
      hidden: true,
    });
  });

  it('keeps only the newest unresolved failure visible after refresh', () => {
    const continued: IMessageText = {
      ...failedMessage('continued'),
      status: 'finish',
      terminal_status: 'completed',
      content: { content: 'continued response' },
    };
    const latest = failedMessage('latest-failure');
    const original: TMessage[] = [failedMessage('historical-failure'), continued, latest];

    const projected = projectTerminalFailuresForDisplay(original, false);

    expect(projected[0]).toMatchObject({ status: 'finish', terminal_superseded: true, hidden: true });
    expect(projected[1]).toBe(continued);
    expect(projected[2]).toBe(latest);
  });

  it('does not allocate when the only failure is still current', () => {
    const original: TMessage[] = [failedMessage('current-failure')];

    expect(projectTerminalFailuresForDisplay(original, false)).toBe(original);
  });
});
