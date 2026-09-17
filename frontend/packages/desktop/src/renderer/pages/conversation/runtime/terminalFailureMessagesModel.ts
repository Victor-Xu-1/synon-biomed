import type { TMessage } from '@/common/chat/chatLib';

export type TerminalFailureRollback = Array<{
  msgId: string;
  status: TMessage['status'];
  terminalSuperseded: TMessage['terminal_superseded'];
}>;

const isTerminalFailureMessageKind = (message: TMessage): boolean =>
  message.type === 'text' || (message.type === 'tips' && message.content.type === 'error');

const isTerminalFailure = (message: TMessage): boolean =>
  isTerminalFailureMessageKind(message) &&
  message.position !== 'right' &&
  message.terminal_superseded !== true &&
  (message.terminal_status === 'failed' || message.status === 'error');

const isSupersededTerminalFailure = (message: TMessage): boolean =>
  isTerminalFailureMessageKind(message) && message.position !== 'right' && message.terminal_superseded === true;

export const markTerminalFailuresSuperseded = (
  messages: TMessage[]
): { messages: TMessage[]; rollback: TerminalFailureRollback } => {
  const rollback: TerminalFailureRollback = [];
  const next = messages.map((message) => {
    if (!isTerminalFailure(message)) {
      return message;
    }
    rollback.push({
      msgId: message.msg_id,
      status: message.status,
      terminalSuperseded: message.terminal_superseded,
    });
    return { ...message, status: 'finish' as const, terminal_superseded: true };
  });
  return { messages: rollback.length > 0 ? next : messages, rollback };
};

/**
 * Present at most the latest unresolved terminal failure. A newer visible
 * message proves that the conversation continued, while an active runtime
 * proves a continuation was accepted before its first new message arrives.
 * This is a read-model projection only: partial assistant content remains in
 * history and the durable transcript is never rewritten by the renderer.
 */
export const projectTerminalFailuresForDisplay = (messages: TMessage[], activeRuntime: boolean): TMessage[] => {
  let newerActivity = activeRuntime;
  let changed = false;
  const next = [...messages];

  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];
    if (isSupersededTerminalFailure(message)) {
      if (message.hidden !== true) {
        next[index] = { ...message, hidden: true };
        changed = true;
      }
      continue;
    }
    if (isTerminalFailure(message)) {
      if (newerActivity) {
        next[index] = { ...message, status: 'finish', terminal_superseded: true, hidden: true };
        changed = true;
      } else {
        // Reserve the one current terminal failure; every older failure is
        // historical even when malformed input contains duplicate terminals.
        newerActivity = true;
      }
      continue;
    }
    if (message.hidden !== true) newerActivity = true;
  }

  return changed ? next : messages;
};

export const restoreTerminalFailures = (messages: TMessage[], rollback: TerminalFailureRollback): TMessage[] => {
  if (rollback.length === 0) return messages;
  const snapshots = new Map(rollback.map((entry) => [entry.msgId, entry]));
  let changed = false;
  const next = messages.map((message) => {
    const snapshot = snapshots.get(message.msg_id);
    if (
      snapshot === undefined ||
      !isTerminalFailureMessageKind(message) ||
      message.terminal_superseded !== true ||
      message.status !== 'finish'
    ) {
      return message;
    }
    changed = true;
    const restored = { ...message, status: snapshot.status };
    if (snapshot.terminalSuperseded === undefined) {
      delete restored.terminal_superseded;
    } else {
      restored.terminal_superseded = snapshot.terminalSuperseded;
    }
    return restored;
  });
  return changed ? next : messages;
};
