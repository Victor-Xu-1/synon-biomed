import { requestSynonBiomedJson, type SynonBiomedGatewayOptions } from '../synonBiomedHttp';
import {
  normalizeConversationStreamingSnapshot,
  type ConversationStreamingSnapshot,
} from './conversationStreamingModel';

export type ConversationStreamingOptions = SynonBiomedGatewayOptions & {
  signal?: AbortSignal;
};

export async function loadConversationStreamingSnapshot(
  rootFrameId: string,
  options: ConversationStreamingOptions = {}
): Promise<ConversationStreamingSnapshot> {
  const payload = await requestSynonBiomedJson<unknown>(
    `/api/frames/${encodeURIComponent(rootFrameId)}/streaming-batch`,
    {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: '{}',
      signal: options.signal,
    },
    options
  );
  return normalizeConversationStreamingSnapshot(payload);
}
