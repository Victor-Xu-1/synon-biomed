import type { IMessageToolCall } from '@/common/chat/chatLib';

export function isStandaloneToolCall(message: IMessageToolCall): boolean {
  return (
    message.content.name === 'ask_user' || Boolean(message.content.subagent) || Boolean(message.content.subagentEvents)
  );
}
