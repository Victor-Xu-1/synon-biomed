import { IconRight } from '@arco-design/web-react/icon';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useConversationDisclosure } from '@/renderer/services/runtime/conversationDisclosureStore';
import { toolPublicDetailText } from '@/renderer/services/i18n/toolPublicDetailLocale';
import type { ToolPublicDetailKind } from '../toolActivityPresentationRegistry';
import { publicReceiptScalar } from './retrievalReceiptPresentation';
import { normalizePublicDetailKey } from './detailFieldPolicy';
import {
  formatPublicTreeScalar,
  isPublicDetailKey,
  publicDetailKeyLabel,
  type DetailValueProjectionOptions,
} from './detailValueProjection';
import {
  loadRemoteToolDetailPage,
  type RemoteToolDetailNode,
  type RemoteToolDetailPage,
  type RemoteToolDetailReference,
} from './toolDetailApi';

type RemoteDetailScope = {
  conversationId: string;
  branchId?: string;
  operationId: string;
};

const RemoteToolDetailPageView: React.FC<{
  reference: RemoteToolDetailReference;
  path: string;
  scope: RemoteDetailScope;
  chinese: boolean;
  detailKind: ToolPublicDetailKind;
}> = ({ reference, path, scope, chinese, detailKind }) => {
  const [pages, setPages] = useState<RemoteToolDetailPage[]>([]);
  const [loading, setLoading] = useState(false);
  const loadingRef = useRef(false);
  const [error, setError] = useState(false);
  const requestRef = useRef<AbortController | null>(null);

  useEffect(() => {
    requestRef.current?.abort();
    requestRef.current = null;
    loadingRef.current = false;
    setPages([]);
    setLoading(false);
    setError(false);
  }, [path, reference.branchId, reference.conversationId, reference.messageId, reference.revision]);

  const load = useCallback(
    async (cursor?: string) => {
      if (loadingRef.current) return;
      requestRef.current?.abort();
      const controller = new AbortController();
      requestRef.current = controller;
      loadingRef.current = true;
      setLoading(true);
      setError(false);
      try {
        const page = await loadRemoteToolDetailPage(reference, {
          path,
          cursor,
          signal: controller.signal,
        });
        if (controller.signal.aborted) return;
        setPages((current) => (cursor ? [...current, page] : [page]));
      } catch {
        if (!controller.signal.aborted) setError(true);
      } finally {
        if (!controller.signal.aborted) setLoading(false);
        if (requestRef.current === controller) loadingRef.current = false;
      }
    },
    [path, reference]
  );

  useEffect(() => {
    if (pages.length === 0 && !loading && !error) void load();
  }, [error, load, loading, pages.length]);

  useEffect(() => () => requestRef.current?.abort(), []);

  const items = pages.flatMap((page) => page.items);
  const nextCursor = pages.at(-1)?.nextCursor ?? null;
  const projectionOptions = useMemo<DetailValueProjectionOptions>(
    () => ({
      chinese,
      detailKind,
      label: '',
      source: 'output',
      includeBlockValues: true,
    }),
    [chinese, detailKind]
  );

  if (error && items.length === 0) {
    return (
      <div className='tool-remote-detail__error' role='alert'>
        <span>{toolPublicDetailText(chinese, 'detailReadFailed')}</span>
        <button type='button' onClick={() => void load()}>
          {toolPublicDetailText(chinese, 'detailRetry')}
        </button>
      </div>
    );
  }
  if (items.length === 0 && loading) {
    return <div className='tool-detail-label'>{toolPublicDetailText(chinese, 'loadingDetails')}</div>;
  }
  return (
    <div className='tool-remote-detail__page' data-detail-path={path}>
      {items.map((node) => (
        <RemoteToolDetailNodeView
          key={node.path}
          node={node}
          reference={reference}
          scope={scope}
          chinese={chinese}
          detailKind={detailKind}
          projectionOptions={projectionOptions}
        />
      ))}
      {error ? (
        <div className='tool-remote-detail__error' role='alert'>
          <span>{toolPublicDetailText(chinese, 'laterDetailsUnavailable')}</span>
          <button type='button' onClick={() => void load(nextCursor ?? undefined)}>
            {toolPublicDetailText(chinese, 'detailRetry')}
          </button>
        </div>
      ) : nextCursor ? (
        <button
          type='button'
          className='tool-detail-tree__more'
          disabled={loading}
          onClick={() => void load(nextCursor)}
        >
          {toolPublicDetailText(chinese, loading ? 'detailLoading' : 'showMore')}
        </button>
      ) : null}
    </div>
  );
};

const RemoteToolDetailNodeView: React.FC<{
  node: RemoteToolDetailNode;
  reference: RemoteToolDetailReference;
  scope: RemoteDetailScope;
  chinese: boolean;
  detailKind: ToolPublicDetailKind;
  projectionOptions: DetailValueProjectionOptions;
}> = ({ node, reference, scope, chinese, detailKind, projectionOptions }) => {
  const rawLabel =
    node.key ??
    (node.index === undefined
      ? (node.path.split('/').at(-1) ?? '')
      : toolPublicDetailText(chinese, 'entryIndex', { index: node.index + 1 }));
  if (node.key && !isPublicDetailKey(node.key, node.value, projectionOptions)) return null;
  const label = node.key ? publicDetailKeyLabel(node.key, chinese) : rawLabel;
  if (node.kind === 'scalar') {
    const value = formatPublicTreeScalar(
      publicReceiptScalar(normalizePublicDetailKey(node.key ?? ''), node.value),
      chinese
    );
    return value ? (
      <div className='tool-detail-tree__scalar' data-detail-path={node.path}>
        <span className='tool-detail-tree__label'>{label}</span>
        <span className='tool-detail-tree__value'>{value}</span>
        {node.truncated ? (
          <span className='tool-detail-tree__count'>{toolPublicDetailText(chinese, 'truncatedField')}</span>
        ) : null}
      </div>
    ) : null;
  }
  return (
    <RemoteToolDetailContainer
      node={node}
      reference={reference}
      scope={scope}
      chinese={chinese}
      detailKind={detailKind}
      label={label}
    />
  );
};

const RemoteToolDetailContainer: React.FC<{
  node: RemoteToolDetailNode;
  reference: RemoteToolDetailReference;
  scope: RemoteDetailScope;
  chinese: boolean;
  detailKind: ToolPublicDetailKind;
  label: string;
}> = ({ node, reference, scope, chinese, detailKind, label }) => {
  const { expanded, toggle } = useConversationDisclosure({
    ...scope,
    path: `remote${node.path}`,
  });
  const count = node.total ?? 0;
  const countLabel = toolPublicDetailText(chinese, node.kind === 'array' ? 'itemCount' : 'fieldCount', {
    count,
    itemWord: count === 1 ? 'item' : 'items',
    fieldWord: count === 1 ? 'field' : 'fields',
  });
  return (
    <div className='tool-detail-tree__container' data-detail-path={node.path}>
      <button
        type='button'
        className='tool-detail-tree__trigger'
        aria-expanded={expanded}
        aria-label={`${label}, ${countLabel}`}
        onClick={toggle}
      >
        <IconRight
          aria-hidden='true'
          className={`tool-detail-tree__chevron${expanded ? ' tool-detail-tree__chevron--expanded' : ''}`}
        />
        <span className='tool-detail-tree__label'>{label}</span>
        <span className='tool-detail-tree__count'>{countLabel}</span>
      </button>
      {expanded ? (
        <div className='tool-detail-tree__children'>
          <RemoteToolDetailPageView
            reference={reference}
            path={node.path}
            scope={scope}
            chinese={chinese}
            detailKind={detailKind}
          />
        </div>
      ) : null}
    </div>
  );
};

const RemoteToolDetailTree: React.FC<{
  reference: RemoteToolDetailReference;
  operationId: string;
  chinese: boolean;
  detailKind: ToolPublicDetailKind;
}> = ({ reference, operationId, chinese, detailKind }) => {
  const scope = {
    conversationId: reference.conversationId,
    branchId: reference.branchId,
    operationId,
  };
  const { expanded, toggle } = useConversationDisclosure({
    ...scope,
    path: 'remote-root',
  });
  return (
    <div className='tool-remote-detail' data-testid='tool-remote-detail'>
      <button type='button' className='tool-detail-tree__trigger' aria-expanded={expanded} onClick={toggle}>
        <IconRight
          aria-hidden='true'
          className={`tool-detail-tree__chevron${expanded ? ' tool-detail-tree__chevron--expanded' : ''}`}
        />
        <span className='tool-detail-tree__label'>{toolPublicDetailText(chinese, 'fullDetails')}</span>
      </button>
      {expanded ? (
        <div className='tool-detail-tree__children'>
          <RemoteToolDetailPageView
            reference={reference}
            path=''
            scope={scope}
            chinese={chinese}
            detailKind={detailKind}
          />
          {reference.contentUrl ? (
            <a className='tool-detail-tree__more' href={reference.contentUrl} download>
              {toolPublicDetailText(chinese, 'downloadFullJson')}
            </a>
          ) : null}
        </div>
      ) : null}
    </div>
  );
};

export default React.memo(RemoteToolDetailTree);
