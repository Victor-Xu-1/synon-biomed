import type { TMessage } from '@/common/chat/chatLib';

/**
 * A terminal capsule may settle only after the authoritative terminal answer
 * for the latest user turn is present in the rendered message window.
 */
export const hasRenderableTerminalAnswerForLatestUserTurn = (
  messages: TMessage[],
  options: { isLatestWindow?: boolean } = {}
): boolean => {
  if (options.isLatestWindow === false) return false;
  let latestUserMessageIndex = -1;
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];
    if (message.type === 'text' && message.position === 'right' && !message.hidden) {
      latestUserMessageIndex = index;
      break;
    }
  }
  if (latestUserMessageIndex < 0) {
    // Long tasks can page the user row out of the live tail. Only an explicit
    // durable terminal annotation at the newest visible row may settle that
    // case; a historical page or merely finished prose is insufficient.
    if (options.isLatestWindow !== true) return false;
    const tail = messages.toReversed().find((message) => !message.hidden);
    return Boolean(
      tail?.type === 'text' &&
      tail.position === 'left' &&
      tail.terminal_superseded !== true &&
      (tail.terminal_status === 'completed' ||
        tail.terminal_status === 'failed' ||
        tail.terminal_status === 'cancelled')
    );
  }

  const turnMessages = messages.slice(latestUserMessageIndex + 1);
  if (
    turnMessages.some(
      (message) =>
        message.type === 'text' &&
        message.position === 'left' &&
        !message.hidden &&
        message.terminal_superseded !== true &&
        (message.terminal_status === 'completed' ||
          message.terminal_status === 'failed' ||
          message.terminal_status === 'cancelled')
    )
  ) {
    return true;
  }

  // The terminal runtime event and the assistant message are separate durable
  // publications. Some incremental projections receive the complete assistant
  // message before its terminal annotation is folded onto that same row. This
  // helper is evaluated only while backend terminal authority is pending, so a
  // finished left-hand text that follows every tool/activity row is the same
  // terminal answer, not permission to infer task completion from prose alone.
  let latestActivityIndex = -1;
  let latestFinishedAssistantIndex = -1;
  turnMessages.forEach((message, index) => {
    if (message.hidden) return;
    if (
      message.type === 'text' &&
      message.position === 'left' &&
      message.status === 'finish' &&
      message.terminal_superseded !== true
    ) {
      latestFinishedAssistantIndex = index;
      return;
    }
    if (message.type !== 'text' || message.position !== 'right') {
      latestActivityIndex = index;
    }
  });
  return latestFinishedAssistantIndex >= 0 && latestFinishedAssistantIndex > latestActivityIndex;
};
