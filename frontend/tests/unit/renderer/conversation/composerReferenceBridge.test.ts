import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  insertArtifactReferenceIntoActiveComposer,
  registerComposerReferenceSink,
} from '@/renderer/components/chat/SendBox/composerReferenceBridge';

describe('composer reference bridge', () => {
  let cleanup: (() => void) | undefined;
  afterEach(() => cleanup?.());

  it('inserts an exact artifact reference into the targeted active composer', () => {
    const sink = vi.fn();
    cleanup = registerComposerReferenceSink('conversation-a', sink);

    expect(
      insertArtifactReferenceIntoActiveComposer({
        conversationId: 'conversation-a',
        filename: 'analysis.csv',
        artifactId: 'artifact-1',
        versionId: 'version-2',
      })
    ).toBe(true);
    expect(sink).toHaveBeenCalledWith({
      conversationId: 'conversation-a',
      filename: 'analysis.csv',
      artifactId: 'artifact-1',
      versionId: 'version-2',
    });
  });

  it('rejects a non-versioned artifact so the send path cannot drift to latest', () => {
    const sink = vi.fn();
    cleanup = registerComposerReferenceSink('conversation-a', sink);
    expect(
      insertArtifactReferenceIntoActiveComposer({
        filename: 'latest.csv',
        artifactId: 'artifact-1',
      })
    ).toBe(false);
    expect(sink).not.toHaveBeenCalled();
  });
  it('returns false when no matching SendBox is mounted', () => {
    expect(
      insertArtifactReferenceIntoActiveComposer({
        conversationId: 'missing',
        filename: 'analysis.csv',
        artifactId: 'artifact-1',
        versionId: 'version-2',
      })
    ).toBe(false);
  });
});
