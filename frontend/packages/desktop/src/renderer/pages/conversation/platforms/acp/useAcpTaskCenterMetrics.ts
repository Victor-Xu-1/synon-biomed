import { useMemo } from 'react';
import { useMessageList } from '@/renderer/pages/conversation/Messages/hooks';

export function useAcpTaskCenterMetrics(tokenUsage: number | null | undefined) {
  const messageList = useMessageList();
  return useMemo(() => {
    const toolCallCount = messageList.filter((message) => message.type === 'tool_call').length;
    return {
      total: { tokenUsage: tokenUsage ?? null, modelCallCount: null as number | null, toolCallCount },
      latest: { tokenUsage: tokenUsage ?? null, modelCallCount: null as number | null, toolCallCount },
    };
  }, [messageList, tokenUsage]);
}
