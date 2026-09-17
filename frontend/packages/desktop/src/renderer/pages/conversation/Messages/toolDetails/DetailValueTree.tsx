import { IconRight } from '@arco-design/web-react/icon';
import React, { useEffect, useId, useState } from 'react';
import type { ToolDetailValueNode } from './detailTypes';
import { useConversationDisclosure } from '@/renderer/services/runtime/conversationDisclosureStore';
import { toolPublicDetailText } from '@/renderer/services/i18n/toolPublicDetailLocale';

const DETAIL_PAGE_SIZE = 100;
const DETAIL_SCALAR_PAGE_SIZE = 96 * 1024;

const DetailScalar: React.FC<{
  node: Extract<ToolDetailValueNode, { kind: 'scalar' }>;
  chinese: boolean;
}> = ({ node, chinese }) => {
  const [visibleCharacters, setVisibleCharacters] = useState(DETAIL_SCALAR_PAGE_SIZE);
  useEffect(() => setVisibleCharacters(DETAIL_SCALAR_PAGE_SIZE), [node.path, node.value]);
  const value = safeStringPrefix(node.value, visibleCharacters);
  const hasMore = value.length < node.value.length;
  return (
    <div className='tool-detail-tree__scalar' data-detail-path={node.path}>
      <span className='tool-detail-tree__label'>{node.label}</span>
      <span className='tool-detail-tree__value'>{value}</span>
      {hasMore ? (
        <button
          type='button'
          className='tool-detail-tree__more'
          onClick={() => setVisibleCharacters((current) => current + DETAIL_SCALAR_PAGE_SIZE)}
        >
          {toolPublicDetailText(chinese, 'showMore')}
        </button>
      ) : null}
    </div>
  );
};

const DetailContainer: React.FC<{
  node: Extract<ToolDetailValueNode, { kind: 'array' | 'object' }>;
  chinese: boolean;
  initiallyExpanded?: boolean;
  pathPrefix: string;
  scope: { conversationId: string; branchId?: string; operationId: string };
}> = ({ node, chinese, initiallyExpanded = false, pathPrefix, scope }) => {
  const { expanded, toggle } = useConversationDisclosure(
    { ...scope, path: `${pathPrefix}${node.path}` },
    { defaultExpanded: initiallyExpanded }
  );
  const [visibleCount, setVisibleCount] = useState(DETAIL_PAGE_SIZE);
  useEffect(() => {
    setVisibleCount(DETAIL_PAGE_SIZE);
  }, [node.path]);

  const visibleChildren = node.children.slice(0, visibleCount);
  const hiddenCount = node.children.length - visibleChildren.length;
  const countLabel = toolPublicDetailText(chinese, node.kind === 'array' ? 'itemCount' : 'fieldCount', {
    count: node.total,
    itemWord: node.total === 1 ? 'item' : 'items',
    fieldWord: node.total === 1 ? 'field' : 'fields',
  });

  return (
    <div
      className={`tool-detail-tree__container tool-detail-tree__container--${node.kind}`}
      data-detail-path={node.path}
    >
      <button
        type='button'
        className='tool-detail-tree__trigger'
        aria-expanded={expanded}
        aria-label={`${node.label}, ${countLabel}`}
        onClick={toggle}
      >
        <IconRight
          aria-hidden='true'
          className={`tool-detail-tree__chevron${expanded ? ' tool-detail-tree__chevron--expanded' : ''}`}
        />
        <span className='tool-detail-tree__label'>{node.label}</span>
        <span className='tool-detail-tree__count'>{countLabel}</span>
      </button>
      {expanded ? (
        <div className='tool-detail-tree__children'>
          {visibleChildren.map((child) => (
            <DetailNode key={child.path} node={child} chinese={chinese} pathPrefix={pathPrefix} scope={scope} />
          ))}
          {hiddenCount > 0 ? (
            <button
              type='button'
              className='tool-detail-tree__more'
              onClick={() => setVisibleCount((current) => Math.min(current + DETAIL_PAGE_SIZE, node.children.length))}
            >
              {toolPublicDetailText(chinese, 'showMoreCount', {
                count: Math.min(DETAIL_PAGE_SIZE, hiddenCount),
              })}
            </button>
          ) : null}
        </div>
      ) : null}
    </div>
  );
};

const DetailNode: React.FC<{
  node: ToolDetailValueNode;
  chinese: boolean;
  pathPrefix: string;
  scope: { conversationId: string; branchId?: string; operationId: string };
}> = ({ node, chinese, pathPrefix, scope }) =>
  node.kind === 'scalar' ? (
    <DetailScalar node={node} chinese={chinese} />
  ) : (
    <DetailContainer node={node} chinese={chinese} pathPrefix={pathPrefix} scope={scope} />
  );

const DetailValueTree: React.FC<{
  node: ToolDetailValueNode;
  chinese: boolean;
  testId?: string;
  initiallyExpanded?: boolean;
  disclosurePathPrefix?: string;
  disclosureScope?: {
    conversationId: string;
    branchId?: string;
    operationId: string;
  };
}> = ({ node, chinese, testId, initiallyExpanded = false, disclosurePathPrefix = 'tree', disclosureScope }) => {
  const localId = useId();
  const scope = disclosureScope ?? {
    conversationId: 'local-detail-tree',
    operationId: localId,
  };
  return (
    <div className='tool-detail-tree' data-testid={testId}>
      {node.kind === 'scalar' ? (
        <DetailScalar node={node} chinese={chinese} />
      ) : (
        <DetailContainer
          node={node}
          chinese={chinese}
          initiallyExpanded={initiallyExpanded}
          pathPrefix={disclosurePathPrefix}
          scope={scope}
        />
      )}
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

export default React.memo(DetailValueTree);
