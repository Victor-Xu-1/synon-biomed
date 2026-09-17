import {
  getSynonBiomedArtifactContentUrl,
  type SynonBiomedProjectArtifact,
  type SynonBiomedProjectBench,
} from '@/renderer/services/synonBiomedGateway';
import { Checkbox, Empty } from '@arco-design/web-react';
import {
  Code,
  Down,
  Download,
  FilePdfOne,
  FileText,
  GridFour,
  ListTwo,
  MoreOne,
  PictureOne,
  Right,
  Search,
  TableFile,
} from '@icon-park/react';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ArtifactBatchActions } from './ArtifactBatchActions';
import {
  buildProjectArtifactGroups,
  classifyProjectArtifact,
  getVisibleProjectArtifacts,
  type ProjectArtifactGroup,
  type ProjectArtifactVisualKind,
} from './projectArtifactLibraryModel';

type ProjectArtifactLibraryProps = {
  artifacts: SynonBiomedProjectArtifact[];
  benches: SynonBiomedProjectBench[];
  onOpenArtifact: (artifact: SynonBiomedProjectArtifact) => void;
  onOpenConversation: (frameId: string) => void;
};

type LibraryViewMode = 'list' | 'grid';

const ProjectArtifactLibrary: React.FC<ProjectArtifactLibraryProps> = ({
  artifacts,
  benches,
  onOpenArtifact,
  onOpenConversation,
}) => {
  const { t } = useTranslation();
  const [query, setQuery] = useState('');
  const [viewMode, setViewMode] = useState<LibraryViewMode>('list');
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(() => new Set());
  const [selectedIds, setSelectedIds] = useState<Set<string>>(() => new Set());
  const groups = useMemo(() => buildProjectArtifactGroups({ artifacts, benches, query }), [artifacts, benches, query]);
  const visibleArtifacts = useMemo(() => getVisibleProjectArtifacts(artifacts), [artifacts]);
  const visibleCount = visibleArtifacts.length;
  const selectedArtifacts = visibleArtifacts.filter((artifact) => selectedIds.has(artifact.artifactId));

  useEffect(() => {
    const validIds = new Set(visibleArtifacts.map((artifact) => artifact.artifactId));
    setSelectedIds((current) => new Set([...current].filter((artifactId) => validIds.has(artifactId))));
  }, [visibleArtifacts]);

  const setSelectedArtifactIds = (artifactIds: string[]) => setSelectedIds(new Set(artifactIds));

  const toggleGroup = (groupId: string) => {
    setCollapsedGroups((current) => {
      const next = new Set(current);
      if (next.has(groupId)) next.delete(groupId);
      else next.add(groupId);
      return next;
    });
  };

  return (
    <section className='min-w-0' aria-label={t('conversation.projectArtifacts.library')}>
      <div className='flex flex-wrap items-center justify-between gap-10px'>
        <div className='flex items-center gap-8px text-t-primary'>
          <FileText theme='outline' size={18} />
          <h2 className='m-0 text-16px leading-24px font-[600]'>{t('conversation.projectArtifacts.title')}</h2>
          <span className='text-12px text-t-tertiary'>{visibleCount}</span>
        </div>
        <div className='flex items-center gap-6px'>
          {visibleCount > 0 && (
            <Checkbox
              aria-label={t('conversation.projectArtifacts.selectAll')}
              checked={selectedIds.size > 0 && selectedIds.size === visibleCount}
              indeterminate={selectedIds.size > 0 && selectedIds.size < visibleCount}
              onChange={(checked) =>
                setSelectedIds(checked ? new Set(visibleArtifacts.map((artifact) => artifact.artifactId)) : new Set())
              }
            >
              {t('conversation.projectArtifacts.selectAllShort')}
            </Checkbox>
          )}
          {selectedArtifacts.length > 0 && (
            <ArtifactBatchActions artifacts={selectedArtifacts} onSelectionChange={setSelectedArtifactIds} />
          )}
          <label className='h-30px min-w-180px flex items-center gap-7px px-9px border border-solid border-[var(--color-border-2)] bg-1 focus-within:border-[rgb(var(--primary-6))]'>
            <Search theme='outline' size={14} className='shrink-0 text-t-tertiary' />
            <input
              type='search'
              aria-label={t('conversation.projectArtifacts.search')}
              value={query}
              placeholder={t('conversation.projectArtifacts.searchPlaceholder')}
              className='min-w-0 flex-1 border-0 outline-0 bg-transparent text-12px text-t-primary placeholder:text-t-tertiary'
              onChange={(event) => setQuery(event.target.value)}
            />
          </label>
          <ViewModeButton
            label={t('conversation.projectArtifacts.gridView')}
            active={viewMode === 'grid'}
            icon={<GridFour theme='outline' size={14} />}
            onClick={() => setViewMode('grid')}
          />
          <ViewModeButton
            label={t('conversation.projectArtifacts.listView')}
            active={viewMode === 'list'}
            icon={<ListTwo theme='outline' size={14} />}
            onClick={() => setViewMode('list')}
          />
        </div>
      </div>

      <div className='mt-12px border-t border-solid border-[var(--color-border-2)]'>
        {groups.length === 0 ? (
          <Empty
            className='py-32px'
            description={
              query.trim() ? t('conversation.projectArtifacts.noMatches') : t('conversation.projectArtifacts.empty')
            }
          />
        ) : (
          groups.map((group) => (
            <ArtifactGroup
              key={group.id}
              group={group}
              collapsed={collapsedGroups.has(group.id)}
              viewMode={viewMode}
              onToggle={() => toggleGroup(group.id)}
              onOpenArtifact={onOpenArtifact}
              onOpenConversation={onOpenConversation}
              selectedIds={selectedIds}
              onSelectionChange={(artifactId, checked) => {
                setSelectedIds((current) => {
                  const next = new Set(current);
                  if (checked) next.add(artifactId);
                  else next.delete(artifactId);
                  return next;
                });
              }}
            />
          ))
        )}
      </div>
    </section>
  );
};

const ViewModeButton: React.FC<{
  label: string;
  active: boolean;
  icon: React.ReactNode;
  onClick: () => void;
}> = ({ label, active, icon, onClick }) => (
  <button
    type='button'
    title={label}
    aria-label={label}
    aria-pressed={active}
    className={`size-30px shrink-0 flex-center border border-solid border-[var(--color-border-2)] cursor-pointer transition-colors ${active ? 'bg-fill-2 text-t-primary' : 'bg-transparent text-t-tertiary hover:bg-fill-1 hover:text-t-primary'}`}
    onClick={onClick}
  >
    {icon}
  </button>
);

const ArtifactGroup: React.FC<{
  group: ProjectArtifactGroup;
  collapsed: boolean;
  viewMode: LibraryViewMode;
  onToggle: () => void;
  onOpenArtifact: (artifact: SynonBiomedProjectArtifact) => void;
  onOpenConversation: (frameId: string) => void;
  selectedIds: ReadonlySet<string>;
  onSelectionChange: (artifactId: string, checked: boolean) => void;
}> = ({ group, collapsed, viewMode, onToggle, onOpenArtifact, onOpenConversation, selectedIds, onSelectionChange }) => {
  const { t, i18n } = useTranslation();
  const label = group.labelKey ? t(`conversation.projectArtifacts.groups.${group.labelKey}`) : group.label;
  return (
    <section
      aria-label={t('conversation.projectArtifacts.groupLabel', { label })}
      className='border-b border-solid border-[var(--color-border-2)]'
    >
      <div className='min-h-40px flex items-center justify-between gap-8px py-7px'>
        {group.kind === 'search' ? (
          <div className='min-w-0 flex items-center gap-7px text-12px font-[500] text-t-primary'>
            <span>{label}</span>
            <span className='text-t-tertiary'>{group.artifacts.length}</span>
          </div>
        ) : (
          <button
            type='button'
            aria-label={
              collapsed
                ? t('conversation.projectArtifacts.expandNamed', { label })
                : t('conversation.projectArtifacts.collapseNamed', { label })
            }
            className='min-w-0 flex items-center gap-7px border-0 bg-transparent p-0 text-left cursor-pointer text-12px font-[500] text-t-primary'
            onClick={onToggle}
          >
            {collapsed ? <Right theme='outline' size={12} /> : <Down theme='outline' size={12} />}
            <span className='truncate'>{label}</span>
            <span className='shrink-0 text-t-tertiary'>{group.artifacts.length}</span>
          </button>
        )}
        <span className='shrink-0 text-11px text-t-tertiary'>{formatRelativeDate(group.latestAt, i18n.language)}</span>
      </div>
      {!collapsed && (
        <div className={viewMode === 'grid' ? 'grid grid-cols-1 gap-8px pb-10px sm:grid-cols-2' : 'pb-4px'}>
          {group.artifacts.map((artifact) =>
            viewMode === 'grid' ? (
              <ArtifactGridItem
                key={artifact.artifactId}
                artifact={artifact}
                onOpen={() => onOpenArtifact(artifact)}
                onOpenConversation={onOpenConversation}
                selected={selectedIds.has(artifact.artifactId)}
                onSelectionChange={(checked) => onSelectionChange(artifact.artifactId, checked)}
              />
            ) : (
              <ArtifactListItem
                key={artifact.artifactId}
                artifact={artifact}
                onOpen={() => onOpenArtifact(artifact)}
                onOpenConversation={onOpenConversation}
                selected={selectedIds.has(artifact.artifactId)}
                onSelectionChange={(checked) => onSelectionChange(artifact.artifactId, checked)}
              />
            )
          )}
        </div>
      )}
    </section>
  );
};

const ArtifactListItem: React.FC<{
  artifact: SynonBiomedProjectArtifact;
  onOpen: () => void;
  onOpenConversation: (frameId: string) => void;
  selected: boolean;
  onSelectionChange: (checked: boolean) => void;
}> = ({ artifact, onOpen, onOpenConversation, selected, onSelectionChange }) => {
  const { t } = useTranslation();
  return (
    <div className='min-h-48px flex items-center gap-6px border-t border-solid border-[var(--color-border-2)] first:border-t-0 group'>
      <Checkbox
        aria-label={t('conversation.projectArtifacts.selectNamed', { name: artifact.filename })}
        checked={selected}
        onChange={onSelectionChange}
        className='shrink-0'
      />
      <button
        type='button'
        aria-label={t('conversation.projectArtifacts.previewNamed', { name: artifact.filename })}
        className='min-w-0 flex-1 flex items-center gap-9px py-8px border-0 bg-transparent text-left cursor-pointer'
        onClick={onOpen}
      >
        <ArtifactKindIcon kind={classifyProjectArtifact(artifact)} />
        <div className='min-w-0 flex-1'>
          <div className='truncate text-12px font-[500] text-t-primary'>{artifact.filename}</div>
          <div className='mt-2px truncate text-11px text-t-tertiary'>
            {artifact.contentType ?? t('conversation.projectArtifacts.unknownType')} · {formatBytes(artifact.sizeBytes)}
          </div>
        </div>
      </button>
      <ArtifactActions artifact={artifact} onOpen={onOpen} onOpenConversation={onOpenConversation} />
    </div>
  );
};

const ArtifactGridItem: React.FC<{
  artifact: SynonBiomedProjectArtifact;
  onOpen: () => void;
  onOpenConversation: (frameId: string) => void;
  selected: boolean;
  onSelectionChange: (checked: boolean) => void;
}> = ({ artifact, onOpen, onOpenConversation, selected, onSelectionChange }) => {
  const { t } = useTranslation();
  const kind = classifyProjectArtifact(artifact);
  const contentUrl = getSynonBiomedArtifactContentUrl(artifact.artifactId);
  return (
    <article className='min-w-0 border border-solid border-[var(--color-border-2)] bg-1'>
      <div className='h-30px flex items-center px-8px border-b border-arco-2'>
        <Checkbox
          aria-label={t('conversation.projectArtifacts.selectNamed', { name: artifact.filename })}
          checked={selected}
          onChange={onSelectionChange}
        >
          {t('conversation.projectArtifacts.select')}
        </Checkbox>
      </div>
      <button
        type='button'
        aria-label={t('conversation.projectArtifacts.previewNamed', { name: artifact.filename })}
        className='w-full h-112px flex-center overflow-hidden border-0 border-b border-solid border-[var(--color-border-2)] bg-fill-1 cursor-pointer'
        onClick={onOpen}
      >
        {kind === 'image' ? (
          <img src={contentUrl} alt={artifact.filename} loading='lazy' className='size-full object-contain' />
        ) : (
          <ArtifactKindIcon kind={kind} size={28} />
        )}
      </button>
      <div className='min-w-0 flex items-center gap-5px px-9px py-8px'>
        <button
          type='button'
          className='min-w-0 flex-1 border-0 bg-transparent p-0 text-left cursor-pointer'
          onClick={onOpen}
        >
          <div className='truncate text-12px font-[500] text-t-primary'>{artifact.filename}</div>
          <div className='mt-2px truncate text-11px text-t-tertiary'>{formatBytes(artifact.sizeBytes)}</div>
        </button>
        <ArtifactActions artifact={artifact} onOpen={onOpen} onOpenConversation={onOpenConversation} />
      </div>
    </article>
  );
};

const ArtifactActions: React.FC<{
  artifact: SynonBiomedProjectArtifact;
  onOpen: () => void;
  onOpenConversation: (frameId: string) => void;
}> = ({ artifact, onOpen, onOpenConversation }) => {
  const { t } = useTranslation();
  const contentUrl = getSynonBiomedArtifactContentUrl(artifact.artifactId);
  const frameId = artifact.rootFrameId ?? artifact.frameId ?? artifact.creatingFrameId;
  return (
    <div className='shrink-0 flex items-center gap-2px'>
      <a
        href={contentUrl}
        download={artifact.filename}
        aria-label={t('conversation.projectArtifacts.downloadNamed', { name: artifact.filename })}
        title={t('common.download')}
        className='size-26px flex-center text-t-tertiary hover:text-t-primary hover:bg-fill-1'
      >
        <Download theme='outline' size={13} />
      </a>
      {frameId && (
        <button
          type='button'
          aria-label={t('conversation.projectArtifacts.openTaskForNamed', { name: artifact.filename })}
          title={t('conversation.projectArtifacts.openTask')}
          className='size-26px flex-center border-0 bg-transparent text-t-tertiary hover:text-t-primary hover:bg-fill-1 cursor-pointer'
          onClick={() => onOpenConversation(frameId)}
        >
          <Right theme='outline' size={13} />
        </button>
      )}
      <button
        type='button'
        aria-label={t('conversation.projectArtifacts.moreNamed', { name: artifact.filename })}
        title={t('conversation.projectArtifacts.viewDetails')}
        className='size-26px flex-center border-0 bg-transparent text-t-tertiary hover:text-t-primary hover:bg-fill-1 cursor-pointer'
        onClick={onOpen}
      >
        <MoreOne theme='outline' size={13} />
      </button>
    </div>
  );
};

const ArtifactKindIcon: React.FC<{ kind: ProjectArtifactVisualKind; size?: number }> = ({ kind, size = 17 }) => {
  const className = 'shrink-0 text-t-secondary';
  if (kind === 'image') return <PictureOne theme='outline' size={size} className={className} />;
  if (kind === 'table') return <TableFile theme='outline' size={size} className={className} />;
  if (kind === 'pdf') return <FilePdfOne theme='outline' size={size} className={className} />;
  if (kind === 'structure') return <Code theme='outline' size={size} className={className} />;
  return <FileText theme='outline' size={size} className={className} />;
};

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function formatRelativeDate(value: string | null, language: string): string {
  if (!value) return '';
  const timestamp = Date.parse(value);
  if (Number.isNaN(timestamp)) return value;
  const ageDays = Math.max(0, Math.floor((Date.now() - timestamp) / 86_400_000));
  if (ageDays < 30) return new Intl.RelativeTimeFormat(language, { numeric: 'auto' }).format(-ageDays, 'day');
  return new Date(timestamp).toLocaleDateString(language);
}

export default ProjectArtifactLibrary;
