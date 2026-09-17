/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { IMessageTips } from '@/common/chat/chatLib';
import { Collapse, Tag } from '@arco-design/web-react';
import { Attention, CheckOne } from '@icon-park/react';
import { theme } from '@office-ai/platform';
import classNames from 'classnames';
import React, { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import MarkdownView from '@renderer/components/Markdown';
import CollapsibleContent from '@renderer/components/chat/CollapsibleContent';

const icon = {
  success: <CheckOne theme='filled' size='16' fill={theme.Color.FunctionalColor.success} className='m-t-2px' />,
  warning: (
    <Attention
      theme='filled'
      size='16'
      strokeLinejoin='bevel'
      className='m-t-2px'
      fill={theme.Color.FunctionalColor.warn}
    />
  ),
  error: (
    <Attention
      theme='filled'
      size='16'
      strokeLinejoin='bevel'
      className='m-t-2px'
      fill={theme.Color.FunctionalColor.error}
    />
  ),
};

const useFormatContent = (content: string) => {
  return useMemo(() => {
    try {
      const json = JSON.parse(content);
      return {
        json: true,
        data: json,
      };
    } catch {
      return { data: content };
    }
  }, [content]);
};

const ownershipColor = {
  'synon-ai': 'red',
  user_agent: 'orange',
  user_llm_provider: 'arcoblue',
  unknown_upstream: 'gray',
};

const resolveAgentTipBody = (
  content: string,
  code: IMessageTips['content']['code'],
  params: IMessageTips['content']['params'],
  t: ReturnType<typeof useTranslation>['t'],
  exists: (key: string) => boolean
) => {
  if (!code) return content;
  const key = `conversation.agentTip.codes.${code}.body`;
  return exists(key) ? t(key, params) : content;
};

const MessageTips: React.FC<{ message: IMessageTips }> = ({ message }) => {
  const { t, i18n } = useTranslation();
  const { content, type, code, params } = message.content;
  const structuredError = type === 'error' ? message.content.error : undefined;
  const localizedTipBody = resolveAgentTipBody(content, code, params, t, (key) => i18n.exists(key));
  const { json, data } = useFormatContent(localizedTipBody);

  const displayContent = json ? '' : localizedTipBody;

  if (structuredError) {
    const errorCode = structuredError.code;
    const ownership = structuredError.ownership;
    const titleKey = errorCode ? `conversation.agentError.codes.${errorCode}.title` : '';
    const title = titleKey && i18n.exists(titleKey) ? t(titleKey) : t('conversation.agentError.fallbackTitle');
    const bodyKey = errorCode
      ? structuredError.workspacePath
        ? `conversation.agentError.codes.${errorCode}.bodyWithPath`
        : `conversation.agentError.codes.${errorCode}.body`
      : '';
    const body =
      bodyKey && i18n.exists(bodyKey)
        ? t(bodyKey, { workspacePath: structuredError.workspacePath })
        : structuredError.message || content;
    const ownershipKey = ownership ? `conversation.agentError.ownership.${ownership}` : '';
    const ownershipLabel = ownership
      ? t(i18n.exists(ownershipKey) ? ownershipKey : 'conversation.agentError.ownership.unknown_upstream')
      : null;
    const retryHint =
      structuredError.retryable === undefined
        ? null
        : structuredError.retryable
          ? t('conversation.agentError.retryable')
          : t('conversation.agentError.notRetryable');
    const resolutionHint = structuredError.resolution
      ? `${t('conversation.agentError.resolutionPrefix')}${t(
          `conversation.agentError.resolution.${structuredError.resolution.kind}`
        )}`
      : null;
    const detailParts = [
      errorCode ? `${t('conversation.agentError.errorCode')}: ${errorCode}` : '',
      structuredError.detail || structuredError.message,
    ].filter(Boolean);

    return (
      <div className='w-full'>
        <div className='bg-message-tips rd-8px p-x-12px p-y-10px flex flex-col gap-8px'>
          <div className='flex items-start gap-6px'>
            {icon.error}
            <div className='flex-1 min-w-0 flex flex-col gap-6px'>
              <div className='flex flex-wrap items-center gap-6px'>
                {ownershipLabel && (
                  <Tag size='small' color={ownership ? ownershipColor[ownership] : 'gray'}>
                    {ownershipLabel}
                  </Tag>
                )}
                {retryHint && (
                  <Tag size='small' color={structuredError.retryable ? 'green' : 'gray'}>
                    {retryHint}
                  </Tag>
                )}
              </div>
              <div className='font-500 text-t-primary [word-break:break-word]'>{title}</div>
              <div className='text-t-secondary whitespace-break-spaces [word-break:break-word]'>{body}</div>
              {resolutionHint && (
                <div className='text-t-secondary whitespace-break-spaces [word-break:break-word]'>{resolutionHint}</div>
              )}
              {detailParts.length > 0 && (
                <Collapse bordered={false} className='bg-transparent' defaultActiveKey={['technical-details']}>
                  <Collapse.Item
                    name='technical-details'
                    header={<span className='text-12px text-t-tertiary'>{t('common.technical_details')}</span>}
                  >
                    <div className='text-t-tertiary text-12px whitespace-break-spaces [word-break:break-word]'>
                      {detailParts.join('\n')}
                    </div>
                  </Collapse.Item>
                </Collapse>
              )}
            </div>
          </div>
        </div>
      </div>
    );
  }

  if (type === 'info') {
    return (
      <div className='w-full'>
        <div className='p-x-12px p-y-4px'>
          <div className='text-center text-13px text-t-secondary whitespace-break-spaces [word-break:break-word]'>
            {localizedTipBody}
          </div>
        </div>
      </div>
    );
  }

  if (json)
    return (
      <div className='w-full'>
        <div className={classNames('bg-message-tips rd-8px p-x-12px p-y-8px flex flex-col gap-4px')}>
          <div className='flex items-start gap-4px'>
            {icon[type] || icon.warning}
            <div className='flex-1 min-w-0'>
              <MarkdownView>{`\`\`\`json\n${JSON.stringify(data, null, 2)}\n\`\`\``}</MarkdownView>
            </div>
          </div>
        </div>
      </div>
    );
  return (
    <div className='w-full'>
      <div className={classNames('bg-message-tips rd-8px  p-x-12px p-y-8px flex flex-col gap-4px')}>
        <div className='flex items-start gap-4px'>
          {icon[type] || icon.warning}
          <div className='flex-1 min-w-0'>
            <CollapsibleContent maxHeight={48} defaultCollapsed={true} useMask={true}>
              <span className='whitespace-break-spaces text-t-primary [word-break:break-word]'>{displayContent}</span>
            </CollapsibleContent>
          </div>
        </div>
      </div>
    </div>
  );
};

export default MessageTips;
