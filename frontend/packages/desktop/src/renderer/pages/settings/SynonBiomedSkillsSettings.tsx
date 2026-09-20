/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Button, Dropdown, Menu, Message, Modal, Spin, Switch, Tag } from '@arco-design/web-react';
import { FileZip, Github, Refresh } from '@icon-park/react';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import SettingsPageHeader from './components/SettingsPageHeader';
import SettingsPageWrapper from './components/SettingsPageWrapper';
import {
  SettingsGeneratedArtwork,
  SettingsGeneratedIcon,
  type SettingsGeneratedArtworkId,
  type SettingsGeneratedIconId,
} from './components/SettingsGeneratedAsset';
import { compactSettingsDescription } from './components/settingsPresentation';
import SettingsPagination from './components/SettingsPagination';
import {
  CreatePersonalSkillModal,
  GitHubSkillImportModal,
  SkillDetailModal,
  type SkillModalItem,
} from './skills/SynonBiomedSkillLibraryModals';
import { SkillLibraryToolbar, type SkillSourceFilter, type SkillStatusFilter } from './skills/SkillLibraryToolbar';
import { SkillMarketModal } from './skills/SkillMarketModal';
import { loadAvailableSkillsWithSynonBiomed } from '@/renderer/services/skills/skillsCatalog';
import {
  getSynonBiomedSkillCategoryLabel,
  getSynonBiomedSkillCategoryOptions,
  resolveSynonBiomedSkillCategory,
  type SynonBiomedSkillCategorySelection,
} from '@/renderer/services/skills/synonBiomedSkillCategories';
import { setSynonBiomedSkillEnabled } from '@/renderer/services/synonBiomedCapabilities';
import { resolveSkillDescription } from '@/renderer/services/skills/synonBiomedSkillDescriptions';
import {
  deleteSynonBiomedPersonalSkill,
  importSynonBiomedSkillFile,
  loadSynonBiomedSkillDrafts,
  loadSynonBiomedSkillSources,
  removeSynonBiomedSkillSource,
  type SynonBiomedSkillDraft,
  type SynonBiomedSkillSource,
} from '@/renderer/services/skills/synonBiomedSkillLibrary';
import {
  findSynonBiomedSkillUsage,
  loadSynonBiomedSkillUsage,
  type SynonBiomedSkillUsage,
  type SynonBiomedSkillUsageByName,
} from '@/renderer/services/skills/synonBiomedSkillUsage';

interface SkillInfo {
  name: string;
  displayName?: string;
  description: string;
  description_i18n?: Record<string, string>;
  source?: string;
  category?: string | null;
  license?: string | null;
  attachedAgents?: string[];
  thirdParty?: Array<{
    kind: string;
    name: string;
    provider?: string;
    license?: string;
    termsUrl?: string;
    infoUrl?: string;
  }>;
  enabled?: boolean;
}

interface SynonBiomedSkillsSettingsProps {
  /** When false, renders without SettingsPageWrapper for route/tab embedding. */
  withWrapper?: boolean;
}

// Five compact columns by three rows keeps the review sheet balanced.
const SKILLS_PAGE_SIZE = 15;

function resolveSkillArtwork(skill: Pick<SkillInfo, 'name' | 'displayName' | 'category'>): SettingsGeneratedArtworkId {
  const searchable = `${skill.name} ${skill.displayName || ''} ${skill.category || ''}`.toLowerCase();
  if (/protein|alphafold|boltz|structure|binding/.test(searchable)) return 'protein';
  if (/chem|molecule|drug|medicinal|dock/.test(searchable)) return 'chemistry';
  if (/rna|transcript|expression/.test(searchable)) return 'rna';
  if (/gene|dna|variant|genome|genetic/.test(searchable)) return 'dna';
  if (/clinical|trial|patient|disease/.test(searchable)) return 'clinical';
  if (/regulat|pathway|network/.test(searchable)) return 'regulation';
  return 'book';
}

function resolveSkillIcon(skill: Pick<SkillInfo, 'name' | 'displayName' | 'category'>): SettingsGeneratedIconId {
  const searchable = `${skill.name} ${skill.displayName || ''} ${skill.category || ''}`.toLowerCase();
  if (/protein|alphafold|boltz|structure|binding/.test(searchable)) return 'connector-protein';
  if (/chem|molecule|drug|medicinal|dock/.test(searchable)) return 'connector-molecule';
  if (/rna|transcript|expression/.test(searchable)) return 'connector-rna';
  if (/gene|dna|variant|genome|genetic/.test(searchable)) return 'connector-dna';
  if (/clinical|trial|patient|disease/.test(searchable)) return 'connector-clipboard';
  if (/regulat|pathway|network/.test(searchable)) return 'connector-regulation';
  return 'connector-book';
}

const normalizeTestId = (name: string): string => name.replace(/[:/\s<>"'|?*]/g, '-');

export function skillSourceLabel(source?: string, t?: ReturnType<typeof useTranslation>['t']): string {
  if (!source) return '';
  const normalized = source.trim().toLocaleLowerCase();
  const labels: Record<string, string> = {
    bundled: 'builtin',
    synon_llm: 'builtin',
    marketplace: 'recommended',
    github: 'github',
    personal: 'personal',
    custom: 'custom',
    user: 'personal',
  };
  const labelKey = labels[normalized];
  return labelKey && t ? t(`settings.skillsSettings.sources.${labelKey}`) : source;
}

const SynonBiomedSkillsSettings: React.FC<SynonBiomedSkillsSettingsProps> = ({ withWrapper = true }) => {
  const { i18n, t } = useTranslation();
  const [message, messageContext] = Message.useMessage({ maxCount: 3 });
  const messageRef = useRef(message);
  const translationRef = useRef(t);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const loadGenerationRef = useRef(0);
  messageRef.current = message;
  translationRef.current = t;

  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const [importingFile, setImportingFile] = useState(false);
  const [availableSkills, setAvailableSkills] = useState<SkillInfo[]>([]);
  const [drafts, setDrafts] = useState<SynonBiomedSkillDraft[]>([]);
  const [sources, setSources] = useState<SynonBiomedSkillSource[]>([]);
  const [skillUsage, setSkillUsage] = useState<SynonBiomedSkillUsageByName | null>(null);
  const [searchQuery, setSearchQuery] = useState('');
  const [filter, setFilter] = useState<SkillStatusFilter>('all');
  const [categoryFilter, setCategoryFilter] = useState<SynonBiomedSkillCategorySelection>('all');
  const [activeSection, setActiveSection] = useState<SkillSourceFilter>('all');
  const [marketVisible, setMarketVisible] = useState(false);
  const [skillPage, setSkillPage] = useState(1);
  const [pendingSkill, setPendingSkill] = useState<string | null>(null);
  const [pendingSource, setPendingSource] = useState<string | null>(null);
  const [githubVisible, setGithubVisible] = useState(false);
  const [githubRepo, setGithubRepo] = useState('');
  const [createVisible, setCreateVisible] = useState(false);
  const [detail, setDetail] = useState<{
    skill: SkillModalItem;
    draft: boolean;
    editable: boolean;
  } | null>(null);

  const fetchData = useCallback(async () => {
    const generation = ++loadGenerationRef.current;
    setLoading(true);
    setLoadError(false);
    setSkillUsage(null);
    const [skillsResult, draftsResult, sourcesResult, usageResult] = await Promise.allSettled([
      loadAvailableSkillsWithSynonBiomed<SkillInfo>(),
      loadSynonBiomedSkillDrafts(),
      loadSynonBiomedSkillSources(),
      loadSynonBiomedSkillUsage(),
    ]);
    if (generation !== loadGenerationRef.current) return;

    if (skillsResult.status === 'fulfilled') setAvailableSkills(skillsResult.value);
    if (draftsResult.status === 'fulfilled') setDrafts(draftsResult.value);
    if (sourcesResult.status === 'fulfilled') setSources(sourcesResult.value);
    if (usageResult.status === 'fulfilled') setSkillUsage(usageResult.value);

    const failures = [skillsResult, draftsResult, sourcesResult, usageResult].filter(
      (result) => result.status === 'rejected'
    );
    if (failures.length > 0) {
      for (const failure of failures) {
        if (failure.status === 'rejected') console.error('Failed to fetch Synon Biomed skill library:', failure.reason);
      }
      setLoadError(true);
      messageRef.current.error(translationRef.current('settings.skillsSettings.fetchFailed'));
    }
    setLoading(false);
  }, []);

  useEffect(() => {
    void fetchData();
    return () => {
      loadGenerationRef.current += 1;
    };
  }, [fetchData]);

  const importedNames = useMemo(() => new Set(sources.flatMap((source) => source.skills)), [sources]);
  const importedSkillKeys = useMemo(
    () => new Set(sources.flatMap((source) => source.skills.map((name) => `${source.repo}::${name}`))),
    [sources]
  );
  const sectionSkills = useMemo(() => {
    const groups: Record<SkillSourceFilter, SkillInfo[]> = {
      all: availableSkills,
      recommended: [],
      imported: [],
      personal: [],
    };
    for (const skill of availableSkills) {
      const source = (skill.source || '').toLowerCase();
      if (source === 'personal' || source === 'custom' || source === 'user') groups.personal.push(skill);
      else if (importedNames.has(skill.name) || source === 'marketplace' || source === 'github')
        groups.imported.push(skill);
      else groups.recommended.push(skill);
    }
    return groups;
  }, [availableSkills, importedNames]);

  const filteredSkills = useMemo(
    () => filterSkills(sectionSkills[activeSection], searchQuery, filter, i18n.language, categoryFilter),
    [activeSection, categoryFilter, filter, i18n.language, searchQuery, sectionSkills]
  );
  const categoryOptions = useMemo(
    () => getSynonBiomedSkillCategoryOptions([...availableSkills, ...drafts], i18n.language),
    [i18n.language, availableSkills, drafts]
  );
  const filteredDrafts = useMemo(
    () =>
      (activeSection === 'all' || activeSection === 'personal') &&
      filter === 'all' &&
      (categoryFilter === 'all' || categoryFilter === 'uncategorized')
        ? filterDrafts(drafts, searchQuery)
        : [],
    [activeSection, categoryFilter, drafts, filter, searchQuery]
  );
  const skillTotalPages = Math.max(1, Math.ceil(filteredSkills.length / SKILLS_PAGE_SIZE));
  const visibleSkills = useMemo(
    () => filteredSkills.slice((skillPage - 1) * SKILLS_PAGE_SIZE, skillPage * SKILLS_PAGE_SIZE),
    [filteredSkills, skillPage]
  );

  useEffect(() => {
    setSkillPage(1);
  }, [activeSection, categoryFilter, filter, searchQuery]);

  useEffect(() => {
    if (skillPage > skillTotalPages) setSkillPage(skillTotalPages);
  }, [skillPage, skillTotalPages]);

  const updateSkillEnabled = useCallback(
    async (skill: SkillInfo, enabled: boolean) => {
      if (pendingSkill) return;
      setPendingSkill(skill.name);
      setAvailableSkills((current) => current.map((item) => (item.name === skill.name ? { ...item, enabled } : item)));
      try {
        await setSynonBiomedSkillEnabled(skill.name, enabled);
        messageRef.current.success(
          enabled
            ? translationRef.current('settings.skillsSettings.enabledMessage')
            : translationRef.current('settings.skillsSettings.disabledMessage')
        );
      } catch (error) {
        console.error('Failed to update skill state:', error);
        setAvailableSkills((current) =>
          current.map((item) => (item.name === skill.name ? { ...item, enabled: skill.enabled !== false } : item))
        );
        messageRef.current.error(translationRef.current('settings.skillsSettings.updateFailed'));
      } finally {
        setPendingSkill(null);
      }
    },
    [pendingSkill]
  );

  const openSkill = useCallback((skill: SkillInfo) => {
    const source = (skill.source || '').toLowerCase();
    setDetail({
      skill: toModalItem(skill),
      draft: false,
      editable: source === 'personal' || source === 'custom' || source === 'user',
    });
  }, []);

  const openDraft = useCallback((draft: SynonBiomedSkillDraft) => {
    setDetail({
      draft: true,
      editable: true,
      skill: {
        name: draft.name,
        displayName: draft.displayName,
        description: draft.description,
        source: 'personal-draft',
      },
    });
  }, []);

  const revealPersonalSkills = useCallback(async () => {
    setActiveSection('personal');
    setSearchQuery('');
    setCategoryFilter('all');
    setFilter('all');
    await fetchData();
  }, [fetchData]);

  const importFile = useCallback(
    async (event: React.ChangeEvent<HTMLInputElement>) => {
      const file = event.target.files?.[0];
      event.target.value = '';
      if (!file || importingFile) return;
      setImportingFile(true);
      try {
        await importSynonBiomedSkillFile(file, undefined);
        messageRef.current.success(translationRef.current('settings.skillsSettings.fileImported', { name: file.name }));
        await revealPersonalSkills();
      } catch (error) {
        console.error('Failed to import skill file:', error);
        messageRef.current.error(translationRef.current('settings.skillsSettings.fileImportFailed'));
      } finally {
        setImportingFile(false);
      }
    },
    [importingFile, revealPersonalSkills]
  );

  const removePersonalSkill = useCallback(
    (skill: SkillInfo) => {
      Modal.confirm({
        title: translationRef.current('settings.skillsSettings.deletePersonalTitle'),
        content: translationRef.current('settings.skillsSettings.deletePersonalBody', {
          name: skill.displayName || skill.name,
        }),
        okButtonProps: { status: 'danger' },
        onOk: async () => {
          try {
            await deleteSynonBiomedPersonalSkill(skill.name);
            messageRef.current.success(translationRef.current('settings.skillsSettings.personalDeleted'));
            await fetchData();
          } catch (error) {
            console.error('Failed to delete personal skill:', error);
            messageRef.current.error(translationRef.current('settings.skillsSettings.deletePersonalFailed'));
          }
        },
      });
    },
    [fetchData]
  );

  const removeSource = useCallback(
    (source: SynonBiomedSkillSource) => {
      Modal.confirm({
        title: translationRef.current('settings.skillsSettings.removeSourceTitle'),
        content: translationRef.current('settings.skillsSettings.removeSourceBody', {
          name: source.slug,
        }),
        okButtonProps: { status: 'danger' },
        onOk: async () => {
          setPendingSource(source.slug);
          try {
            await removeSynonBiomedSkillSource(source.slug);
            messageRef.current.success(translationRef.current('settings.skillsSettings.sourceRemoved'));
            await fetchData();
          } catch (error) {
            console.error('Failed to remove skill source:', error);
            messageRef.current.error(translationRef.current('settings.skillsSettings.sourceRemoveFailed'));
          } finally {
            setPendingSource(null);
          }
        },
      });
    },
    [fetchData]
  );

  const addMenu = (
    <Menu
      onClickMenuItem={(key) => {
        if (key === 'marketplace') setMarketVisible(true);
        else if (key === 'github') {
          setGithubRepo('');
          setGithubVisible(true);
        } else if (key === 'file') fileInputRef.current?.click();
        else setCreateVisible(true);
      }}
    >
      <Menu.Item key='marketplace'>{t('settings.skillsSettings.marketplace.tab')}</Menu.Item>
      <Menu.Item key='github'>
        <span className='flex items-center gap-8px'>
          <Github size={15} /> {t('settings.skillsSettings.importGithub')}
        </span>
      </Menu.Item>
      <Menu.Item key='file'>
        <span className='flex items-center gap-8px'>
          <FileZip size={15} />
          {t('settings.skillsSettings.importFile')}
        </span>
      </Menu.Item>
      <Menu.Item key='create'>
        <span>{t('settings.skillsSettings.createPersonal')}</span>
      </Menu.Item>
    </Menu>
  );

  const headerActions = (
    <>
      <Dropdown droplist={addMenu} trigger='click' position='br'>
        <Button type='primary' data-testid='add-skill-button' loading={importingFile} disabled={importingFile}>
          {t('settings.skillsSettings.addSkill')}
        </Button>
      </Dropdown>
      <input
        ref={fileInputRef}
        className='hidden'
        type='file'
        accept='.zip,.skill,.md'
        disabled={importingFile}
        onChange={(event) => void importFile(event)}
      />
    </>
  );

  const list =
    loading && availableSkills.length === 0 ? (
      <div className='flex min-h-180px items-center justify-center' data-testid='synon-biomed-skills-loading'>
        <Spin />
      </div>
    ) : (
      <div className='min-h-0'>
        {activeSection === 'imported' ? (
          <ImportedSources
            sources={sources}
            pendingSource={pendingSource}
            onUpdate={(source) => {
              setGithubRepo(source.repo);
              setGithubVisible(true);
            }}
            onRemove={removeSource}
          />
        ) : null}
        {filteredDrafts.length > 0 ? (
          <section className='mb-18px' data-testid='personal-skill-drafts'>
            <SectionLabel
              title={t('settings.skillsSettings.drafts')}
              description={t('settings.skillsSettings.draftsDescription')}
              count={filteredDrafts.length}
            />
            <div className='settings-entity-grid' data-testid='personal-skill-draft-grid'>
              {filteredDrafts.map((draft) => (
                <SkillDraftRow key={draft.name} draft={draft} onOpen={openDraft} />
              ))}
            </div>
          </section>
        ) : null}
        {filteredSkills.length > 0 ? (
          <div className='settings-entity-grid' data-testid='synon-biomed-skill-grid' role='list'>
            {visibleSkills.map((skill) => (
              <SkillRow
                key={skill.name}
                skill={skill}
                pendingSkill={pendingSkill}
                personal={sectionSkills.personal.includes(skill)}
                usage={skillUsage ? findSynonBiomedSkillUsage(skillUsage, skill.name) : null}
                usageAvailable={skillUsage !== null}
                onOpen={openSkill}
                onToggle={updateSkillEnabled}
                onRemove={removePersonalSkill}
              />
            ))}
          </div>
        ) : filteredDrafts.length === 0 ? (
          <div className='border-y border-dashed border-arco-2 py-48px text-center text-13px text-t-secondary'>
            {searchQuery.trim() || categoryFilter !== 'all' || filter !== 'all'
              ? t('settings.skillsSettings.noMatches')
              : activeSection === 'imported'
                ? t('settings.skillsSettings.noImported')
                : activeSection === 'personal'
                  ? t('settings.skillsSettings.noPersonal')
                  : t('settings.skillsSettings.noSkills')}
          </div>
        ) : null}
      </div>
    );

  const mainContent = (
    <div className='settings-skills-page flex min-h-0 flex-col gap-24px'>
      {messageContext}
      <SettingsPageHeader
        data-testid='skills-header'
        title={
          <>
            {t('settings.skillsSettings.title')}{' '}
            <span className='settings-skill-library-total'>{availableSkills.length + drafts.length}</span>
          </>
        }
        actions={headerActions}
      />
      <SkillLibraryToolbar
        query={searchQuery}
        onQueryChange={setSearchQuery}
        category={categoryFilter}
        categories={categoryOptions}
        onCategoryChange={setCategoryFilter}
        source={activeSection}
        sourceCounts={{
          all: availableSkills.length + drafts.length,
          recommended: sectionSkills.recommended.length,
          imported: sectionSkills.imported.length,
          personal: sectionSkills.personal.length + drafts.length,
        }}
        onSourceChange={setActiveSection}
        status={filter}
        onStatusChange={setFilter}
      />
      {loadError ? (
        <div
          data-testid='synon-biomed-skills-load-error'
          role='status'
          className='settings-load-error-panel flex items-center gap-10px text-12px text-danger-6'
        >
          <span className='min-w-0 flex-1'>{t('settings.skillsSettings.partialLoadFailed')}</span>
          <Button size='mini' icon={<Refresh size={13} />} loading={loading} onClick={() => void fetchData()}>
            {t('common.retry')}
          </Button>
        </div>
      ) : null}
      <section data-testid='synon-biomed-skills-section' className='min-h-0'>
        <div>
          {list}
          {filteredSkills.length > SKILLS_PAGE_SIZE ? (
            <SettingsPagination
              page={skillPage}
              totalPages={skillTotalPages}
              onChange={setSkillPage}
              label={t('settings.skillsSettings.paginationLabel')}
            />
          ) : null}
        </div>
      </section>
      <SkillMarketModal
        visible={marketVisible}
        onClose={() => setMarketVisible(false)}
        importedSkillKeys={importedSkillKeys}
        onOpenRepository={(repo) => {
          setGithubRepo(repo);
          setGithubVisible(true);
        }}
        onSkillLoaded={fetchData}
      />
      <GitHubSkillImportModal
        visible={githubVisible}
        initialRepo={githubRepo}
        onClose={() => setGithubVisible(false)}
        onChanged={fetchData}
      />
      <CreatePersonalSkillModal
        visible={createVisible}
        initialDescription=''
        onClose={() => setCreateVisible(false)}
        onChanged={revealPersonalSkills}
      />
      <SkillDetailModal
        visible={detail !== null}
        skill={detail?.skill ?? null}
        draft={detail?.draft ?? false}
        editable={detail?.editable ?? false}
        onClose={() => setDetail(null)}
        onChanged={fetchData}
      />
    </div>
  );

  return withWrapper ? <SettingsPageWrapper>{mainContent}</SettingsPageWrapper> : mainContent;
};

function SkillRow({
  skill,
  pendingSkill,
  personal,
  usage,
  usageAvailable,
  onOpen,
  onToggle,
  onRemove,
}: {
  skill: SkillInfo;
  pendingSkill: string | null;
  personal: boolean;
  usage: SynonBiomedSkillUsage | null;
  usageAvailable: boolean;
  onOpen: (skill: SkillInfo) => void;
  onToggle: (skill: SkillInfo, enabled: boolean) => void | Promise<void>;
  onRemove: (skill: SkillInfo) => void;
}) {
  const { i18n, t } = useTranslation();
  const displayName = skill.displayName || skill.name;
  const metadata = [getSynonBiomedSkillCategoryLabel(skill.category, i18n.language), skill.license]
    .filter(Boolean)
    .join(' · ');
  const description = resolveSkillDescription(skill.name, skill.description, i18n.language, skill.description_i18n);
  return (
    <div
      data-testid={`synon-biomed-skill-row-${normalizeTestId(skill.name)}`}
      role='button'
      tabIndex={0}
      aria-label={t('settings.skillsSettings.viewNamed', { name: displayName })}
      className='settings-entity-card settings-skill-card'
      onClick={() => onOpen(skill)}
      onKeyDown={(event) => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault();
          onOpen(skill);
        }
      }}
    >
      <SettingsGeneratedArtwork id={resolveSkillArtwork(skill)} className='settings-skill-card__artwork' />
      <div className='settings-skill-card__content min-w-0 flex-1'>
        <div className='settings-skill-card__heading'>
          <span className='settings-skill-card__icon' aria-hidden='true'>
            <SettingsGeneratedIcon id={resolveSkillIcon(skill)} className='settings-skill-card__icon-image' />
          </span>
          <span className='settings-skill-card__heading-copy min-w-0 flex-1'>
            <span className='flex min-w-0 items-start justify-between gap-8px'>
              <span className='min-w-0 truncate text-14px font-medium text-t-primary'>{displayName}</span>
              {skillSourceLabel(skill.source, t) ? (
                <Tag size='small' color='gray' className='shrink-0'>
                  {skillSourceLabel(skill.source, t)}
                </Tag>
              ) : null}
            </span>
          </span>
        </div>
        <span
          className='mt-8px block line-clamp-3 min-h-54px text-12px leading-18px text-t-secondary'
          title={description}
        >
          {compactSettingsDescription(description, { maxLength: 150 })}
        </span>
        {metadata ? <span className='mt-8px block truncate text-11px text-t-quaternary'>{metadata}</span> : null}
      </div>
      <div
        className='flex min-w-0 items-center justify-between gap-8px border-t border-arco-2 pt-8px'
        onClick={(event) => event.stopPropagation()}
      >
        <SkillUsageSummary skillName={skill.name} usage={usage} available={usageAvailable} />
        <div className='flex shrink-0 items-center gap-4px'>
          {personal ? (
            <Button
              type='text'
              status='danger'
              size='small'
              aria-label={t('settings.skillsSettings.deleteNamed', { name: displayName })}
              onClick={() => onRemove(skill)}
            >
              {t('common.delete')}
            </Button>
          ) : null}
          <Switch
            size='small'
            checked={skill.enabled !== false}
            loading={pendingSkill === skill.name}
            disabled={pendingSkill !== null && pendingSkill !== skill.name}
            aria-label={t('settings.skillsSettings.enableNamed', { name: displayName })}
            onChange={(enabled) => void onToggle(skill, enabled)}
          />
        </div>
      </div>
    </div>
  );
}

function SkillUsageSummary({
  skillName,
  usage,
  available,
}: {
  skillName: string;
  usage: SynonBiomedSkillUsage | null;
  available: boolean;
}) {
  const { i18n, t } = useTranslation();
  const lastUsed = usage?.lastUsedAt
    ? t('settings.skillsSettings.usageLastUsed', {
        date: formatSkillUsageDate(usage.lastUsedAt, i18n.language),
      })
    : t('settings.skillsSettings.usageNever');

  return (
    <div
      data-testid={`synon-biomed-skill-usage-${normalizeTestId(skillName)}`}
      className='min-w-0 flex flex-col items-start gap-2px text-left text-11px text-t-tertiary'
      title={
        available
          ? `${lastUsed} · ${t('settings.skillsSettings.usageCount', { count: usage?.invocationCount ?? 0 })}`
          : t('settings.skillsSettings.usageUnavailable')
      }
    >
      <span className='max-w-full truncate'>
        {available ? lastUsed : t('settings.skillsSettings.usageUnavailable')}
      </span>
      <span className='tabular-nums'>
        {available ? t('settings.skillsSettings.usageCount', { count: usage?.invocationCount ?? 0 }) : '—'}
      </span>
    </div>
  );
}

function formatSkillUsageDate(value: string, language: string | undefined): string {
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return value;
  const locale = language?.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en-US';
  return new Intl.DateTimeFormat(locale, { dateStyle: 'short', timeStyle: 'short' }).format(new Date(timestamp));
}

function SkillDraftRow({
  draft,
  onOpen,
}: {
  draft: SynonBiomedSkillDraft;
  onOpen: (draft: SynonBiomedSkillDraft) => void;
}) {
  const { t } = useTranslation();
  return (
    <button
      type='button'
      data-testid={`synon-biomed-skill-draft-${normalizeTestId(draft.name)}`}
      className='settings-entity-card settings-skill-card settings-skill-card--draft'
      onClick={() => onOpen(draft)}
    >
      <SettingsGeneratedArtwork id='book' className='settings-skill-card__artwork' />
      <span className='settings-skill-card__icon' aria-hidden='true'>
        <SettingsGeneratedIcon id='skills' className='settings-skill-card__icon-image' />
      </span>
      <span className='min-w-0 flex-1'>
        <span className='flex items-start justify-between gap-8px'>
          <span className='min-w-0 truncate text-14px font-medium text-t-primary'>{draft.displayName}</span>
          <Tag size='small' color='orange'>
            {t('settings.skillsSettings.draft')}
          </Tag>
        </span>
        <span className='mt-8px block line-clamp-3 min-h-54px text-12px leading-18px text-t-secondary'>
          {compactSettingsDescription(draft.description || draft.name, { maxLength: 150 })}
        </span>
      </span>
    </button>
  );
}

function ImportedSources({
  sources,
  pendingSource,
  onUpdate,
  onRemove,
}: {
  sources: SynonBiomedSkillSource[];
  pendingSource: string | null;
  onUpdate: (source: SynonBiomedSkillSource) => void;
  onRemove: (source: SynonBiomedSkillSource) => void;
}) {
  const { t } = useTranslation();
  if (sources.length === 0) return null;
  return (
    <section className='mb-18px' data-testid='imported-skill-sources'>
      <SectionLabel
        title={t('settings.skillsSettings.sourceRepositories')}
        description={t('settings.skillsSettings.sourceRepositoriesDescription')}
        count={sources.length}
      />
      <div className='grid grid-cols-2 gap-10px max-md:grid-cols-1'>
        {sources.map((source) => (
          <div
            key={source.slug}
            className='flex min-w-0 items-center gap-10px border border-arco-2 rd-6px px-12px py-10px'
          >
            <Github size={18} className='shrink-0 text-t-secondary' />
            <div className='min-w-0 flex-1'>
              <div className='truncate text-13px font-medium text-t-primary'>{source.slug}</div>
              <div className='mt-2px truncate text-11px text-t-tertiary'>
                {source.sha ? `@${source.sha.slice(0, 12)}` : source.repo}
              </div>
            </div>
            <Button size='mini' loading={pendingSource === source.slug} onClick={() => onUpdate(source)}>
              {t('settings.skillsSettings.checkUpdates')}
            </Button>
            {source.removable ? (
              <Button size='mini' status='danger' disabled={pendingSource !== null} onClick={() => onRemove(source)}>
                {t('settings.skillsSettings.remove')}
              </Button>
            ) : null}
          </div>
        ))}
      </div>
    </section>
  );
}

function SectionLabel({ title, description, count }: { title: string; description: string; count: number }) {
  return (
    <div className='mb-10px flex items-end justify-between gap-12px'>
      <div>
        <div className='text-14px font-semibold text-t-primary'>{title}</div>
        <div className='mt-2px text-12px text-t-tertiary'>{description}</div>
      </div>
      <span className='text-12px tabular-nums text-t-tertiary'>{count}</span>
    </div>
  );
}

function filterSkills(
  skills: SkillInfo[],
  queryValue: string,
  filter: SkillStatusFilter,
  language?: string,
  categoryFilter: SynonBiomedSkillCategorySelection = 'all'
): SkillInfo[] {
  const query = queryValue.trim().toLowerCase();
  return skills.filter((skill) => {
    if (filter === 'enabled' && skill.enabled === false) return false;
    if (filter === 'disabled' && skill.enabled !== false) return false;
    const skillCategory = resolveSynonBiomedSkillCategory(skill);
    if (
      categoryFilter !== 'all' &&
      (categoryFilter === 'uncategorized' ? skillCategory !== null : skillCategory !== categoryFilter)
    )
      return false;
    if (!query) return true;
    const localizedDescription = resolveSkillDescription(
      skill.name,
      skill.description,
      language,
      skill.description_i18n
    );
    return [skill.name, skill.displayName, skill.description, localizedDescription, skill.category, skill.source]
      .filter((value): value is string => Boolean(value))
      .some((value) => value.toLowerCase().includes(query));
  });
}

function filterDrafts(drafts: SynonBiomedSkillDraft[], queryValue: string): SynonBiomedSkillDraft[] {
  const query = queryValue.trim().toLowerCase();
  return query
    ? drafts.filter((draft) =>
        [draft.name, draft.displayName, draft.description].some((value) => value.toLowerCase().includes(query))
      )
    : drafts;
}

function toModalItem(skill: SkillInfo): SkillModalItem {
  return {
    name: skill.name,
    displayName: skill.displayName || skill.name,
    description: skill.description,
    source: skill.source || 'bundled',
    license: skill.license,
    category: skill.category,
    attachedAgents: skill.attachedAgents,
    thirdParty: skill.thirdParty,
  };
}

export default SynonBiomedSkillsSettings;
