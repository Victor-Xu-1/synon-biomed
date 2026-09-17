import type { TMessage } from '@/common/chat/chatLib';

export type MessageItemMemoProps = {
  message: TMessage;
  branchState?: unknown;
  highlighted?: boolean;
  rowWidthClass: string;
  showCopyRow?: boolean;
  messageIndex?: number;
  isLastUserMessage?: boolean;
  showReadCursorDivider?: boolean;
  readCursorDividerFaded?: boolean;
  readCursorAgo?: string | null;
};

export const areMessageItemPropsEqual = (prev: MessageItemMemoProps, next: MessageItemMemoProps): boolean =>
  prev.message.id === next.message.id &&
  prev.message.content === next.message.content &&
  prev.message.position === next.message.position &&
  prev.message.type === next.message.type &&
  prev.message.status === next.message.status &&
  prev.message.terminal_status === next.message.terminal_status &&
  prev.message.terminal_superseded === next.message.terminal_superseded &&
  prev.branchState === next.branchState &&
  prev.highlighted === next.highlighted &&
  prev.rowWidthClass === next.rowWidthClass &&
  prev.showCopyRow === next.showCopyRow &&
  prev.messageIndex === next.messageIndex &&
  prev.isLastUserMessage === next.isLastUserMessage &&
  prev.showReadCursorDivider === next.showReadCursorDivider &&
  prev.readCursorDividerFaded === next.readCursorDividerFaded &&
  prev.readCursorAgo === next.readCursorAgo;
