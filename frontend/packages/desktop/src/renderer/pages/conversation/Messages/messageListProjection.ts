import type {
  IConversationArtifact,
  IScientificFilesArtifact,
  ISynonBiomedScientificFile,
} from '@/common/adapter/ipcBridge';
import type { ArtifactReferenceRelation } from '@/common/adapter/messageStreamProtocol';
import type { IMessageAcpToolCall, IMessageToolCall, IMessageToolGroup, TMessage } from '@/common/chat/chatLib';
import { isPendingSynonBiomedAskUserHistory } from '@/renderer/components/synonBiomed/runtime/askUserHistoryModel';
import { parseDiff, type FileChangeInfo } from '@/renderer/utils/file/diffUtils';
import { hasRenderableMessageText } from './components/messageTextVisibility';
import {
  indexScientificFilesByArtifactReference,
  matchScientificFilesToArtifactReferences,
} from './components/artifactReferenceModel';
import { artifactVersionIdsInPresentedContent } from './components/artifactReferencePresentation';
import { isStandaloneToolCall } from './messagePresentationModel';
import { isPublicToolActivity } from './toolActivityPresentationRegistry';
import { startsNewToolSummary } from './toolSummaryGroupingModel';
import type { WriteFileResult } from './types';

export type MessageListMessageItem =
  | TMessage
  | {
      type: 'file_summary';
      id: string;
      diffs: FileChangeInfo[];
      sourceMessageIds: string[];
      created_at: number;
    }
  | {
      type: 'tool_summary';
      id: string;
      messages: Array<IMessageToolGroup | IMessageAcpToolCall | IMessageToolCall>;
      sourceMessageIds: string[];
      created_at: number;
    };

export type MessageListReferencedFilesItem = {
  type: 'referenced_files';
  id: string;
  files: ISynonBiomedScientificFile[];
  sourceMessageIds: string[];
  created_at: number;
};

type InlineConversationArtifact = Exclude<IConversationArtifact, IScientificFilesArtifact>;

export type MessageListArtifactItem = {
  type: 'artifact';
  id: string;
  artifact: InlineConversationArtifact;
  created_at: number;
};

export type MessageListProcessedItem =
  | MessageListMessageItem
  | MessageListArtifactItem
  | MessageListReferencedFilesItem;

export const getProcessedItemSourceMessageIds = (item: MessageListProcessedItem): string[] => {
  if ('type' in item && item.type === 'artifact') return [item.id];
  if (
    'type' in item &&
    (item.type === 'tool_summary' || item.type === 'file_summary' || item.type === 'referenced_files')
  ) {
    return item.sourceMessageIds;
  }
  return 'id' in item ? [item.id] : [];
};

export const matchesTargetMessage = (item: MessageListProcessedItem, targetMessageId?: string): boolean =>
  Boolean(targetMessageId && getProcessedItemSourceMessageIds(item).includes(targetMessageId));

export const getProcessedItemAnchorId = (item: MessageListProcessedItem): string => {
  if ('type' in item && item.type === 'referenced_files') return item.id;
  return getProcessedItemSourceMessageIds(item)[0] || item.id;
};

export const getProcessedItemRowKey = (item: MessageListProcessedItem): string => `${item.type}:${item.id}`;

export const getProcessedItemCreatedAt = (item: MessageListProcessedItem): number => {
  if ('type' in item && ['file_summary', 'tool_summary', 'referenced_files', 'artifact'].includes(item.type)) {
    return item.created_at;
  }
  return item.created_at ?? 0;
};

export const buildMessagePresentationList = (
  presentationList: TMessage[],
  artifacts: IConversationArtifact[],
  groupBoundaryMessageIds: string[] = []
): MessageListProcessedItem[] => {
  const result: MessageListMessageItem[] = [];
  let diffsChanges: FileChangeInfo[] = [];
  let diffsSourceMessageIds: string[] = [];
  let toolList: Array<IMessageToolGroup | IMessageAcpToolCall | IMessageToolCall> = [];
  let toolSourceMessageIds: string[] = [];
  const groupBoundaries = new Set(groupBoundaryMessageIds);

  const resetSummaryGroups = () => {
    toolList = [];
    toolSourceMessageIds = [];
    diffsChanges = [];
    diffsSourceMessageIds = [];
  };
  const pushFileDiffChanges = (changes: FileChangeInfo, sourceMessageId: string, createdAt: number) => {
    if (!diffsChanges.length) {
      diffsSourceMessageIds = [];
      result.push({
        type: 'file_summary',
        id: `summary-${sourceMessageId}`,
        diffs: diffsChanges,
        sourceMessageIds: diffsSourceMessageIds,
        created_at: createdAt,
      });
    }
    diffsChanges.push(changes);
    diffsSourceMessageIds.push(sourceMessageId);
    toolList = [];
    toolSourceMessageIds = [];
  };
  const pushToolList = (message: IMessageToolGroup | IMessageAcpToolCall | IMessageToolCall) => {
    if (!toolList.length) {
      toolSourceMessageIds = [];
      result.push({
        type: 'tool_summary',
        id: `tool-summary-${message.id}`,
        messages: toolList,
        sourceMessageIds: toolSourceMessageIds,
        created_at: message.created_at ?? 0,
      });
    }
    toolList.push(message);
    toolSourceMessageIds.push(message.id);
    diffsChanges = [];
    diffsSourceMessageIds = [];
  };

  for (const message of presentationList) {
    if (groupBoundaries.has(message.id)) resetSummaryGroups();
    if (message.hidden || message.type === 'available_commands') continue;
    if (message.type === 'text' && !hasRenderableMessageText(message)) {
      // An empty assistant segment is still a durable turn boundary. Keeping
      // it out of the UI must not fuse tool phases from opposite sides.
      resetSummaryGroups();
      continue;
    }
    if (
      message.type === 'tool_call' &&
      message.content.name === 'ask_user' &&
      isPendingSynonBiomedAskUserHistory(message.content.input ?? message.content.args, message.content.output)
    ) {
      continue;
    }
    if (message.type === 'tool_group') {
      if (message.content.length === 1) {
        const writeFileResults = message.content
          .filter(
            (item) =>
              item.name === 'WriteFile' &&
              item.result_display &&
              typeof item.result_display === 'object' &&
              'file_diff' in item.result_display
          )
          .map((item) => item.result_display as WriteFileResult);
        if (writeFileResults.length && writeFileResults[0].file_diff) {
          pushFileDiffChanges(
            parseDiff(writeFileResults[0].file_diff, writeFileResults[0].file_name),
            message.id,
            message.created_at ?? 0
          );
          continue;
        }
      }
      if (startsNewToolSummary(toolList, message)) resetSummaryGroups();
      pushToolList(message);
      continue;
    }
    if (message.type === 'acp_tool_call') {
      if (startsNewToolSummary(toolList, message)) resetSummaryGroups();
      pushToolList(message);
      continue;
    }
    if (message.type === 'tool_call') {
      if (!isPublicToolActivity(message.content.name)) continue;
      if (isStandaloneToolCall(message)) {
        resetSummaryGroups();
        result.push(message);
        continue;
      }
      if (startsNewToolSummary(toolList, message)) resetSummaryGroups();
      pushToolList(message);
      continue;
    }
    resetSummaryGroups();
    result.push(message);
  }

  const allScientificFiles = artifacts
    .filter(
      (artifact): artifact is IScientificFilesArtifact =>
        artifact.kind === 'scientific_files' && artifact.status === 'active'
    )
    .flatMap((artifact) => artifact.payload.files);
  const scientificFilesByVersion = indexScientificFilesByArtifactReference(allScientificFiles);
  const artifactPresentationIndex = {
    byFilename: new Map(allScientificFiles.map((file) => [file.filename.toLocaleLowerCase(), file])),
    byArtifactId: new Map(allScientificFiles.map((file) => [file.artifact_id, file])),
    byVersionId: new Map(
      allScientificFiles.filter((file) => file.version_id !== null).map((file) => [file.version_id as string, file])
    ),
  };
  const producedRelations = new Set<ArtifactReferenceRelation>(['produced']);
  const attachedRelations = new Set<ArtifactReferenceRelation>(['attached']);
  const resultWithInlineFiles: MessageListProcessedItem[] = [];
  for (const item of result) {
    resultWithInlineFiles.push(item);
    if (!('type' in item) || item.type !== 'text' || !item.artifact_refs?.length) continue;
    const presentedVersionIds =
      typeof item.content.content === 'string'
        ? artifactVersionIdsInPresentedContent(
            item.content.content,
            item.artifact_refs,
            artifactPresentationIndex,
            item.status === 'finish' || item.status === 'error' || Boolean(item.terminal_status)
          )
        : new Set<string>();
    const files = matchScientificFilesToArtifactReferences(
      item.artifact_refs,
      allScientificFiles,
      item.position === 'right' ? attachedRelations : producedRelations,
      scientificFilesByVersion
    )
      .filter((file) => file.version_id !== null)
      .filter((file) => file.version_id === null || !presentedVersionIds.has(file.version_id));
    if (files.length === 0) continue;
    resultWithInlineFiles.push({
      type: 'referenced_files',
      id: `artifact-refs-${item.id}`,
      files,
      sourceMessageIds: [item.id, ...(item.msg_id ? [item.msg_id] : [])],
      created_at: item.created_at ?? 0,
    });
  }

  const visibleArtifacts = artifacts
    .filter((artifact): artifact is InlineConversationArtifact => {
      if (artifact.kind === 'cron_trigger') return artifact.status === 'active';
      if (artifact.kind === 'skill_suggest') return artifact.status === 'pending';
      return false;
    })
    .map<MessageListArtifactItem>((artifact) => ({
      type: 'artifact',
      id: artifact.id,
      artifact,
      created_at: artifact.created_at,
    }));

  return [...resultWithInlineFiles, ...visibleArtifacts].toSorted(
    (left, right) => getProcessedItemCreatedAt(left) - getProcessedItemCreatedAt(right)
  );
};
