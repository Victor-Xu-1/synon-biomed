import { IconRight } from '@arco-design/web-react/icon';
import React, { useEffect, useMemo, useState } from 'react';
import type { NormalizedToolCall } from '@/common/chat/normalizeToolCall';
import MarkdownView from '@/renderer/components/Markdown';
import SynonBiomedPlanTree from '@/renderer/components/synonBiomed/runtime/SynonBiomedPlanTree';
import { useConversationDisclosure } from '@/renderer/services/runtime/conversationDisclosureStore';
import {
  buildToolPublicDetailPresentation,
  type ToolPublicDetailCollection,
  type ToolPublicDetailRow,
} from '../toolDetails/detailProjection';
import DetailValueTree from '../toolDetails/DetailValueTree';
import RemoteToolDetailTree from '../toolDetails/RemoteToolDetailTree';
import type { ToolPublicDetailBlock } from './toolPublicDetailBlocks';
import { toolPublicDetailText } from '@/renderer/services/i18n/toolPublicDetailLocale';

type DisclosureScope = {
  conversationId: string;
  branchId?: string;
  operationId: string;
};

const PublicDetailRows: React.FC<{
  rows: ToolPublicDetailRow[];
  testId?: string;
}> = ({ rows, testId }) => {
  if (rows.length === 0) return null;
  return (
    <div className='tool-public-detail__rows' data-testid={testId}>
      {rows.map((row) => (
        <div className='tool-public-detail__row' key={`${row.label}:${row.value}`}>
          <span className='tool-public-detail__label'>{row.label}</span>
          <span className='tool-public-detail__value'>{row.value}</span>
        </div>
      ))}
    </div>
  );
};

const PublicDetailCollection: React.FC<{
  collection: ToolPublicDetailCollection;
  chinese: boolean;
  initiallyExpanded: boolean;
  path: string;
  scope: DisclosureScope;
}> = ({ collection, chinese, initiallyExpanded, path, scope }) => {
  const { expanded, toggle } = useConversationDisclosure({ ...scope, path }, { defaultExpanded: initiallyExpanded });
  const [visibleCount, setVisibleCount] = useState(100);
  useEffect(() => {
    setVisibleCount(100);
  }, [collection.label, collection.total]);
  const visibleItems = collection.items.slice(0, visibleCount);
  const hiddenCount = Math.max(0, collection.items.length - visibleItems.length);
  const countLabel = toolPublicDetailText(chinese, 'itemCount', {
    count: collection.total,
    itemWord: collection.total === 1 ? 'item' : 'items',
  });
  return (
    <div className='tool-public-detail__collection'>
      <button
        type='button'
        className='tool-public-detail__collection-trigger'
        aria-expanded={expanded}
        aria-label={`${collection.label}, ${countLabel}`}
        onClick={toggle}
      >
        <IconRight
          aria-hidden='true'
          className={`tool-public-detail__collection-chevron${
            expanded ? ' tool-public-detail__collection-chevron--expanded' : ''
          }`}
        />
        <span>{collection.label}</span>
        <span className='tool-public-detail__collection-count'>{countLabel}</span>
      </button>
      <div
        className='tool-public-detail__collection-body'
        data-expanded={expanded ? 'true' : 'false'}
        aria-hidden={!expanded}
      >
        {expanded ? (
          <div className='tool-public-detail__collection-body-inner'>
            <div className='tool-public-detail__collection-list' role='list'>
              <code>
                <span aria-hidden='true'>[ </span>
                {visibleItems.map((item, index) => (
                  <React.Fragment key={`${index}:${item}`}>
                    <span className='tool-public-detail__collection-item' role='listitem'>
                      {item}
                    </span>
                    {index < visibleItems.length - 1 ? <span aria-hidden='true'>, </span> : null}
                  </React.Fragment>
                ))}
                {hiddenCount > 0 ? <span aria-hidden='true'>, …</span> : null}
                <span aria-hidden='true'> ]</span>
              </code>
            </div>
            {hiddenCount > 0 ? (
              <button
                type='button'
                className='tool-public-detail__collection-more'
                onClick={() => setVisibleCount((current) => Math.min(current + 100, collection.items.length))}
              >
                {toolPublicDetailText(chinese, 'hiddenItemCount', {
                  count: hiddenCount,
                })}
              </button>
            ) : null}
          </div>
        ) : null}
      </div>
    </div>
  );
};

const PublicDetailBlocks: React.FC<{
  blocks: ToolPublicDetailBlock[];
  testId: string;
  disclosure?: boolean;
  path: string;
  scope: DisclosureScope;
  chinese: boolean;
}> = ({ blocks, testId, disclosure = false, path, scope, chinese }) => {
  if (blocks.length === 0) return null;
  return (
    <div className='tool-public-detail__blocks' data-testid={testId}>
      {blocks.map((block, index) => (
        <PublicDetailBlock
          block={block}
          disclosure={disclosure}
          key={`${block.label}:${index}`}
          path={`${path}/${index}`}
          scope={scope}
          chinese={chinese}
        />
      ))}
    </div>
  );
};

const PublicDetailBlock: React.FC<{
  block: ToolPublicDetailBlock;
  disclosure: boolean;
  path: string;
  scope: DisclosureScope;
  chinese: boolean;
}> = ({ block, disclosure, path, scope, chinese }) => {
  const { expanded, toggle } = useConversationDisclosure({ ...scope, path }, { defaultExpanded: !disclosure });
  const [visibleCharacters, setVisibleCharacters] = useState(96 * 1024);
  useEffect(() => setVisibleCharacters(96 * 1024), [block.content]);
  const visibleContent = safeStringPrefix(block.content, visibleCharacters);
  const hasMoreContent = visibleContent.length < block.content.length;
  const content =
    block.variant === 'markdown' ? (
      <div className='tool-public-detail__block-markdown' data-language={block.language}>
        <MarkdownView hiddenCodeCopyButton>{visibleContent}</MarkdownView>
      </div>
    ) : (
      <pre className='tool-public-detail__block-content' data-language={block.language}>
        <code>{visibleContent}</code>
      </pre>
    );

  return (
    <div className={`tool-public-detail__block tool-public-detail__block--${block.variant}`}>
      {disclosure ? (
        <button type='button' className='tool-public-detail__block-trigger' aria-expanded={expanded} onClick={toggle}>
          <IconRight
            aria-hidden='true'
            className={`tool-public-detail__block-chevron${
              expanded ? ' tool-public-detail__block-chevron--expanded' : ''
            }`}
          />
          <span>{block.label}</span>
          <span className='tool-public-detail__block-summary'>{block.summary}</span>
        </button>
      ) : (
        <div className='tool-public-detail__block-header'>
          <span>{block.label}</span>
          {block.content.includes('\n') ? (
            <span className='tool-public-detail__block-summary'>{block.summary}</span>
          ) : null}
        </div>
      )}
      <div
        className='tool-public-detail__block-body'
        data-expanded={expanded ? 'true' : 'false'}
        aria-hidden={!expanded}
      >
        {expanded ? (
          <div className='tool-public-detail__block-body-inner'>
            {content}
            {hasMoreContent ? (
              <button
                type='button'
                className='tool-detail-tree__more'
                onClick={() => setVisibleCharacters((current) => current + 96 * 1024)}
              >
                {toolPublicDetailText(chinese, 'showMore')}
              </button>
            ) : null}
          </div>
        ) : null}
      </div>
    </div>
  );
};

function safeStringPrefix(value: string, maximum: number): string {
  if (value.length <= maximum) return value;
  let end = maximum;
  const previous = value.charCodeAt(end - 1);
  const next = value.charCodeAt(end);
  if (previous >= 0xd800 && previous <= 0xdbff && next >= 0xdc00 && next <= 0xdfff) end -= 1;
  return value.slice(0, end);
}

const MessageToolPublicDetail: React.FC<{
  item: NormalizedToolCall;
  language: string;
  resultSummary: string | null;
}> = ({ item, language, resultSummary }) => {
  const chinese = language.toLowerCase().startsWith('zh');
  const scope = {
    conversationId: item.conversationId ?? 'unknown-conversation',
    branchId: item.branchId,
    operationId: item.key,
  };
  const resultDisclosure = useConversationDisclosure({ ...scope, path: 'result' });
  const presentation = useMemo(
    () => buildToolPublicDetailPresentation(item, language, resultSummary),
    [item, language, resultSummary]
  );
  const hasOutputDetail =
    presentation.showGenericOutput &&
    (presentation.resultRows.length > 0 ||
      presentation.resultCollections.length > 0 ||
      presentation.outputBlocks.length > 0 ||
      presentation.narrative.length > 0 ||
      presentation.outputTree !== null ||
      item.remoteDetail !== undefined);
  const showStaticResult = presentation.showGenericOutput;
  const hasResult = hasOutputDetail || (showStaticResult && Boolean(presentation.resultSummary));
  const hasInput =
    Boolean(presentation.planSummary) ||
    presentation.inputRows.length > 0 ||
    presentation.inputCollections.length > 0 ||
    presentation.inputBlocks.length > 0 ||
    presentation.planDocument !== null ||
    presentation.inputTree !== null;

  return (
    <div
      className={`tool-public-detail tool-public-detail--${item.status} tool-public-detail--${presentation.detailKind}`}
      data-testid='tool-public-detail'
    >
      {presentation.showIdentity ? (
        <div className='tool-public-detail__identity'>
          <span className='tool-public-detail__identity-value'>{presentation.toolLabel}</span>
          {presentation.toolContext ? (
            <span className='tool-public-detail__identity-context'>{presentation.toolContext}</span>
          ) : null}
        </div>
      ) : null}
      {item.streamingRecoveryError ? (
        <div className='tool-detail-label' role='status'>
          {toolPublicDetailText(chinese, 'liveOutputUnavailable')}
        </div>
      ) : null}
      {presentation.notices.map((notice) => (
        <p className='tool-public-detail__notice' role='note' key={notice}>
          {notice}
        </p>
      ))}
      {hasInput ? (
        <section className='tool-public-detail__section' aria-label={toolPublicDetailText(chinese, 'input')}>
          <div className='tool-public-detail__surface tool-public-detail__surface--input'>
            {presentation.planSummary ? (
              <p className='tool-public-detail__plan-summary' data-testid='tool-public-plan-summary'>
                {presentation.planSummary}
              </p>
            ) : null}
            <PublicDetailRows rows={presentation.inputRows} testId='tool-public-inputs' />
            {presentation.inputCollections.map((collection, index) => (
              <PublicDetailCollection
                key={`${collection.label}:${collection.total}`}
                collection={collection}
                chinese={chinese}
                initiallyExpanded={presentation.collectionsInitiallyExpanded}
                path={`input/collection/${index}`}
                scope={scope}
              />
            ))}
            <PublicDetailBlocks
              blocks={presentation.inputBlocks}
              testId='tool-public-input-blocks'
              disclosure={presentation.inputBlockMode === 'disclosure'}
              path='input/block'
              scope={scope}
              chinese={chinese}
            />
            {presentation.inputTree ? (
              <DetailValueTree
                node={presentation.inputTree}
                chinese={chinese}
                disclosurePathPrefix='input/tree'
                disclosureScope={scope}
                testId='tool-public-input-tree'
              />
            ) : null}
            {presentation.planDocument ? (
              <div className='tool-public-detail__plan' data-testid='tool-public-plan'>
                <SynonBiomedPlanTree document={presentation.planDocument} />
              </div>
            ) : null}
            {presentation.planAssessment ? (
              <div className='tool-public-detail__plan-assessment' data-testid='tool-public-plan-assessment'>
                <span className='tool-public-detail__confidence'>{presentation.planAssessment.confidence}</span>
                {presentation.planAssessment.rationale ? (
                  <span className='tool-public-detail__plan-rationale'>{presentation.planAssessment.rationale}</span>
                ) : null}
              </div>
            ) : null}
          </div>
        </section>
      ) : null}
      {hasResult ? (
        <div className='tool-public-detail__result'>
          {hasOutputDetail ? (
            <>
              <button
                type='button'
                data-testid='show-output-toggle'
                className='tool-public-detail__result-trigger'
                aria-expanded={resultDisclosure.expanded}
                onClick={resultDisclosure.toggle}
              >
                <IconRight
                  aria-hidden='true'
                  className={`tool-public-detail__result-chevron${
                    resultDisclosure.expanded ? ' tool-public-detail__result-chevron--expanded' : ''
                  }`}
                />
                <span>
                  {resultDisclosure.expanded
                    ? toolPublicDetailText(chinese, 'collapseOutput')
                    : toolPublicDetailText(chinese, 'showOutput')}
                </span>
                {presentation.resultSummary ? (
                  <span className='tool-public-detail__result-summary'>{presentation.resultSummary}</span>
                ) : null}
                <span className='sr-only'>
                  {resultDisclosure.expanded
                    ? toolPublicDetailText(chinese, 'collapse')
                    : toolPublicDetailText(chinese, 'expand')}
                </span>
              </button>
              <div
                className='tool-public-detail__result-body'
                data-expanded={resultDisclosure.expanded ? 'true' : 'false'}
                aria-hidden={!resultDisclosure.expanded}
              >
                {resultDisclosure.expanded ? (
                  <div className='tool-public-detail__result-body-inner'>
                    <div
                      className='tool-public-detail__surface tool-public-detail__surface--output'
                      data-testid='tool-public-output'
                    >
                      <PublicDetailRows rows={presentation.resultRows} testId='tool-public-results' />
                      {presentation.resultCollections.map((collection, index) => (
                        <PublicDetailCollection
                          key={`${collection.label}:${collection.total}`}
                          collection={collection}
                          chinese={chinese}
                          initiallyExpanded={presentation.collectionsInitiallyExpanded}
                          path={`output/collection/${index}`}
                          scope={scope}
                        />
                      ))}
                      <PublicDetailBlocks
                        blocks={presentation.outputBlocks}
                        testId='tool-public-output-blocks'
                        path='output/block'
                        scope={scope}
                        chinese={chinese}
                      />
                      {presentation.outputTree ? (
                        <DetailValueTree
                          node={presentation.outputTree}
                          chinese={chinese}
                          disclosurePathPrefix='output/tree'
                          disclosureScope={scope}
                          testId='tool-public-output-tree'
                        />
                      ) : null}
                      {item.remoteDetail ? (
                        <RemoteToolDetailTree
                          reference={item.remoteDetail}
                          operationId={item.key}
                          chinese={chinese}
                          detailKind={presentation.detailKind}
                        />
                      ) : null}
                      {presentation.narrative.length > 0 ? (
                        <div className='tool-public-detail__narrative-list'>
                          {presentation.narrative.map((line) => (
                            <p key={line} className='tool-public-detail__narrative'>
                              {line}
                            </p>
                          ))}
                        </div>
                      ) : null}
                    </div>
                  </div>
                ) : null}
              </div>
            </>
          ) : (
            <div className='tool-public-detail__result-trigger tool-public-detail__result-trigger--static'>
              <span className='tool-public-detail__result-spacer' aria-hidden='true' />
              <span>{toolPublicDetailText(chinese, 'output')}</span>
              <span className='tool-public-detail__result-summary'>{presentation.resultSummary}</span>
            </div>
          )}
        </div>
      ) : null}
    </div>
  );
};

export default React.memo(MessageToolPublicDetail);
