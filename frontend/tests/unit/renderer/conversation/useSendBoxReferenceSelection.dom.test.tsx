import { act, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { insertArtifactReferenceIntoActiveComposer } from '@/renderer/components/chat/SendBox/composerReferenceBridge';
import {
  getActiveComposerReferenceQuery,
  type ArtifactComposerReference,
} from '@/renderer/components/chat/SendBox/composerReferenceModel';
import { useSendBoxReferenceSelection } from '@/renderer/components/chat/SendBox/useSendBoxReferenceSelection';

const artifact = (): ArtifactComposerReference => ({
  kind: 'artifact',
  key: 'artifact:artifact-1:version-2',
  label: 'analysis.csv',
  detail: 'Project A',
  projectId: 'project-a',
  projectName: 'Project A',
  isCurrentProject: true,
  artifactId: 'artifact-1',
  versionId: 'version-2',
});

describe('useSendBoxReferenceSelection', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('turns an @ artifact result into structured context and removes only the active query', () => {
    vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
      callback(0);
      return 1;
    });
    const input = '分析 @ana';
    const activeQuery = getActiveComposerReferenceQuery(input, input.length, '@');
    const onSelectArtifactReference = vi.fn();
    const setInput = vi.fn();
    const { result } = renderHook(() =>
      useSendBoxReferenceSelection({
        activeQuery,
        activeTokenKey: activeQuery?.token ?? null,
        allAtFileQueries: [],
        conversationId: 'conversation-a',
        conversationType: 'acp',
        getTextareaElement: () => null,
        input,
        latestInputRef: { current: input },
        onInvalidReference: vi.fn(),
        onSelectArtifactReference,
        setCaretPosition: vi.fn(),
        setDismissedReferenceToken: vi.fn(),
        setInput,
        setInputRef: { current: vi.fn() },
      })
    );

    act(() => result.current.insertSelectedReference(artifact()));

    expect(onSelectArtifactReference).toHaveBeenCalledWith(artifact());
    expect(setInput).toHaveBeenCalledWith('分析 ');
  });

  it('routes a generated file Add-to-chat action through the same exact-version context sink', () => {
    const onSelectArtifactReference = vi.fn();
    renderHook(() =>
      useSendBoxReferenceSelection({
        activeQuery: null,
        activeTokenKey: null,
        allAtFileQueries: [],
        conversationId: 'conversation-a',
        conversationType: 'acp',
        getTextareaElement: () => null,
        input: '',
        latestInputRef: { current: '' },
        onInvalidReference: vi.fn(),
        onSelectArtifactReference,
        setCaretPosition: vi.fn(),
        setDismissedReferenceToken: vi.fn(),
        setInput: vi.fn(),
        setInputRef: { current: vi.fn() },
      })
    );

    act(() => {
      expect(
        insertArtifactReferenceIntoActiveComposer({
          conversationId: 'conversation-a',
          filename: 'analysis.csv',
          artifactId: 'artifact-1',
          versionId: 'version-2',
        })
      ).toBe(true);
    });

    expect(onSelectArtifactReference).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: 'artifact',
        artifactId: 'artifact-1',
        versionId: 'version-2',
        label: 'analysis.csv',
      })
    );
  });
});
