import type { IDirOrFile } from '@/common/adapter/ipcBridge';
import { Empty, Spin } from '@arco-design/web-react';
import { Down, Download, GridFour, ListTwo, MoreOne, Refresh, Right, Search } from '@icon-park/react';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Virtuoso } from 'react-virtuoso';
import FileTypeIcon from './FileTypeIcon';
import ArtifactThumbnailPreview from '@/renderer/components/synonBiomed/files/ArtifactThumbnailPreview';
import { resolveSynonBiomedArtifactPreviewPlan } from '@/renderer/services/synonBiomedArtifactPreview';
import { preloadSynonBiomedStructureViewer } from '@/renderer/pages/conversation/Preview/components/viewers/scientificPreviewLoaders';

type ViewMode = 'grid' | 'list';

type ArtifactGroup = {
  id: string;
  label: string | null;
  artifacts: IDirOrFile[];
};

type ArtifactLibraryRow =
  | {
      type: 'group';
      id: string;
      groupId: string;
      label: string | null;
      count: number;
      collapsed: boolean;
    }
  | {
      type: 'artifacts';
      id: string;
      groupId: string;
      artifacts: IDirOrFile[];
    };

type Props = {
  nodes: IDirOrFile[];
  loading: boolean;
  total?: number;
  hasMore?: boolean;
  loadingMore?: boolean;
  loadMoreError?: boolean;
  onOpen: (artifact: IDirOrFile) => void;
  onMenu: (artifact: IDirOrFile, x: number, y: number) => void;
  onRefresh: () => void;
  onSearch?: (query: string) => void | Promise<unknown>;
  onLoadMore?: () => void | Promise<unknown>;
};

function preloadArtifactPreview(artifact: IDirOrFile): void {
  const plan = resolveSynonBiomedArtifactPreviewPlan({
    filename: artifact.name,
    contentType: artifact.contentType,
    previewKind: artifact.previewKind,
  });
  if (plan.type === 'structure') {
    void preloadSynonBiomedStructureViewer().catch((): undefined => undefined);
  }
}

const collectArtifactFiles = (node: IDirOrFile): IDirOrFile[] => {
  if (node.isFile) return [node];
  return (node.children ?? []).flatMap(collectArtifactFiles);
};

export const buildSynonBiomedArtifactGroups = (nodes: IDirOrFile[], query = ''): ArtifactGroup[] => {
  const normalizedQuery = query.trim().toLocaleLowerCase();
  const syntheticRoot = nodes.length === 1 && !nodes[0]?.isFile && !nodes[0]?.relativePath && nodes[0]?.children;
  const roots = syntheticRoot ? nodes[0].children! : nodes;
  const groups: ArtifactGroup[] = [];

  for (const node of roots) {
    const artifacts = collectArtifactFiles(node).filter((artifact) => {
      if (!artifact.artifactId) return false;
      return !normalizedQuery || artifact.name.toLocaleLowerCase().includes(normalizedQuery);
    });
    if (artifacts.length === 0) continue;
    groups.push({
      id: node.relativePath || node.fullPath || node.name,
      label: node.isFile ? null : node.name,
      artifacts,
    });
  }

  return groups;
};

export const buildSynonBiomedArtifactRows = (
  groups: ArtifactGroup[],
  collapsed: ReadonlySet<string>,
  viewMode: ViewMode
): ArtifactLibraryRow[] => {
  const rows: ArtifactLibraryRow[] = [];
  // Keep virtualization independent from the rendered column count. CSS wraps
  // each four-item chunk into two compact columns in a narrow rail and up to
  // four columns in a wider workspace, so resize never needs a second JS width
  // observer or a remounting layout branch.
  const artifactsPerRow = viewMode === 'grid' ? 4 : 1;

  for (const group of groups) {
    const isCollapsed = collapsed.has(group.id);
    rows.push({
      type: 'group',
      id: `group:${group.id}`,
      groupId: group.id,
      label: group.label,
      count: group.artifacts.length,
      collapsed: isCollapsed,
    });
    if (isCollapsed) continue;

    for (let index = 0; index < group.artifacts.length; index += artifactsPerRow) {
      const artifacts = group.artifacts.slice(index, index + artifactsPerRow);
      rows.push({
        type: 'artifacts',
        id: `artifacts:${group.id}:${artifacts.map((artifact) => artifact.artifactId ?? artifact.relativePath).join(':')}`,
        groupId: group.id,
        artifacts,
      });
    }
  }

  return rows;
};

export const synonBiomedArtifactItemKey = (groupId: string, artifact: IDirOrFile, occurrence: number): string =>
  [
    groupId,
    artifact.artifactId ?? '',
    artifact.versionId ?? '',
    artifact.relativePath || artifact.fullPath || artifact.name,
    occurrence,
  ].join('\0');

const SynonBiomedArtifactLibrary: React.FC<Props> = ({
  nodes,
  loading,
  total,
  hasMore = false,
  loadingMore = false,
  loadMoreError = false,
  onOpen,
  onMenu,
  onRefresh,
  onSearch,
  onLoadMore,
}) => {
  const { t } = useTranslation();
  const [query, setQuery] = useState('');
  const [viewMode, setViewMode] = useState<ViewMode>('grid');
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());
  const initializedGroupsRef = useRef(new Set<string>());
  const lastSubmittedQueryRef = useRef('');
  const groups = useMemo(() => buildSynonBiomedArtifactGroups(nodes, query), [nodes, query]);
  const loadedCount = groups.reduce((sum, group) => sum + group.artifacts.length, 0);
  const count = total ?? loadedCount;
  const rows = useMemo(() => buildSynonBiomedArtifactRows(groups, collapsed, viewMode), [collapsed, groups, viewMode]);

  useEffect(() => {
    setCollapsed((current) => {
      const next = new Set(current);
      let changed = false;
      const visibleGroupIds = new Set(groups.map((group) => group.id));
      if (!query.trim()) {
        for (const groupId of initializedGroupsRef.current) {
          if (!visibleGroupIds.has(groupId)) initializedGroupsRef.current.delete(groupId);
        }
        for (const groupId of next) {
          if (!visibleGroupIds.has(groupId)) {
            next.delete(groupId);
            changed = true;
          }
        }
      }
      groups.forEach((group, index) => {
        if (query.trim()) {
          if (next.delete(group.id)) changed = true;
        } else if (!initializedGroupsRef.current.has(group.id) && index > 0) {
          next.add(group.id);
          changed = true;
        }
        initializedGroupsRef.current.add(group.id);
      });
      return changed ? next : current;
    });
  }, [groups, query]);

  useEffect(() => {
    if (!onSearch || query === lastSubmittedQueryRef.current) return;
    const timer = window.setTimeout(() => {
      lastSubmittedQueryRef.current = query;
      void onSearch(query);
    }, 200);
    return () => window.clearTimeout(timer);
  }, [onSearch, query]);

  const toggleGroup = useCallback((id: string) => {
    setCollapsed((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const renderRow = useCallback(
    (_index: number, row: ArtifactLibraryRow) => {
      if (row.type === 'group') {
        const groupLabel = row.label ?? t('conversation.projectArtifacts.ungrouped');
        return (
          <button
            type='button'
            aria-expanded={!row.collapsed}
            aria-label={t(
              row.collapsed
                ? 'conversation.projectArtifacts.expandNamed'
                : 'conversation.projectArtifacts.collapseNamed',
              { label: groupLabel }
            )}
            className='w-full min-h-38px flex items-center gap-6px px-12px border-0 border-b border-solid border-[var(--color-border-2)] bg-transparent text-left cursor-pointer hover:bg-fill-1'
            onClick={() => toggleGroup(row.groupId)}
          >
            {row.collapsed ? <Right theme='outline' size={12} /> : <Down theme='outline' size={12} />}
            <span className='min-w-0 flex-1 truncate text-12px font-500 text-t-primary'>{groupLabel}</span>
            <span className='text-11px text-t-tertiary'>{row.count}</span>
          </button>
        );
      }

      return (
        <div
          className={
            viewMode === 'grid'
              ? 'artifact-library__grid-row'
              : 'px-10px border-b border-solid border-[var(--color-border-2)]'
          }
        >
          {row.artifacts.map((artifact, index) => (
            <ArtifactItem
              key={synonBiomedArtifactItemKey(row.groupId, artifact, index)}
              artifact={artifact}
              viewMode={viewMode}
              onOpen={() => onOpen(artifact)}
              onMenu={(x, y) => onMenu(artifact, x, y)}
            />
          ))}
        </div>
      );
    },
    [onMenu, onOpen, t, toggleGroup, viewMode]
  );

  return (
    <section
      className='artifact-library size-full min-h-0 flex flex-col bg-1'
      aria-label={t('conversation.projectArtifacts.library')}
    >
      <div className='h-42px shrink-0 flex items-center justify-between gap-8px px-12px border-b border-solid border-[var(--color-border-2)]'>
        <div className='flex items-center gap-6px min-w-0'>
          <span className='text-13px font-600 text-t-primary'>{t('conversation.projectArtifacts.title')}</span>
          <span className='text-11px text-t-tertiary'>{count}</span>
        </div>
        <div className='flex items-center gap-3px'>
          <IconButton
            label={t('conversation.projectArtifacts.refresh')}
            onClick={onRefresh}
            icon={<Refresh theme='outline' size={14} />}
          />
          <IconButton
            label={t('conversation.projectArtifacts.gridView')}
            active={viewMode === 'grid'}
            onClick={() => setViewMode('grid')}
            icon={<GridFour theme='outline' size={14} />}
          />
          <IconButton
            label={t('conversation.projectArtifacts.listView')}
            active={viewMode === 'list'}
            onClick={() => setViewMode('list')}
            icon={<ListTwo theme='outline' size={14} />}
          />
        </div>
      </div>

      <label className='artifact-library__search h-36px shrink-0 flex items-center gap-7px mx-12px my-9px px-9px border border-solid border-[var(--color-border-2)] bg-1 focus-within:border-[rgb(var(--primary-6))]'>
        <Search theme='outline' size={14} className='text-t-tertiary' />
        <input
          type='search'
          aria-label={t('conversation.projectArtifacts.search')}
          placeholder={t('conversation.projectArtifacts.searchPlaceholder')}
          value={query}
          className='min-w-0 flex-1 border-0 outline-0 bg-transparent text-12px text-t-primary placeholder:text-t-tertiary'
          onChange={(event) => setQuery(event.target.value)}
        />
      </label>

      <div className='min-h-0 flex-1 border-t border-solid border-[var(--color-border-2)]'>
        {loading && nodes.length === 0 ? (
          <div className='h-160px flex-center'>
            <Spin />
          </div>
        ) : groups.length === 0 ? (
          <Empty
            className='py-32px'
            description={t(
              query.trim() ? 'conversation.projectArtifacts.noMatches' : 'conversation.projectArtifacts.empty'
            )}
          />
        ) : (
          <Virtuoso<ArtifactLibraryRow>
            key={viewMode}
            data={rows}
            computeItemKey={(_index, row) => row.id}
            itemContent={renderRow}
            defaultItemHeight={viewMode === 'grid' ? 236 : 46}
            increaseViewportBy={{ top: 280, bottom: 560 }}
            endReached={() => {
              if (hasMore && !loadingMore && !loadMoreError) void onLoadMore?.();
            }}
            components={{
              Footer: () =>
                loadingMore ? (
                  <div role='status' className='h-42px flex-center text-11px text-t-tertiary'>
                    <Spin size={14} />
                  </div>
                ) : loadMoreError ? (
                  <div className='h-42px flex-center'>
                    <button
                      type='button'
                      className='border border-solid border-[var(--color-border-2)] bg-1 px-10px py-5px text-11px text-t-secondary cursor-pointer hover:bg-fill-1'
                      onClick={() => void onLoadMore?.()}
                    >
                      {t('conversation.projectArtifacts.retryLoad')}
                    </button>
                  </div>
                ) : (
                  <div className='h-8px' />
                ),
            }}
            data-testid='artifact-library-scroller'
            className='size-full'
          />
        )}
      </div>
    </section>
  );
};

const ArtifactItem: React.FC<{
  artifact: IDirOrFile;
  viewMode: ViewMode;
  onOpen: () => void;
  onMenu: (x: number, y: number) => void;
}> = React.memo(({ artifact, viewMode, onOpen, onMenu }) => {
  const { t } = useTranslation();
  if (viewMode === 'list') {
    return (
      <div
        className='min-h-46px flex items-center gap-6px border-t border-solid border-[var(--color-border-2)] first:border-t-0'
        onContextMenu={(event) => {
          event.preventDefault();
          onMenu(event.clientX, event.clientY);
        }}
      >
        <button
          type='button'
          className='min-w-0 flex-1 flex items-center gap-8px py-7px border-0 bg-transparent text-left cursor-pointer'
          onPointerEnter={() => preloadArtifactPreview(artifact)}
          onFocus={() => preloadArtifactPreview(artifact)}
          onClick={onOpen}
        >
          <FileTypeIcon node={artifact} expanded={false} />
          <span className='min-w-0 flex-1 truncate text-12px text-t-primary'>{artifact.name}</span>
          <span className='text-10px text-t-tertiary'>{formatBytes(artifact.sizeBytes)}</span>
        </button>
        <ArtifactActions artifact={artifact} onMenu={onMenu} />
      </div>
    );
  }

  return (
    <article
      className='artifact-library__card relative min-w-0 border border-solid border-[var(--color-border-2)] bg-1'
      style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 114px' }}
      onContextMenu={(event) => {
        event.preventDefault();
        onMenu(event.clientX, event.clientY);
      }}
    >
      <button
        type='button'
        aria-label={t('conversation.projectArtifacts.previewNamed', { name: artifact.name })}
        className='artifact-library__preview w-full flex-center overflow-hidden border-0 border-b border-solid border-[var(--color-border-2)] bg-fill-1 cursor-pointer'
        onPointerEnter={() => preloadArtifactPreview(artifact)}
        onFocus={() => preloadArtifactPreview(artifact)}
        onClick={onOpen}
      >
        <ArtifactThumbnailPreview
          filename={artifact.name}
          contentUrl={artifact.contentUrl}
          contentType={artifact.contentType}
          previewKind={artifact.previewKind}
          sizeBytes={artifact.sizeBytes}
          imageFit='cover'
        />
      </button>
      <div className='artifact-library__actions absolute right-4px top-4px'>
        <ArtifactActions artifact={artifact} onMenu={onMenu} />
      </div>
      <button
        type='button'
        className='artifact-library__metadata'
        onPointerEnter={() => preloadArtifactPreview(artifact)}
        onFocus={() => preloadArtifactPreview(artifact)}
        onClick={onOpen}
      >
        <span className='artifact-library__name'>{artifact.name}</span>
        <span className='artifact-library__size'>{formatBytes(artifact.sizeBytes)}</span>
      </button>
    </article>
  );
});

const ArtifactActions: React.FC<{
  artifact: IDirOrFile;
  onMenu: (x: number, y: number) => void;
}> = ({ artifact, onMenu }) => {
  const { t } = useTranslation();
  return (
    <div className='shrink-0 flex items-center gap-1px'>
      {artifact.contentUrl ? (
        <a
          href={artifact.contentUrl}
          download={artifact.name}
          aria-label={t('conversation.projectArtifacts.downloadNamed', { name: artifact.name })}
          title={t('conversation.projectArtifacts.download')}
          className='size-24px flex-center text-t-tertiary hover:text-t-primary hover:bg-fill-1'
        >
          <Download theme='outline' size={12} />
        </a>
      ) : null}
      <button
        type='button'
        aria-label={t('conversation.projectArtifacts.moreNamed', { name: artifact.name })}
        title={t('conversation.projectArtifacts.moreActions')}
        className='size-24px flex-center border-0 bg-transparent text-t-tertiary hover:text-t-primary hover:bg-fill-1 cursor-pointer'
        onClick={(event) => {
          event.stopPropagation();
          const rect = event.currentTarget.getBoundingClientRect();
          onMenu(rect.right, rect.bottom);
        }}
      >
        <MoreOne theme='outline' size={12} />
      </button>
    </div>
  );
};

const IconButton: React.FC<{ label: string; icon: React.ReactNode; active?: boolean; onClick: () => void }> = ({
  label,
  icon,
  active,
  onClick,
}) => (
  <button
    type='button'
    aria-label={label}
    aria-pressed={active}
    title={label}
    className={`size-28px flex-center border-0 cursor-pointer ${active ? 'bg-fill-2 text-t-primary' : 'bg-transparent text-t-tertiary hover:bg-fill-1 hover:text-t-primary'}`}
    onClick={onClick}
  >
    {icon}
  </button>
);

const formatBytes = (bytes?: number): string => {
  if (bytes === undefined) return '';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
};

export default SynonBiomedArtifactLibrary;
