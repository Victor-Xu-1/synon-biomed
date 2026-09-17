/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import type { ISynonBiomedScientificFile } from '@/common/adapter/ipcBridge';
import type { ArtifactReferenceWire } from '@/common/adapter/messageStreamProtocol';
import {
  createConversationArtifactIndex,
  useConversationArtifactIndex,
  useResolveConversationArtifactWindow,
  type ConversationArtifactIndex,
} from '@/renderer/pages/conversation/Messages/artifacts';
import type { PreviewMetadata } from '@/renderer/pages/conversation/Preview/context/PreviewContext';
import { usePreviewContext } from '@/renderer/pages/conversation/Preview/context/PreviewContext';
import {
  isSynonBiomedArtifactPreviewEditable,
  resolveSynonBiomedArtifactPreviewPlan,
  SYNON_BIOMED_TEXT_ACCEPT_HEADER,
} from '@/renderer/services/synonBiomedArtifactPreview';
import {
  getSynonBiomedArtifactImageFilename,
  getSynonBiomedArtifactReferenceId,
  getSynonBiomedRelativeArtifactFilename,
} from '@/renderer/services/synonBiomedArtifactReferences';
import { Message } from '@arco-design/web-react';
import { useCallback, useMemo } from 'react';
import { useTranslation } from 'react-i18next';

type UseSynonBiomedArtifactLinkPreviewOptions = {
  conversationId?: string;
  workspace?: string;
  artifactReferences?: readonly ArtifactReferenceWire[];
};

type ArtifactPreviewMetadata = PreviewMetadata & {
  artifactId: string;
  versionId: string;
  contentUrl: string;
};

export function resolveSynonBiomedArtifactRootFrameId(
  file: Pick<ISynonBiomedScientificFile, 'root_frame_id' | 'frame_id'>,
  conversationId?: string
): string | undefined {
  return file.root_frame_id?.trim() || file.frame_id?.trim() || conversationId?.trim() || undefined;
}

export {
  getSynonBiomedArtifactImageFilename,
  getSynonBiomedArtifactReferenceId,
  getSynonBiomedRelativeArtifactFilename,
} from '@/renderer/services/synonBiomedArtifactReferences';

function availableArtifactReferences(
  references: readonly ArtifactReferenceWire[] | undefined
): Array<{ artifact_id: string; version_id: string }> {
  const result: Array<{ artifact_id: string; version_id: string }> = [];
  const seen = new Set<string>();
  for (const reference of references ?? []) {
    if (reference.availability && reference.availability !== 'available') continue;
    const artifactId = reference.artifact_id.trim();
    const versionId = reference.version_id.trim();
    if (!artifactId || !versionId) continue;
    const key = `${artifactId}\0${versionId}`;
    if (seen.has(key)) continue;
    seen.add(key);
    result.push({ artifact_id: artifactId, version_id: versionId });
  }
  return result;
}

export function resolveSynonBiomedArtifactFile(
  index: ConversationArtifactIndex,
  rawReferenceId: string | null,
  rawFilename: string | null,
  references: readonly ArtifactReferenceWire[] | undefined
): ISynonBiomedScientificFile | null {
  const referenceId = rawReferenceId?.trim() ?? '';
  if (referenceId) {
    const exact = index.byVersionId.get(referenceId) ?? index.byArtifactId.get(referenceId);
    if (exact) return exact;
  }

  const filename = rawFilename?.trim().toLocaleLowerCase() ?? '';
  if (filename) {
    const exactVersions = availableArtifactReferences(references)
      .map((reference) => index.byVersionId.get(reference.version_id))
      .filter((file): file is ISynonBiomedScientificFile =>
        Boolean(file && file.filename.toLocaleLowerCase() === filename)
      );
    if (exactVersions.length === 1) return exactVersions[0];
    const byFilename = index.byFilename.get(filename);
    if (byFilename) return byFilename;
  }
  return null;
}

export function useSynonBiomedArtifactResolver({
  conversationId,
  workspace,
  artifactReferences,
}: UseSynonBiomedArtifactLinkPreviewOptions) {
  const { openPreview } = usePreviewContext();
  const { t } = useTranslation();
  const artifactIndex = useConversationArtifactIndex();
  const resolveWindowReferences = useResolveConversationArtifactWindow();
  const exactReferences = useMemo(() => availableArtifactReferences(artifactReferences), [artifactReferences]);

  const resolveFile = useCallback(
    async (referenceId: string | null, filename: string | null): Promise<ISynonBiomedScientificFile | null> => {
      const cached = resolveSynonBiomedArtifactFile(artifactIndex, referenceId, filename, artifactReferences);
      const exactReferenceIsAlreadyIncluded = exactReferences.some(
        (reference) => reference.version_id === referenceId || reference.artifact_id === referenceId
      );
      const versionIds = referenceId && !exactReferenceIsAlreadyIncluded ? [referenceId] : [];
      if (cached || !conversationId || (exactReferences.length === 0 && versionIds.length === 0)) return cached;
      const collections = await resolveWindowReferences(exactReferences, versionIds);
      const resolvedCollections =
        collections ??
        (await ipcBridge.conversation.listArtifacts.invoke({
          conversation_id: conversationId,
          references: exactReferences,
          version_ids: versionIds,
        }));
      return resolveSynonBiomedArtifactFile(
        createConversationArtifactIndex(resolvedCollections),
        referenceId,
        filename,
        artifactReferences
      );
    },
    [artifactIndex, artifactReferences, conversationId, exactReferences, resolveWindowReferences]
  );

  const resolveImage = useCallback(
    async (src: string): Promise<string | null> => {
      if (!workspace?.startsWith('synonbiomed://') || !conversationId) return null;
      const artifactReferenceId = getSynonBiomedArtifactReferenceId(src);
      if (artifactReferenceId) {
        return (await resolveFile(artifactReferenceId, null))?.content_url ?? null;
      }
      const filename = getSynonBiomedArtifactImageFilename(src);
      if (!filename) return null;
      return (await resolveFile(null, filename))?.content_url ?? null;
    },
    [conversationId, resolveFile, workspace]
  );

  const resolveLinkHref = useCallback(
    async (href: string): Promise<string | null> => {
      if (!workspace?.startsWith('synonbiomed://') || !conversationId) return null;
      const artifactReferenceId = getSynonBiomedArtifactReferenceId(href);
      const filename = getSynonBiomedRelativeArtifactFilename(href);
      if (!artifactReferenceId && !filename) return null;
      return (await resolveFile(artifactReferenceId, filename))?.content_url ?? null;
    },
    [conversationId, resolveFile, workspace]
  );

  const handleLink = useCallback(
    async (href: string): Promise<boolean> => {
      if (!workspace?.startsWith('synonbiomed://') || !conversationId) return false;
      const artifactReferenceId = getSynonBiomedArtifactReferenceId(href);
      const filename = getSynonBiomedRelativeArtifactFilename(href);
      if (!artifactReferenceId && !filename) return false;

      try {
        const file = await resolveFile(artifactReferenceId, filename);
        if (!file) {
          Message.error(t('conversation.scientificFiles.loadFailed'));
          return true;
        }

        const plan = resolveSynonBiomedArtifactPreviewPlan({
          filename: file.filename,
          contentType: file.content_type,
          previewKind: file.preview_kind,
        });
        let content = file.content_url;
        if (plan.fetchText) {
          const response = await fetch(file.content_url, {
            headers: { accept: SYNON_BIOMED_TEXT_ACCEPT_HEADER },
          });
          if (!response.ok) throw new Error(`Artifact request failed: ${response.status}`);
          content = await response.text();
        }
        const metadata: ArtifactPreviewMetadata = {
          title: file.filename,
          file_name: file.filename,
          artifactId: file.artifact_id,
          versionId: file.version_id,
          contentUrl: file.content_url,
          rootFrameId: resolveSynonBiomedArtifactRootFrameId(file, conversationId),
          workspace,
          language: plan.language,
          editable: isSynonBiomedArtifactPreviewEditable(plan),
        };
        openPreview(content, plan.type, metadata, { presentation: 'board' });
      } catch (error) {
        console.error('[useSynonBiomedArtifactLinkPreview] Failed to preview artifact link:', error);
        Message.error(t('conversation.scientificFiles.loadFailed'));
      }
      return true;
    },
    [conversationId, openPreview, resolveFile, t, workspace]
  );

  return { handleLink, resolveImage, resolveLinkHref };
}

export function useSynonBiomedArtifactLinkPreview(options: UseSynonBiomedArtifactLinkPreviewOptions) {
  return useSynonBiomedArtifactResolver(options).handleLink;
}
