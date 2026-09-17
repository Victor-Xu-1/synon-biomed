/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { IMessageText } from '@/common/chat/chatLib';
import { SYNON_AI_FILES_MARKER } from '@/common/config/constants';
import { useConversationContextSafe } from '@/renderer/hooks/context/ConversationContext';
import { useLayoutContext } from '@/renderer/hooks/context/LayoutContext';
import { useLocalFilePreview } from '@/renderer/pages/conversation/Preview/hooks/useLocalFilePreview';
import { iconColors } from '@/renderer/styles/colors';
import { Alert, Message, Tooltip } from '@arco-design/web-react';
import { Copy } from '@icon-park/react';
import classNames from 'classnames';
import React, { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { copyText } from '@/renderer/utils/ui/clipboard';
import CollapsibleContent from '@renderer/components/chat/CollapsibleContent';
import FilePreview from '@renderer/components/media/FilePreview';
import HorizontalFileList from '@renderer/components/media/HorizontalFileList';
import MarkdownView from '@renderer/components/Markdown';
import { stripThinkTags, hasThinkTags } from '@renderer/utils/chat/thinkTagFilter';
import { stripSkillSuggest, hasSkillSuggest } from '@renderer/utils/chat/skillSuggestParser';
import { useSynonBiomedArtifactResolver } from '../useSynonBiomedArtifactLinkPreview';
import { useConversationArtifactIndex } from '../artifacts';
import { presentArtifactReferenceContent } from './artifactReferencePresentation';
import MessageCronBadge from './MessageCronBadge';
import { hasRenderableMessageText } from './messageTextVisibility';

const CODE_STYLE = { marginTop: 4, marginBlock: 4 };

const parseFileMarker = (content: string) => {
  const markerIndex = content.indexOf(SYNON_AI_FILES_MARKER);
  if (markerIndex === -1) {
    return { text: content, files: [] as string[] };
  }
  const text = content.slice(0, markerIndex).trimEnd();
  const afterMarker = content.slice(markerIndex + SYNON_AI_FILES_MARKER.length).trim();
  const files = afterMarker
    ? afterMarker
        .split('\n')
        .map((line) => line.trim())
        .filter(Boolean)
    : [];
  return { text, files };
};

const isAbsoluteMessageFilePath = (file_path: string): boolean =>
  file_path.startsWith('/') || /^[A-Za-z]:/.test(file_path);

export const resolveMessageFilePath = (file_path: string, workspace?: string): string => {
  if (!file_path || isAbsoluteMessageFilePath(file_path) || !workspace) {
    return file_path;
  }

  const normalizedWorkspace = workspace.replace(/[\\/]+$/, '').replace(/\\/g, '/');
  const normalizedFilePath = file_path.replace(/^\.?[\\/]+/, '').replace(/\\/g, '/');
  return `${normalizedWorkspace}/${normalizedFilePath}`.replace(/\/+/g, '/');
};

const useFormatContent = (content: string) => {
  return useMemo(() => {
    try {
      const json = JSON.parse(content);
      const isJson = typeof json === 'object';
      return {
        json: isJson,
        data: isJson ? json : content,
      };
    } catch {
      return { data: content };
    }
  }, [content]);
};

const MessageText: React.FC<{
  message: IMessageText;
  messageIndex?: number;
  showCopyRow?: boolean;
}> = ({ message, showCopyRow = true }) => {
  // Filter think tags from content before rendering
  // 在渲染前过滤 think 标签
  const contentToRender = useMemo(() => {
    let content = message.content.content;
    if (typeof content === 'string') {
      if (hasThinkTags(content)) {
        content = stripThinkTags(content);
      }
      // Strip any inline [SKILL_SUGGEST] blocks (now handled via separate skill_suggest message type)
      if (hasSkillSuggest(content)) {
        content = stripSkillSuggest(content);
      }
      return content;
    }
    return content;
  }, [message.content.content]);

  const artifactIndex = useConversationArtifactIndex();
  const publicContent = useMemo(
    () =>
      presentArtifactReferenceContent(
        contentToRender,
        message.artifact_refs,
        artifactIndex,
        message.status === 'finish' || message.status === 'error' || Boolean(message.terminal_status)
      ),
    [artifactIndex, contentToRender, message.artifact_refs, message.status, message.terminal_status]
  );
  const { text, files } = parseFileMarker(publicContent);
  const { data, json } = useFormatContent(text);
  const { t } = useTranslation();
  const [showCopyAlert, setShowCopyAlert] = useState(false);
  const isUserMessage = message.position === 'right';
  const isRejectedTerminalCandidate =
    !isUserMessage &&
    message.terminal_superseded !== true &&
    (message.terminal_status === 'failed' || message.status === 'error');
  const hasTextContent = hasRenderableMessageText(message);
  const shouldRenderPlainText = isUserMessage;
  const conversationContext = useConversationContextSafe();
  const layout = useLayoutContext();
  const isMobile = layout?.isMobile ?? false;
  const handleLocalFileLink = useLocalFilePreview(conversationContext?.workspace);
  const synonBiomedArtifactResolver = useSynonBiomedArtifactResolver({
    conversationId: conversationContext?.conversation_id,
    workspace: conversationContext?.workspace,
    artifactReferences: message.artifact_refs,
  });
  const isSynonBiomedWorkspace = conversationContext?.workspace?.startsWith('synonbiomed://') === true;
  const resolvedFiles = useMemo(
    () => files.map((file_path) => resolveMessageFilePath(file_path, conversationContext?.workspace)),
    [conversationContext?.workspace, files]
  );

  // 过滤空内容，避免渲染空DOM
  if (!hasRenderableMessageText(message)) {
    return null;
  }

  const handleCopy = () => {
    const baseText = shouldRenderPlainText ? text : json ? JSON.stringify(data, null, 2) : text;
    const fileList = files.length ? `Files:\n${files.map((path) => `- ${path}`).join('\n')}\n\n` : '';
    const textToCopy = fileList + baseText;
    copyText(textToCopy)
      .then(() => {
        setShowCopyAlert(true);
        setTimeout(() => setShowCopyAlert(false), 2000);
      })
      .catch(() => {
        Message.error(t('common.copyFailed'));
      });
  };

  const copyButton = (
    <Tooltip content={t('common.copy')}>
      <button
        type='button'
        aria-label={t('common.copy')}
        className={classNames(
          'border-none bg-transparent p-4px rd-4px cursor-pointer hover:bg-3 transition-colors',
          isMobile
            ? ''
            : 'opacity-0 pointer-events-none group-hover:opacity-100 group-hover:pointer-events-auto focus:opacity-100 focus:pointer-events-auto'
        )}
        onClick={handleCopy}
        style={{ lineHeight: 0 }}
      >
        <Copy theme='outline' size='16' fill={iconColors.secondary} />
      </button>
    </Tooltip>
  );

  const cronMeta = message.content.cronMeta;

  return (
    <>
      <div
        className={classNames(
          'min-w-0 max-w-full flex flex-col group',
          isUserMessage ? 'message-text-layout--user items-end' : 'items-start'
        )}
      >
        {isRejectedTerminalCandidate && (
          <Alert
            type='error'
            title={t('conversation.synonRuntime.runtimeOperations.failureResultRejected')}
            content={t('conversation.synonRuntime.runtimeOperations.assistantResponseIncomplete')}
            showIcon
            data-testid='terminal-failure-alert'
            className='mb-12px w-full'
          />
        )}
        {cronMeta && <MessageCronBadge meta={cronMeta} />}
        {files.length > 0 && (
          <div className={classNames('mt-6px', { 'self-end': isUserMessage })}>
            {resolvedFiles.length === 1 ? (
              <div className='flex items-center'>
                <FilePreview path={resolvedFiles[0]} onRemove={() => undefined} readonly />
              </div>
            ) : (
              <HorizontalFileList>
                {resolvedFiles.map((path) => (
                  <FilePreview key={path} path={path} onRemove={() => undefined} readonly />
                ))}
              </HorizontalFileList>
            )}
          </div>
        )}
        <div
          className={classNames(
            'relative isolate min-w-0 max-w-full [&>p:first-child]:mt-0px [&>p:last-child]:mb-0px',
            {
              'bg-aou-2 p-6px md:p-8px': isUserMessage || cronMeta,
              'message-text-bubble--user': isUserMessage,
              'w-full': !(isUserMessage || cronMeta),
              'assistant-transcript-text': !isUserMessage && !cronMeta,
            }
          )}
          style={{
            ...(isUserMessage || cronMeta
              ? { borderRadius: '8px 0 8px 8px', color: 'var(--text-primary)' }
              : undefined),
          }}
        >
          {/* JSON 内容使用折叠组件 Use CollapsibleContent for JSON content */}
          {shouldRenderPlainText ? (
            <div className='whitespace-pre-wrap break-words' data-testid='message-text-content'>
              {text}
            </div>
          ) : json ? (
            <CollapsibleContent maxHeight={200} defaultCollapsed={true}>
              <div data-testid='message-text-content'>
                <MarkdownView
                  codeStyle={CODE_STYLE}
                  onLink={synonBiomedArtifactResolver.handleLink}
                  resolveLinkHref={isSynonBiomedWorkspace ? synonBiomedArtifactResolver.resolveLinkHref : undefined}
                  resolveImageSrc={isSynonBiomedWorkspace ? synonBiomedArtifactResolver.resolveImage : undefined}
                  onLocalFileLink={handleLocalFileLink}
                >{`\`\`\`json\n${JSON.stringify(data, null, 2)}\n\`\`\``}</MarkdownView>
              </div>
            </CollapsibleContent>
          ) : (
            <div data-testid='message-text-content'>
              <MarkdownView
                codeStyle={CODE_STYLE}
                onLink={synonBiomedArtifactResolver.handleLink}
                resolveLinkHref={isSynonBiomedWorkspace ? synonBiomedArtifactResolver.resolveLinkHref : undefined}
                resolveImageSrc={isSynonBiomedWorkspace ? synonBiomedArtifactResolver.resolveImage : undefined}
                onLocalFileLink={handleLocalFileLink}
              >
                {data}
              </MarkdownView>
            </div>
          )}
          <div className='pointer-events-none absolute inset-0 z-1' data-testid='transcript-highlight-host' />
        </div>
        {/* Only the final text block in a turn exposes a message-level action. */}
        {showCopyRow && hasTextContent && (
          <div
            data-testid='message-text-actions'
            className={classNames('min-h-32px flex items-center mt-4px', {
              'flex-row-reverse': isUserMessage,
            })}
          >
            {copyButton}
          </div>
        )}
      </div>
      {showCopyAlert && (
        <Alert
          type='success'
          content={t('messages.copySuccess')}
          showIcon
          className='fixed top-20px left-50% transform -translate-x-50% z-9999 w-max max-w-[80%]'
          style={{ boxShadow: '0px 2px 12px rgba(0,0,0,0.12)' }}
          closable={false}
        />
      )}
    </>
  );
};

export default MessageText;
