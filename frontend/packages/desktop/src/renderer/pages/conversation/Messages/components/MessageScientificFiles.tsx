/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { IScientificFilesArtifact, ISynonBiomedScientificFile } from '@/common/adapter/ipcBridge';
import {
  isSynonBiomedArtifactPreviewEditable,
  resolveSynonBiomedArtifactPreviewPlan,
  SYNON_BIOMED_TEXT_ACCEPT_HEADER,
} from '@/renderer/services/synonBiomedArtifactPreview';
import { usePreviewContext } from '@/renderer/pages/conversation/Preview/context/PreviewContext';
import { useConversationContextSafe } from '@/renderer/hooks/context/ConversationContext';
import { Message } from '@arco-design/web-react';
import React, { useCallback, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import ArtifactThumbnailPreview from '@/renderer/components/synonBiomed/files/ArtifactThumbnailPreview';
import { createSynonBiomedCompanionArtifactUrls } from '@/renderer/services/synonBiomedArtifactReferences';
import { preloadSynonBiomedStructureViewer } from '@/renderer/pages/conversation/Preview/components/viewers/scientificPreviewLoaders';
import './MessageScientificFiles.css';

type ScientificFilePreviewStripProps = {
  files: ISynonBiomedScientificFile[];
  inline?: boolean;
};

const COLLAPSED_TURN_FILE_LIMIT = 5;
const COMPACT_FILE_DISCLOSURE_CLASS =
  'message-scientific-files__more message-scientific-files__more--compact shrink-0 self-center border-0 px-8px text-11px text-t-secondary cursor-pointer hover:text-t-primary';

function artifactTrayRank(file: ISynonBiomedScientificFile): number {
  const type = resolveSynonBiomedArtifactPreviewPlan({
    filename: file.filename,
    contentType: file.content_type,
    previewKind: file.preview_kind,
  }).type;
  if (['markdown', 'word', 'latex', 'html', 'pdf', 'notebook'].includes(type)) return 0;
  if (['image', 'structure', 'molecule'].includes(type)) return 1;
  if (type === 'table') return 2;
  return 3;
}

function preloadScientificFilePreview(file: ISynonBiomedScientificFile): void {
  const plan = resolveSynonBiomedArtifactPreviewPlan({
    filename: file.filename,
    contentType: file.content_type,
    previewKind: file.preview_kind,
  });
  if (plan.type === 'structure') {
    void preloadSynonBiomedStructureViewer().catch((): undefined => undefined);
  }
}

function scientificFileVersionKey(file: ISynonBiomedScientificFile): string {
  return `${file.artifact_id}\0${file.version_id ?? ''}`;
}

function uniqueScientificFileVersions(files: ISynonBiomedScientificFile[]): ISynonBiomedScientificFile[] {
  const unique = new Map<string, ISynonBiomedScientificFile>();
  for (const file of files) {
    const key = scientificFileVersionKey(file);
    if (!unique.has(key)) unique.set(key, file);
  }
  return [...unique.values()];
}

export const ScientificFilePreviewStrip: React.FC<ScientificFilePreviewStripProps> = ({ files, inline = false }) => {
  const { t } = useTranslation();
  const { openPreview } = usePreviewContext();
  const conversationContext = useConversationContextSafe();
  const contextRootFrameId = conversationContext?.conversation_id?.trim() || undefined;
  const [loadingArtifactId, setLoadingArtifactId] = useState<string | null>(null);
  const [expanded, setExpanded] = useState(false);
  const sortedFiles = useMemo(() => {
    const uniqueFiles = uniqueScientificFileVersions(files);
    return inline
      ? uniqueFiles.toSorted((left, right) => artifactTrayRank(left) - artifactTrayRank(right))
      : uniqueFiles;
  }, [files, inline]);
  const collapsible = inline && sortedFiles.length > COLLAPSED_TURN_FILE_LIMIT;
  const visibleFiles = useMemo(
    () => (collapsible && !expanded ? sortedFiles.slice(0, COLLAPSED_TURN_FILE_LIMIT) : sortedFiles),
    [collapsible, expanded, sortedFiles]
  );
  const hiddenCount = collapsible ? sortedFiles.length - visibleFiles.length : 0;
  const companionArtifactUrls = useMemo(() => createSynonBiomedCompanionArtifactUrls(sortedFiles), [sortedFiles]);

  const handleOpen = useCallback(
    async (file: ISynonBiomedScientificFile) => {
      if (file.availability === 'deleted' || file.availability === 'missing') return;
      const plan = resolveSynonBiomedArtifactPreviewPlan({
        filename: file.filename,
        contentType: file.content_type,
        previewKind: file.preview_kind,
      });
      const metadata = {
        title: file.filename,
        file_name: file.filename,
        editable: isSynonBiomedArtifactPreviewEditable(plan),
        artifactId: file.artifact_id,
        versionId: file.version_id,
        contentUrl: file.content_url,
        workspace: conversationContext?.workspace,
        companionArtifactUrls,
        rootFrameId: file.root_frame_id?.trim() || file.frame_id?.trim() || contextRootFrameId,
        ...(plan.language ? { language: plan.language } : {}),
      };

      if (!plan.fetchText) {
        openPreview(file.content_url, plan.type, metadata, { presentation: 'board' });
        return;
      }

      try {
        setLoadingArtifactId(file.artifact_id);
        const response = await fetch(file.content_url, {
          headers: { accept: SYNON_BIOMED_TEXT_ACCEPT_HEADER },
        });
        if (!response.ok) {
          throw new Error(`Artifact request failed: ${response.status}`);
        }
        openPreview(await response.text(), plan.type, metadata, { presentation: 'board' });
      } catch (error) {
        console.warn('[MessageScientificFiles] Failed to preview artifact', {
          errorName: error instanceof Error ? error.name : typeof error,
        });
        Message.error(t('conversation.scientificFiles.loadFailed'));
      } finally {
        setLoadingArtifactId(null);
      }
    },
    [companionArtifactUrls, contextRootFrameId, conversationContext?.workspace, openPreview, t]
  );

  if (sortedFiles.length === 0) return null;

  return (
    <section
      className={`message-scientific-files w-full min-w-0 ${inline ? 'message-scientific-files--inline' : ''}`}
      data-testid={inline ? 'inline-scientific-files' : 'message-scientific-files'}
    >
      {inline ? (
        <div className='message-scientific-files__turn-label mb-6px text-10.5px font-[500] tracking-[0.02em] text-t-tertiary'>
          {t('conversation.scientificFiles.generatedLabel', { count: sortedFiles.length })}
        </div>
      ) : (
        <div className='mb-8px text-12px font-[500] text-t-tertiary'>
          {t('conversation.scientificFiles.title')} · {sortedFiles.length}
        </div>
      )}
      <div
        role='list'
        aria-label={t('conversation.scientificFiles.title')}
        className={`message-scientific-files__list ${inline ? 'flex flex-wrap' : 'grid'} gap-8px`}
        style={inline ? undefined : { gridTemplateColumns: 'repeat(auto-fill, minmax(min(100%, 128px), 1fr))' }}
      >
        {visibleFiles.map((file) => {
          const loading = loadingArtifactId === file.artifact_id;
          const unavailable = file.availability === 'deleted' || file.availability === 'missing';
          return (
            <button
              key={scientificFileVersionKey(file)}
              type='button'
              aria-label={
                unavailable
                  ? t('conversation.scientificFiles.unavailableNamed', { name: file.filename })
                  : t('conversation.scientificFiles.preview', { name: file.filename })
              }
              title={file.filename}
              disabled={loading || unavailable}
              className={`message-scientific-files__card min-w-0 overflow-hidden flex flex-col bg-white p-0 text-left cursor-pointer disabled:opacity-60 transition-all ${
                inline
                  ? 'shrink-0 border-0 shadow-[0_0_0_1px_rgba(0,0,0,0.08),0_1px_3px_rgba(0,0,0,0.06)] hover:shadow-[0_0_0_1px_rgba(0,0,0,0.12),0_2px_6px_rgba(0,0,0,0.08)]'
                  : 'border border-solid border-[var(--color-border-2)] shadow-sm hover:border-[var(--color-border-3)] hover:shadow-md'
              }`}
              style={inline ? { borderRadius: 10 } : { aspectRatio: '8 / 5', borderRadius: 12 }}
              onPointerEnter={() => preloadScientificFilePreview(file)}
              onFocus={() => preloadScientificFilePreview(file)}
              onClick={() => void handleOpen(file)}
            >
              <ArtifactThumbnailPreview
                filename={file.filename}
                contentUrl={file.content_url}
                contentType={file.content_type}
                previewKind={file.preview_kind}
                sizeBytes={file.size_bytes}
                imageFit='contain'
                className='message-scientific-files__preview min-h-0 flex-1'
              />
              <div
                className={`message-scientific-files__metadata min-w-0 shrink-0 flex flex-col justify-center px-8px ${inline ? 'h-24px border-t border-alpha-2' : 'h-30px'}`}
              >
                <div
                  className={`message-scientific-files__name w-full truncate leading-16px font-[500] text-t-primary ${inline ? 'text-10.5px' : 'text-12px'}`}
                >
                  {file.filename}
                </div>
                {unavailable ? (
                  <div className='w-full truncate text-11px leading-16px text-t-tertiary'>
                    {t(`conversation.scientificFiles.${file.availability}`)}
                  </div>
                ) : null}
              </div>
            </button>
          );
        })}
        {hiddenCount > 0 ? (
          <button
            type='button'
            className={COMPACT_FILE_DISCLOSURE_CLASS}
            aria-expanded={false}
            onClick={() => setExpanded(true)}
          >
            {t('conversation.scientificFiles.showMore', { count: hiddenCount })}
          </button>
        ) : collapsible ? (
          <button
            type='button'
            className={COMPACT_FILE_DISCLOSURE_CLASS}
            aria-expanded={true}
            onClick={() => setExpanded(false)}
          >
            {t('conversation.scientificFiles.showLess')}
          </button>
        ) : null}
      </div>
    </section>
  );
};

const MessageScientificFiles: React.FC<{ artifact: IScientificFilesArtifact }> = ({ artifact }) => (
  <ScientificFilePreviewStrip files={artifact.payload.files} />
);

export default MessageScientificFiles;
