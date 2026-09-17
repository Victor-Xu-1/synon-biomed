import { describe, expect, it } from 'vitest';
import {
  ConversationStreamPublicationFence,
  MAX_STREAM_PUBLICATION_KEYS,
} from '@/renderer/services/runtime/conversationStreamPublicationFence';

describe('ConversationStreamPublicationFence', () => {
  it('accepts each durable publication once across replay and recovery delivery', () => {
    const fence = new ConversationStreamPublicationFence();
    const publication = { publication_boundary_id: 'root:attempt-1', source_publication_sequence: 369 };
    expect(fence.accept(publication)).toBe(true);
    expect(fence.accept(publication)).toBe(false);
    expect(fence.accept({ ...publication, source_publication_sequence: 370 })).toBe(true);
    expect(fence.accept({ ...publication, publication_boundary_id: 'root:attempt-2' })).toBe(true);
  });

  it('leaves provider events without durable coordinates untouched and resets by conversation', () => {
    const fence = new ConversationStreamPublicationFence();
    expect(fence.accept({})).toBe(true);
    expect(fence.accept({})).toBe(true);
    const publication = { publication_boundary_id: 'root:attempt-1', source_publication_sequence: 1 };
    expect(fence.accept(publication)).toBe(true);
    fence.reset();
    expect(fence.accept(publication)).toBe(true);
  });

  it('keeps memory bounded while retaining recent publication identities', () => {
    const fence = new ConversationStreamPublicationFence();
    for (let sequence = 1; sequence <= MAX_STREAM_PUBLICATION_KEYS + 1; sequence += 1) {
      expect(fence.accept({ publication_boundary_id: 'root', source_publication_sequence: sequence })).toBe(true);
    }
    expect(fence.accept({ publication_boundary_id: 'root', source_publication_sequence: 1 })).toBe(true);
    expect(
      fence.accept({ publication_boundary_id: 'root', source_publication_sequence: MAX_STREAM_PUBLICATION_KEYS + 1 })
    ).toBe(false);
  });
});
