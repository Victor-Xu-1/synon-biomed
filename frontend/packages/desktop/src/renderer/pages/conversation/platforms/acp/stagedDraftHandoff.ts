/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Message } from '@arco-design/web-react';
import type { ArtifactReferenceWire } from '@/common/adapter/messageStreamProtocol';
import {
  normalizeComposerContextItems,
  type ComposerContextItem,
} from '@/renderer/components/chat/SendBox/composerCompositionModel';
import type { StagedSessionOptions } from '@/renderer/hooks/chat/sendBoxDraftPersistence';
import type { TFunction } from 'i18next';

export type StagedDraftPayload = {
  input: string;
  files: string[];
  artifactRefs: ArtifactReferenceWire[];
  injectSkills: string[];
  injectMcpServerIds: string[];
  sessionOptions: {
    delegation: boolean;
    autoReview: boolean;
    memory: boolean;
    targetAgent: string | null;
  } | null;
  planMode: boolean;
};

export type StagedDraftApply = {
  setContent: (content: string) => void;
  setUploadFile: (files: string[]) => void;
  setContextItems: (update: (current: ComposerContextItem[]) => ComposerContextItem[]) => void;
  setStagedSessionOptions: (options: StagedSessionOptions) => void;
  setStagedPlanMode: (enabled: boolean) => void;
};

export type StagedDraftAuthority = {
  /** Persisted staged options mirrored into a synchronous ref for the send path. */
  onStagedSessionOptions: (options: StagedSessionOptions) => void;
  /** Apply the staged options onto the visible session state immediately. */
  applyStagedSessionOptions: () => void;
};

/**
 * Hand a staged first request to the composer: text, files, capability chips,
 * and the requesting page's session selections. A fresh conversation has no
 * frames yet, so the staged selection stays authoritative until the first
 * explicit send bakes it into the frame. It lives in the durable draft so the
 * async runtime-options load can never overwrite it, and so a reload keeps
 * the requesting page's choices.
 */
export function applyStagedDraft(
  payload: StagedDraftPayload,
  apply: StagedDraftApply,
  authority: StagedDraftAuthority
): void {
  if (payload.input.trim()) apply.setContent(payload.input);
  if (payload.files.length > 0) apply.setUploadFile(payload.files);
  const stagedItems: ComposerContextItem[] = [
    ...payload.artifactRefs.map(
      (reference): ComposerContextItem => ({
        kind: 'artifact',
        artifactId: reference.artifact_id,
        versionId: reference.version_id,
        label: reference.filename || reference.artifact_id,
        ...(reference.content_type ? { contentType: reference.content_type } : {}),
        ...(reference.size_bytes === undefined ? {} : { sizeBytes: reference.size_bytes }),
      })
    ),
    ...payload.injectSkills.map((name): ComposerContextItem => ({ kind: 'skill', name, label: name })),
    ...payload.injectMcpServerIds.map((serverId): ComposerContextItem => ({ kind: 'mcp', serverId, label: serverId })),
  ];
  if (stagedItems.length > 0) {
    apply.setContextItems((current: ComposerContextItem[]) =>
      normalizeComposerContextItems([...current, ...stagedItems])
    );
  }
  if (payload.sessionOptions) {
    const staged: StagedSessionOptions = {
      delegation: payload.sessionOptions.delegation,
      autoReview: payload.sessionOptions.autoReview,
      memory: payload.sessionOptions.memory,
      targetAgent: payload.sessionOptions.targetAgent?.trim() ?? '',
    };
    apply.setStagedSessionOptions(staged);
    authority.onStagedSessionOptions(staged);
    authority.applyStagedSessionOptions();
  }
  if (payload.planMode) apply.setStagedPlanMode(true);
}

export function makeDraftRestoreIssueNotifier(t: TFunction) {
  return (issue: { providers: string[] }) => {
    console.error('[AcpSendBox] staged draft compute restore failed:', issue.providers);
    Message.error(t('conversation.sendbox.draftComputeRestoreFailed'));
  };
}
