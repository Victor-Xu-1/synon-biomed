import { ipcBridge } from '@/common';
import { useCallback, useSyncExternalStore } from 'react';
import { loadConversationStreamingSnapshot } from './conversationStreamingApi';
import { ConversationToolStdoutStore } from './conversationToolStdoutStore';
import { registerRendererAccountReset } from '../rendererAccountScope';

// One root-scoped external store owns high-frequency auxiliary streaming.
// Durable message.stream remains the sole text/thinking/tool lifecycle authority.
const store = new ConversationToolStdoutStore({
  subscribeChunks: (listener) => ipcBridge.realtime.toolStdoutChunk.on(listener),
  subscribeReconnect: (listener) => ipcBridge.realtime.reconnected.on(listener),
  hydrate: (rootFrameId, signal) =>
    loadConversationStreamingSnapshot(rootFrameId, { signal }).then((snapshot) => snapshot.frames),
  schedulePublish: (callback) => requestAnimationFrame(callback),
  cancelScheduledPublish: (handle) => cancelAnimationFrame(Number(handle)),
});

registerRendererAccountReset('conversation-tool-stdout', () => store.clearCachedData());

export function useConversationStreaming(rootFrameId: string) {
  const subscribe = useCallback((listener: () => void) => store.subscribe(rootFrameId, listener), [rootFrameId]);
  const getSnapshot = useCallback(() => store.getSnapshot(rootFrameId), [rootFrameId]);
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}

export function readConversationStreamingNow(rootFrameId: string) {
  return store.getCurrentSnapshot(rootFrameId);
}
