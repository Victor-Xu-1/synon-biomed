import CodeEditor, {
  type CodeEditorSelection,
} from '@/renderer/pages/conversation/Preview/components/editors/CodeEditor';
import {
  MAX_EXPAND_ALL_JSON_NODES,
  collectJsonContainerPaths,
  collectJsonMatchPaths,
  describeJsonContainer,
  formatJsonScalar,
  isJsonContainer,
  jsonPath,
  jsonScalarType,
  parseJsonDocument,
  type JsonValue,
} from './jsonDocumentModel';
import { Button, Input, Message, Tooltip } from '@arco-design/web-react';
import { IconCode, IconCopy, IconDown, IconExpand, IconList, IconRight, IconSearch } from '@arco-design/web-react/icon';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

type JsonViewerProps = {
  filename: string;
  content: string;
  readOnly?: boolean;
  onContentChange?: (content: string) => void;
  onCodeSelection?: (selection: CodeEditorSelection | null) => void;
};

type TreeNodeProps = {
  label: string;
  value: JsonValue;
  path: string;
  depth: number;
  query: string;
  matchingPaths: ReadonlySet<string> | null;
  expandedPaths: ReadonlySet<string>;
  onToggle: (path: string) => void;
};

const SynonBiomedJsonViewer: React.FC<JsonViewerProps> = ({
  filename,
  content,
  readOnly = true,
  onContentChange,
  onCodeSelection,
}) => {
  const { i18n, t } = useTranslation();
  const document = useMemo(() => parseJsonDocument(content), [content]);
  const [mode, setMode] = useState<'tree' | 'source'>(document.status === 'ready' ? 'tree' : 'source');
  const [query, setQuery] = useState('');
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(() => new Set(['$']));

  useEffect(() => {
    setMode(document.status === 'ready' ? 'tree' : 'source');
    setQuery('');
    setExpandedPaths(new Set(['$']));
  }, [filename]);

  useEffect(() => {
    if (document.status !== 'ready' && mode === 'tree') setMode('source');
  }, [document.status, mode]);

  useEffect(() => {
    if (!readOnly) setMode('source');
  }, [readOnly]);

  const togglePath = useCallback((path: string) => {
    setExpandedPaths((current) => {
      const next = new Set(current);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  }, []);

  const expandAll = useCallback(() => {
    if (document.status !== 'ready') return;
    setExpandedPaths(collectJsonContainerPaths(document.value));
  }, [document]);

  const copyJson = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(content);
      Message.success(t('preview.scientific.json.copied'));
    } catch {
      Message.error(t('preview.scientific.json.copyFailed'));
    }
  }, [content, t]);

  const canUseTree = document.status === 'ready';
  const canExpandAll = canUseTree && document.nodeCount <= MAX_EXPAND_ALL_JSON_NODES;
  const matchingPaths = useMemo(
    () => (document.status === 'ready' ? collectJsonMatchPaths(document.value, query) : null),
    [document, query]
  );

  return (
    <div
      className='preview-json size-full min-h-360px flex flex-col overflow-hidden bg-1'
      aria-label={t('preview.scientific.contentNamed', { name: filename })}
    >
      <div className='preview-json__toolbar shrink-0 border-b border-t-0 border-x-0 border-solid border-[var(--color-border-2)] bg-1'>
        <div className='h-46px px-14px flex items-center justify-between gap-8px'>
          <div
            className='preview-json__tabs flex items-center rounded-8px bg-fill-2 p-2px'
            role='tablist'
            aria-label={t('preview.scientific.json.viewMode')}
          >
            <ViewTab
              active={mode === 'tree'}
              disabled={!canUseTree}
              icon={<IconList />}
              label={t('preview.scientific.json.tree')}
              onClick={() => setMode('tree')}
            />
            <ViewTab
              active={mode === 'source'}
              icon={<IconCode />}
              label={t('preview.scientific.source')}
              onClick={() => setMode('source')}
            />
          </div>
          <div className='flex items-center gap-2px'>
            {mode === 'tree' && canUseTree && (
              <Tooltip
                content={t(
                  canExpandAll ? 'preview.scientific.json.expandAll' : 'preview.scientific.json.expandAllUnavailable',
                  {
                    count: MAX_EXPAND_ALL_JSON_NODES,
                    formattedCount: MAX_EXPAND_ALL_JSON_NODES.toLocaleString(i18n.resolvedLanguage),
                  }
                )}
              >
                <Button
                  type='text'
                  size='small'
                  icon={<IconExpand />}
                  aria-label={t('preview.scientific.json.expandAll')}
                  disabled={!canExpandAll}
                  onClick={expandAll}
                />
              </Tooltip>
            )}
            <Tooltip content={t('preview.scientific.json.copy')}>
              <Button
                type='text'
                size='small'
                icon={<IconCopy />}
                aria-label={t('preview.scientific.json.copy')}
                onClick={() => void copyJson()}
              />
            </Tooltip>
          </div>
        </div>
        {mode === 'tree' && canUseTree && (
          <div className='preview-json__search px-14px pb-12px'>
            <Input
              size='small'
              allowClear
              prefix={<IconSearch className='text-t-tertiary' />}
              placeholder={t('preview.scientific.json.searchPlaceholder')}
              aria-label={t('preview.scientific.json.search')}
              value={query}
              onChange={setQuery}
            />
          </div>
        )}
        {!canUseTree && (
          <div className='mx-10px mb-9px rounded-4px bg-warning-1 px-9px py-7px text-11px leading-17px text-warning-7'>
            {document.status === 'too-large'
              ? t(`preview.scientific.json.tooLarge.${document.reason}`, {
                  limit: document.limit.toLocaleString(i18n.resolvedLanguage),
                })
              : t('preview.scientific.json.invalid')}
          </div>
        )}
      </div>

      {mode === 'tree' && document.status === 'ready' ? (
        <div
          className='preview-content-scroll preview-json__tree min-h-0 flex-1 overflow-auto px-12px py-12px'
          role='tree'
          aria-label={t('preview.scientific.json.treeNamed', { name: filename })}
        >
          <TreeNode
            label={t('preview.scientific.json.root')}
            value={document.value}
            path='$'
            depth={0}
            query={query}
            matchingPaths={matchingPaths}
            expandedPaths={expandedPaths}
            onToggle={togglePath}
          />
          {query && matchingPaths?.size === 0 && (
            <div className='px-12px py-24px text-center text-12px text-t-tertiary'>
              {t('preview.scientific.json.noMatches')}
            </div>
          )}
        </div>
      ) : (
        <div className='min-h-0 flex-1 overflow-hidden'>
          <CodeEditor
            value={content}
            onChange={onContentChange ?? (() => undefined)}
            language='json'
            fileName={filename}
            readOnly={readOnly}
            onSelectionChange={onCodeSelection}
          />
        </div>
      )}
    </div>
  );
};

const ViewTab: React.FC<{
  active: boolean;
  disabled?: boolean;
  icon: React.ReactNode;
  label: string;
  onClick: () => void;
}> = ({ active, disabled = false, icon, label, onClick }) => (
  <button
    type='button'
    role='tab'
    aria-selected={active}
    disabled={disabled}
    className={`h-30px min-w-72px border-0 rounded-6px px-10px flex items-center justify-center gap-6px text-12px transition-colors ${
      active ? 'bg-1 text-t-primary shadow-sm' : 'bg-transparent text-t-secondary hover:text-t-primary'
    } disabled:cursor-not-allowed disabled:opacity-40`}
    onClick={onClick}
  >
    {icon}
    <span>{label}</span>
  </button>
);

const TreeNode: React.FC<TreeNodeProps> = ({
  label,
  value,
  path,
  depth,
  query,
  matchingPaths,
  expandedPaths,
  onToggle,
}) => {
  const { i18n, t } = useTranslation();
  if (matchingPaths && !matchingPaths.has(path)) return null;

  const container = isJsonContainer(value);
  const expanded = container && (expandedPaths.has(path) || Boolean(query));
  const entries = container ? Object.entries(value) : [];
  const description = container ? describeJsonContainer(value) : null;

  return (
    <div>
      <button
        type='button'
        role='treeitem'
        aria-expanded={container ? expanded : undefined}
        className='preview-json__node group min-h-34px w-full border-0 rounded-6px bg-transparent pr-10px flex items-center text-left hover:bg-fill-2 focus-visible:outline-2 focus-visible:outline-primary-6'
        style={{ paddingLeft: `${8 + depth * 14}px` }}
        onClick={() => container && onToggle(path)}
      >
        <span className='w-18px shrink-0 flex-center text-11px text-t-tertiary'>
          {container ? expanded ? <IconDown /> : <IconRight /> : null}
        </span>
        <span className='min-w-0 flex-1 flex items-baseline gap-6px overflow-hidden'>
          <span className='shrink-0 max-w-[48%] truncate text-13px font-[550] text-t-primary' title={label}>
            {label}
          </span>
          {container ? (
            <span className='truncate text-11px text-t-tertiary'>
              {t(`preview.scientific.json.container.${description!.kind}`, {
                count: description!.size,
                formattedCount: description!.size.toLocaleString(i18n.resolvedLanguage),
              })}
            </span>
          ) : (
            <span className={`min-w-0 truncate text-13px ${scalarColorClass(value)}`} title={formatJsonScalar(value)}>
              {formatJsonScalar(value)}
            </span>
          )}
        </span>
        {!container && (
          <span className='ml-6px shrink-0 text-10px uppercase text-t-tertiary opacity-0 group-hover:opacity-100'>
            {jsonScalarType(value)}
          </span>
        )}
      </button>
      {expanded && (
        <div role='group'>
          {entries.map(([key, child]) => (
            <TreeNode
              key={jsonPath(path, key)}
              label={Array.isArray(value) ? `[${key}]` : key}
              value={child}
              path={jsonPath(path, key)}
              depth={depth + 1}
              query={query}
              matchingPaths={matchingPaths}
              expandedPaths={expandedPaths}
              onToggle={onToggle}
            />
          ))}
          {entries.length === 0 && (
            <div
              className='h-28px flex items-center text-11px italic text-t-tertiary'
              style={{ paddingLeft: `${44 + depth * 14}px` }}
            >
              {t(Array.isArray(value) ? 'preview.scientific.json.emptyArray' : 'preview.scientific.json.emptyObject')}
            </div>
          )}
        </div>
      )}
    </div>
  );
};

function scalarColorClass(value: Exclude<JsonValue, JsonValue[] | { [key: string]: JsonValue }>): string {
  if (value === null) return 'text-t-tertiary';
  if (typeof value === 'number') return 'text-primary-6';
  if (typeof value === 'boolean') return 'text-[#6f4ea1]';
  return 'text-[#287052]';
}

export default SynonBiomedJsonViewer;
