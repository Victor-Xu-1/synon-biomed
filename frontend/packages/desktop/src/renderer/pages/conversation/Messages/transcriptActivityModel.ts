import type { RealtimeConnectionStatus } from '@/common/adapter/realtimeClient';
import { normalizeToolMessages } from '@/common/chat/normalizeToolCall';
import type { ConversationRuntimeView } from '../runtime/conversationRuntimeViewStore';
import type { MessageListProcessedItem } from './messageListProjection';
import { isPublicToolActivity } from './toolActivityPresentationRegistry';

export type TranscriptActivityState = { labelKey: string; spinning: boolean } | null;

const status = (name: string, spinning = false): TranscriptActivityState => ({
  labelKey: `conversation.synonRuntime.runtimeOperations.${name}`,
  spinning,
});

// Task authority owns whether work continues; message rows only decide where
// to show the indicator. Never rewrite a settled tool's immutable outcome.
export function projectTranscriptActivity(
  view: ConversationRuntimeView | undefined,
  connection: RealtimeConnectionStatus,
  tail: MessageListProcessedItem | undefined
): TranscriptActivityState {
  if (!view || (!view.hasTask && !view.localSubmitting)) return null;
  if (view.state === 'idle' && !view.localSubmitting) return null;
  if (view.localSubmitting && !view.hasBackendRuntime) return status('taskStarting', connection === 'connected');
  if (!view.hydrated) return status('taskStatusLoading');
  if (view.hydrationError || !view.hasBackendRuntime || connection !== 'connected') {
    return { labelKey: 'conversation.syncNotice.unavailableTitle', spinning: false };
  }
  if (view.pendingConfirmations > 0 || view.state === 'waiting_approval' || view.state === 'waiting_confirmation') {
    return status('taskWaitingApproval');
  }
  if (view.state === 'waiting_input') return status('waitingForAnswer');
  if (view.state === 'paused') return status('taskPaused');
  if (view.state === 'cancelling' || view.localStopping) return status('taskCancelling');
  if (view.state === 'starting') return status('taskStarting', true);
  if (view.state !== 'running' || !view.isProcessing) return null;

  const tools =
    tail?.type === 'tool_summary'
      ? tail.messages
      : tail && (tail.type === 'tool_call' || tail.type === 'tool_group' || tail.type === 'acp_tool_call')
        ? [tail]
        : [];
  if (
    normalizeToolMessages(tools).some(
      (tool) =>
        isPublicToolActivity(tool.name) &&
        (tool.status === 'running' || tool.status === 'pending' || tool.status === 'waiting')
    )
  ) {
    return null;
  }
  return { labelKey: 'messages.processing', spinning: true };
}
