import type { ConversationCommandQueueItem } from '@/renderer/pages/conversation/platforms/useConversationCommandQueue';
import type { toSynonBiomedMessageSessionOptions } from '@/renderer/services/synonBiomedSessionOptions';

export type CommandExecutionAuthority = {
  targetBranchId: string | null;
  targetBranchRevision: number;
  sessionOptions: ReturnType<typeof toSynonBiomedMessageSessionOptions>;
};

export type DirectCommandRetry = {
  signature: string;
  item: ConversationCommandQueueItem;
  authority?: CommandExecutionAuthority;
};
