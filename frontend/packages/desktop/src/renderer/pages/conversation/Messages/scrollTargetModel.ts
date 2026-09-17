import type { TMessage } from '@/common/chat/chatLib';

/** A logical task sent by the user, rather than an assistant/tool/status row. */
export const isUserTaskMessage = (message: TMessage): boolean => {
  if (message.hidden || message.type !== 'text' || message.position !== 'right') return false;

  const blockIndex = message.content.synonBiomed?.blockIndex;
  return blockIndex === undefined || blockIndex === 0;
};

/** Find the closest earlier logical user task before a rendered message index. */
export const findPreviousUserTaskIndex = (messages: TMessage[], referenceIndex: number): number => {
  const startIndex = Math.min(Math.max(referenceIndex, 0), messages.length);
  for (let index = startIndex - 1; index >= 0; index -= 1) {
    if (isUserTaskMessage(messages[index])) return index;
  }
  return -1;
};
