import {
  getActiveComposerReferenceQuery,
  insertSerializedComposerReference,
  serializeComposerReference,
  type ActiveComposerReferenceQuery,
  type ArtifactComposerReference,
  type ComposerReferenceItem,
} from './composerReferenceModel';
import { registerComposerReferenceSink, type ComposerArtifactReferenceInput } from './composerReferenceBridge';
import {
  areSelectionItemsEquivalent,
  buildOwnedSelectionItems,
  getSelectedItemMatchKeys,
  getSelectedItemPath,
  rememberSelectedItem,
} from './selectionModel';
import { buildAtFileInsertion } from '@/renderer/utils/chat/atFileQuery';
import { emitter, useAddEventListener } from '@/renderer/utils/emitter';
import { mergeFileSelectionItems, type FileSelectionItem } from '@/renderer/utils/file/fileSelection';
import type { FileOrFolderItem } from '@/renderer/utils/file/fileTypes';
import { useCallback, useEffect, useRef } from 'react';

type Props = {
  activeQuery: ActiveComposerReferenceQuery | null;
  activeTokenKey: string | null;
  allAtFileQueries: Array<{ query: string }>;
  conversationId?: string;
  conversationType?: string;
  getTextareaElement: () => HTMLTextAreaElement | null;
  input: string;
  latestInputRef: React.MutableRefObject<string>;
  onInvalidReference: () => void;
  onSelectArtifactReference?: (reference: ArtifactComposerReference) => void;
  onSelectedWorkspaceItemsChange?: (items: FileSelectionItem[]) => void;
  selectedWorkspaceItems?: FileSelectionItem[];
  setCaretPosition: (position: number) => void;
  setDismissedReferenceToken: (token: string | null) => void;
  setInput: (value: string) => void;
  setInputRef: React.MutableRefObject<(value: string) => void>;
};

export const useSendBoxReferenceSelection = ({
  activeQuery,
  activeTokenKey,
  allAtFileQueries,
  conversationId,
  conversationType,
  getTextareaElement,
  input,
  latestInputRef,
  onInvalidReference,
  onSelectArtifactReference,
  onSelectedWorkspaceItemsChange,
  selectedWorkspaceItems,
  setCaretPosition,
  setDismissedReferenceToken,
  setInput,
  setInputRef,
}: Props) => {
  const mentionOwnedPathsRef = useRef<Set<string>>(new Set());
  const everMentionOwnedPathsRef = useRef<Set<string>>(new Set());
  const externalOwnedPathsRef = useRef<Set<string>>(new Set());
  const selectedItemByPathRef = useRef<Map<string, FileSelectionItem>>(new Map());
  const suppressedExternalAppendPathsRef = useRef<Set<string>>(new Set());

  useEffect(() => {
    mentionOwnedPathsRef.current = new Set();
    everMentionOwnedPathsRef.current = new Set();
    externalOwnedPathsRef.current = new Set();
    selectedItemByPathRef.current = new Map();
    suppressedExternalAppendPathsRef.current = new Set();
  }, [conversationId]);

  useEffect(() => {
    if (!selectedWorkspaceItems || !onSelectedWorkspaceItemsChange) return;

    const mentionQueries = new Set(allAtFileQueries.map((item) => item.query));
    selectedWorkspaceItems.forEach((item) => rememberSelectedItem(selectedItemByPathRef.current, item));
    const nextMentionOwnedPaths = new Set<string>();
    for (const path of mentionOwnedPathsRef.current) {
      const item = selectedItemByPathRef.current.get(path);
      if (item && getSelectedItemMatchKeys(item).some((key) => mentionQueries.has(key))) {
        nextMentionOwnedPaths.add(path);
      }
    }
    for (const item of selectedWorkspaceItems) {
      const path = getSelectedItemPath(item);
      if (path && getSelectedItemMatchKeys(item).some((key) => mentionQueries.has(key))) {
        nextMentionOwnedPaths.add(path);
      }
    }

    const incomingPaths = new Set(
      selectedWorkspaceItems.map(getSelectedItemPath).filter((path): path is string => Boolean(path))
    );
    const nextExternalOwnedPaths = new Set(
      Array.from(externalOwnedPathsRef.current).filter((path) => incomingPaths.has(path))
    );
    for (const path of incomingPaths) {
      if (!nextMentionOwnedPaths.has(path) && !everMentionOwnedPathsRef.current.has(path)) {
        nextExternalOwnedPaths.add(path);
      }
    }

    mentionOwnedPathsRef.current = nextMentionOwnedPaths;
    nextMentionOwnedPaths.forEach((path) => everMentionOwnedPathsRef.current.add(path));
    externalOwnedPathsRef.current = nextExternalOwnedPaths;
    const nextItems = buildOwnedSelectionItems(
      selectedWorkspaceItems,
      nextMentionOwnedPaths,
      nextExternalOwnedPaths,
      selectedItemByPathRef.current
    );
    if (!areSelectionItemsEquivalent(selectedWorkspaceItems, nextItems)) onSelectedWorkspaceItemsChange(nextItems);
  }, [allAtFileQueries, onSelectedWorkspaceItemsChange, selectedWorkspaceItems]);

  const handleExternalSelectionAppend = useCallback((items: FileSelectionItem[]) => {
    for (const item of items) {
      const path = getSelectedItemPath(item);
      if (!path) continue;
      if (suppressedExternalAppendPathsRef.current.delete(path)) continue;
      rememberSelectedItem(selectedItemByPathRef.current, item);
      externalOwnedPathsRef.current.add(path);
    }
  }, []);

  useAddEventListener(
    'acp.selected.file.append',
    (items: FileSelectionItem[]) => {
      if (conversationType === 'acp') handleExternalSelectionAppend(items);
    },
    [conversationType, handleExternalSelectionAppend]
  );

  const emitSelectedFileAppend = useCallback(
    (item: FileOrFolderItem) => {
      if (conversationType === 'acp') emitter.emit('acp.selected.file.append', [item]);
    },
    [conversationType]
  );

  const insertSelectedReference = useCallback(
    (referenceItem: ComposerReferenceItem) => {
      if (!activeQuery) return;
      let nextValue: string;
      let nextCaret: number;
      if (referenceItem.kind === 'workspace') {
        const insertion = buildAtFileInsertion(referenceItem.item);
        if (!insertion) return;
        nextValue = input.slice(0, activeQuery.start) + insertion + input.slice(activeQuery.end);
        nextCaret = activeQuery.start + insertion.length;
        const path = getSelectedItemPath(referenceItem.item);
        if (path) {
          rememberSelectedItem(selectedItemByPathRef.current, referenceItem.item);
          mentionOwnedPathsRef.current.add(path);
          everMentionOwnedPathsRef.current.add(path);
          suppressedExternalAppendPathsRef.current.add(path);
        }
        if (selectedWorkspaceItems && onSelectedWorkspaceItemsChange) {
          onSelectedWorkspaceItemsChange(
            buildOwnedSelectionItems(
              mergeFileSelectionItems(selectedWorkspaceItems, [referenceItem.item]),
              mentionOwnedPathsRef.current,
              externalOwnedPathsRef.current,
              selectedItemByPathRef.current
            )
          );
        }
        emitSelectedFileAppend(referenceItem.item);
      } else if (referenceItem.kind === 'artifact' && onSelectArtifactReference) {
        if (!referenceItem.versionId?.trim()) {
          onInvalidReference();
          return;
        }
        onSelectArtifactReference(referenceItem);
        nextValue = input.slice(0, activeQuery.start) + input.slice(activeQuery.end);
        nextCaret = activeQuery.start;
      } else {
        try {
          const result = insertSerializedComposerReference(
            input,
            activeQuery,
            serializeComposerReference(referenceItem)
          );
          nextValue = result.value;
          nextCaret = result.caret;
        } catch {
          onInvalidReference();
          return;
        }
      }

      setDismissedReferenceToken(activeTokenKey);
      setInput(nextValue);
      requestAnimationFrame(() => {
        const textarea = getTextareaElement();
        if (!textarea) return;
        textarea.focus();
        textarea.setSelectionRange(nextCaret, nextCaret);
        setCaretPosition(nextCaret);
      });
    },
    [
      activeQuery,
      activeTokenKey,
      emitSelectedFileAppend,
      getTextareaElement,
      input,
      onInvalidReference,
      onSelectArtifactReference,
      onSelectedWorkspaceItemsChange,
      selectedWorkspaceItems,
      setCaretPosition,
      setDismissedReferenceToken,
      setInput,
    ]
  );

  const insertExternalArtifactReference = useCallback(
    (referenceInput: ComposerArtifactReferenceInput) => {
      if (onSelectArtifactReference && referenceInput.versionId?.trim()) {
        onSelectArtifactReference({
          kind: 'artifact',
          key: `artifact:${referenceInput.artifactId}:${referenceInput.versionId}`,
          label: referenceInput.filename,
          detail: '',
          projectId: null,
          projectName: null,
          isCurrentProject: false,
          artifactId: referenceInput.artifactId,
          versionId: referenceInput.versionId,
        });
        return;
      }
      const reference = serializeComposerReference({
        kind: 'artifact',
        key: `artifact:${referenceInput.artifactId}:${referenceInput.versionId}`,
        label: referenceInput.filename,
        detail: '',
        projectId: null,
        projectName: null,
        isCurrentProject: false,
        artifactId: referenceInput.artifactId,
        versionId: referenceInput.versionId,
      });
      const current = latestInputRef.current;
      const activeTextarea = getTextareaElement();
      const start = activeTextarea?.selectionStart ?? current.length;
      const end = activeTextarea?.selectionEnd ?? start;
      const activeReference = getActiveComposerReferenceQuery(current, start, '@');
      let nextValue: string;
      let nextCaret: number;
      if (activeReference) {
        const result = insertSerializedComposerReference(current, activeReference, reference);
        nextValue = result.value;
        nextCaret = result.caret;
      } else {
        const leadingSpace = start > 0 && !/\s/.test(current[start - 1]) ? ' ' : '';
        const trailingSpace = end < current.length && !/\s/.test(current[end]) ? ' ' : '';
        const insertion = `${leadingSpace}${reference}${trailingSpace}`;
        nextValue = current.slice(0, start) + insertion + current.slice(end);
        nextCaret = start + insertion.length;
      }
      setInputRef.current(nextValue);
      requestAnimationFrame(() => {
        const textarea = getTextareaElement();
        if (!textarea) return;
        textarea.focus();
        textarea.setSelectionRange(nextCaret, nextCaret);
        setCaretPosition(nextCaret);
      });
    },
    [getTextareaElement, latestInputRef, onSelectArtifactReference, setCaretPosition, setInputRef]
  );

  useEffect(() => {
    if (!conversationId) return;
    return registerComposerReferenceSink(conversationId, insertExternalArtifactReference);
  }, [conversationId, insertExternalArtifactReference]);

  const removeExternalSelection = useCallback(
    (item: FileSelectionItem) => {
      const path = getSelectedItemPath(item);
      if (!path || !onSelectedWorkspaceItemsChange) return;
      externalOwnedPathsRef.current.delete(path);
      onSelectedWorkspaceItemsChange(
        buildOwnedSelectionItems(
          selectedWorkspaceItems ?? [],
          mentionOwnedPathsRef.current,
          externalOwnedPathsRef.current,
          selectedItemByPathRef.current
        )
      );
    },
    [onSelectedWorkspaceItemsChange, selectedWorkspaceItems]
  );

  return { insertSelectedReference, removeExternalSelection };
};
