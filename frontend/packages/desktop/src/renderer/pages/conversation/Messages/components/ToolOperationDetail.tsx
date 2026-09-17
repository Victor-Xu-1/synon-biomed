import { Button, Message, Tooltip } from '@arco-design/web-react';
import { Download } from '@icon-park/react';
import React, { useCallback, useEffect, useLayoutEffect, useMemo, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import type { NormalizedToolCall } from '@/common/chat/normalizeToolCall';
import { isActiveToolStatus } from '@/common/chat/normalizeToolCall';
import { getAcpImageFileName } from '@/common/chat/acpToolCallOutput';
import LocalImageView from '@/renderer/components/media/LocalImageView';
import { downloadFileFromPath } from '@/renderer/utils/file/download';
import { useConversationDisclosure } from '@/renderer/services/runtime/conversationDisclosureStore';
import { getToolPublicDetailKind } from '../toolActivityPresentationRegistry';
import MessageToolPublicDetail from './MessageToolPublicDetail';
import MessageWebResearchSources from './MessageWebResearchSources';
import ToolOperationDiffs from './ToolOperationDiffs';
import ToolStatusIcon from './ToolStatusIcon';
import TranscriptActivity from './TranscriptActivity';
import type { TranscriptActivityState } from '../transcriptActivityModel';
import { toolExecutionDisposition } from './toolExecutionDisposition';
import { buildToolStepPublicPresentation } from './toolStepSummaryModel';
import { useToolOperationDetail } from './useToolOperationDetail';
import { toolPublicDetailText } from '@/renderer/services/i18n/toolPublicDetailLocale';

const ToolOperationDetail: React.FC<{
  item: NormalizedToolCall;
  nested?: boolean;
  repeatCount?: number;
  activity?: TranscriptActivityState;
}> = ({ item, nested = false, repeatCount = 1, activity = null }) => {
  const { t, i18n } = useTranslation();
  const detailsRef = useRef<HTMLDivElement>(null);
  const detailsId = useMemo(() => `tool-step-details-${item.key}`, [item.key]);
  const language = i18n?.language ?? 'en-US';
  const timelinePresentation = buildToolStepPublicPresentation(item, language);
  const { displayItem, loadingFull, loadError, loadFullItem, retryFullItem } = useToolOperationDetail(
    item,
    timelinePresentation.shouldLoadFull
  );
  const detailPresentation = buildToolStepPublicPresentation(displayItem, language);
  const hasDetail = detailPresentation.expandable;
  const isActive = isActiveToolStatus(item.status);
  const continuationLabel =
    activity?.spinning && !isActive
      ? toolPublicDetailText(language.toLowerCase().startsWith('zh'), 'continuationAfterStep')
      : activity?.spinning
        ? t(activity.labelKey)
        : undefined;
  const executionDisposition = toolExecutionDisposition(item);
  const disclosure = useConversationDisclosure({
    conversationId: item.conversationId ?? 'unknown-conversation',
    branchId: item.branchId,
    operationId: item.key,
    path: 'operation',
  });
  const expanded = hasDetail && disclosure.expanded;
  const [messageApi, messageContext] = Message.useMessage();

  useLayoutEffect(() => {
    const details = detailsRef.current as (HTMLDivElement & { inert?: boolean }) | null;
    if (details) details.inert = !expanded;
  }, [expanded]);

  useEffect(() => {
    if (expanded) void loadFullItem();
  }, [expanded, loadFullItem]);

  const handleDownloadImage = useCallback(
    async (path: string) => {
      try {
        await downloadFileFromPath(path, getAcpImageFileName(path));
        messageApi.success(t('acp.image.download_success'));
      } catch (error) {
        console.error('[ToolOperationDetail] Failed to download image:', error);
        messageApi.error(t('acp.image.download_error'));
      }
    },
    [messageApi, t]
  );

  return (
    <div
      className={`tool-step tool-step--${item.status}${nested ? ' tool-step--nested' : ''}${
        executionDisposition ? ` tool-step--${executionDisposition}` : ''
      }${expanded ? ' tool-step--expanded' : ''}`}
    >
      {messageContext}
      <button
        type='button'
        data-testid='tool-chip'
        className='tool-step-row'
        aria-description={continuationLabel}
        title={continuationLabel}
        aria-expanded={hasDetail ? expanded : undefined}
        aria-controls={hasDetail ? detailsId : undefined}
        disabled={!hasDetail}
        onClick={hasDetail ? disclosure.toggle : undefined}
      >
        <span className='tool-step-row__status' aria-hidden='true'>
          {activity?.spinning ? (
            <TranscriptActivity activity={activity} />
          ) : (
            <ToolStatusIcon status={item.status} disposition={toolExecutionDisposition(item)} />
          )}
        </span>
        <span
          className={`tool-step-row__label${expanded ? ' break-all' : ''}${
            isActive ? ' tool-step-row__label--active' : ''
          }`}
        >
          <span className='tool-step-row__primary'>{timelinePresentation.label}</span>
          {timelinePresentation.detail ? (
            <span className='tool-step-row__detail'>
              <span className='tool-step-row__separator' aria-hidden='true'>
                ·
              </span>
              <span className='tool-step-row__detail-text'>{timelinePresentation.detail}</span>
            </span>
          ) : null}
        </span>
        {timelinePresentation.resultSummary ? (
          <span className='tool-step-row__result'>
            {timelinePresentation.resultSummary}
            {repeatCount > 1 ? ` ×${repeatCount}` : ''}
          </span>
        ) : null}
      </button>
      {hasDetail ? (
        <div
          ref={detailsRef}
          id={detailsId}
          className='tool-step__details'
          aria-hidden={!expanded}
          data-expanded={expanded ? 'true' : 'false'}
        >
          <div className='tool-step__details-inner'>
            {expanded ? (
              <div className={`tool-detail-panel tool-detail-panel--${getToolPublicDetailKind(displayItem.name)}`}>
                {loadingFull ? <div className='tool-detail-label'>{t('tools.labels.loadingFullOutput')}</div> : null}
                {loadError ? (
                  <div className='tool-remote-detail__error' role='alert'>
                    <span>{t('tools.labels.loadFullOutputFailed')}</span>
                    <button type='button' onClick={retryFullItem}>
                      {toolPublicDetailText(language.toLowerCase().startsWith('zh'), 'detailRetry')}
                    </button>
                  </div>
                ) : null}
                {detailPresentation.failureSummary ? (
                  <div className='tool-detail-failure' data-testid='tool-failure-safe-detail'>
                    <div className='tool-detail-label'>{t('tools.labels.failureSummary')}</div>
                    <div className='tool-detail-failure__content'>{detailPresentation.failureSummary}</div>
                  </div>
                ) : null}
                {displayItem.fileDiffs?.length ? <ToolOperationDiffs diffs={displayItem.fileDiffs} /> : null}
                {detailPresentation.showResearchSources && displayItem.output && !displayItem.remoteDetail ? (
                  <MessageWebResearchSources
                    toolName={displayItem.name}
                    input={displayItem.input}
                    output={displayItem.output}
                  />
                ) : null}
                <MessageToolPublicDetail
                  item={displayItem}
                  language={language}
                  resultSummary={timelinePresentation.resultSummary ?? detailPresentation.resultSummary}
                />
                {displayItem.imagePath ? (
                  <div className='tool-image-inset group relative overflow-hidden'>
                    <LocalImageView
                      src={displayItem.imagePath}
                      alt={getAcpImageFileName(displayItem.imagePath)}
                      className='tool-image-inset__image max-w-full max-h-320px object-contain rounded-8px'
                    />
                    <Tooltip content={t('acp.image.download')}>
                      <Button
                        aria-label={t('acp.image.download_aria')}
                        className='!absolute right-12px top-12px !h-28px !w-28px !p-0 opacity-0 shadow-sm transition-opacity group-hover:opacity-90 focus:opacity-100'
                        type='secondary'
                        size='mini'
                        shape='circle'
                        icon={<Download theme='outline' size='14' />}
                        onClick={() => void handleDownloadImage(displayItem.imagePath!)}
                      />
                    </Tooltip>
                  </div>
                ) : null}
              </div>
            ) : null}
          </div>
        </div>
      ) : null}
    </div>
  );
};

export default React.memo(ToolOperationDetail);
