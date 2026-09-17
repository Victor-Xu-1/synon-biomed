import type { IResponseMessage } from '@/common/adapter/messageStreamProtocol';

export const MAX_STREAM_PUBLICATION_KEYS = 8192;

/** Idempotence fence for replayed durable message.stream publications. */
export class ConversationStreamPublicationFence {
  private readonly seen = new Set<string>();

  accept(message: Pick<IResponseMessage, 'publication_boundary_id' | 'source_publication_sequence'>): boolean {
    const boundary = message.publication_boundary_id;
    const sequence = message.source_publication_sequence;
    if (!boundary || sequence === undefined) return true;
    const key = `${boundary}\0${sequence}`;
    if (this.seen.has(key)) return false;
    this.seen.add(key);
    while (this.seen.size > MAX_STREAM_PUBLICATION_KEYS) {
      const oldest = this.seen.values().next().value;
      if (!oldest) break;
      this.seen.delete(oldest);
    }
    return true;
  }

  reset(): void {
    this.seen.clear();
  }
}
