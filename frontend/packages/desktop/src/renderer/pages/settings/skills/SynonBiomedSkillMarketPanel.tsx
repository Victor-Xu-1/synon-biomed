import { Button, Message, Spin, Tag } from '@arco-design/web-react';
import { Github, Link, Refresh } from '@icon-park/react';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  importSynonBiomedRepositorySkills,
  previewSynonBiomedSkillRepository,
  type SynonBiomedSkillRepoEntry,
  type SynonBiomedSkillRepoPreview,
} from '@/renderer/services/skills/synonBiomedSkillLibrary';
import { getSkillMarketplaceCopy } from './skillMarketplaceCopy';

export type SkillMarketSource = {
  id: string;
  repo: string;
  title: string;
  titleZh: string;
  description: string;
  descriptionZh: string;
  category: string;
  official?: boolean;
};

export const SKILL_MARKET_SOURCES: SkillMarketSource[] = [
  {
    id: 'anthropics-skills',
    repo: 'https://github.com/anthropics/skills',
    title: 'Anthropic Skills',
    titleZh: 'Anthropic 技能库',
    description: 'Official Agent Skills examples and reusable workflows.',
    descriptionZh: 'Anthropic 官方提供的 Agent Skill 示例与可复用工作流。',
    category: 'official',
    official: true,
  },
  {
    id: 'bioconductor-ai-agent-skills',
    repo: 'https://github.com/Bioconductor/ai-agent-skills',
    title: 'Bioconductor AI Agent Skills',
    titleZh: 'Bioconductor 生物医药技能库',
    description: 'Bioconductor-curated workflows for R packages, bioinformatics, and reproducible analysis.',
    descriptionZh: '面向 R/Bioconductor、基因组学与可复现生物信息分析的技能。',
    category: 'biomedical',
    official: true,
  },
];

type MarketTopic = 'all' | 'biomedical' | 'science' | 'general';
type SourceStatus = 'loading' | 'ready' | 'error';

type SkillMarketCatalog = {
  source: SkillMarketSource;
  status: SourceStatus;
  preview: SynonBiomedSkillRepoPreview | null;
};

type MarketSkill = SynonBiomedSkillRepoEntry & {
  key: string;
  source: SkillMarketSource;
  preview: SynonBiomedSkillRepoPreview;
  topic: Exclude<MarketTopic, 'all'>;
};

type Props = {
  query?: string;
  importedSkillKeys?: ReadonlySet<string>;
  onOpenRepository: (repo: string) => void;
  onOpenCustomRepository: () => void;
  onSkillLoaded?: () => void | Promise<void>;
};

export const SynonBiomedSkillMarketPanel: React.FC<Props> = ({
  query = '',
  importedSkillKeys,
  onOpenRepository,
  onOpenCustomRepository,
  onSkillLoaded,
}) => {
  const { t } = useTranslation();
  const [message, contextHolder] = Message.useMessage({ maxCount: 3 });
  const [sourceFilter, setSourceFilter] = useState('bioconductor-ai-agent-skills');
  const [topicFilter, setTopicFilter] = useState<MarketTopic>('biomedical');
  const [loadingSkill, setLoadingSkill] = useState<string | null>(null);
  const [loadedSkillKeys, setLoadedSkillKeys] = useState<Set<string>>(() => new Set());
  const [catalogs, setCatalogs] = useState<SkillMarketCatalog[]>(createLoadingCatalogs);
  const loadGeneration = useRef(0);
  const catalogLoadInFlight = useRef(false);

  const loadCatalogs = useCallback(async () => {
    if (catalogLoadInFlight.current) return;
    catalogLoadInFlight.current = true;
    const generation = ++loadGeneration.current;
    setCatalogs(createLoadingCatalogs());
    try {
      await Promise.all(
        SKILL_MARKET_SOURCES.map(async (source) => {
          try {
            const preview = await previewSynonBiomedSkillRepositoryWithTimeout(source.repo);
            if (generation !== loadGeneration.current) return;
            setCatalogs((current) =>
              current.map((catalog) =>
                catalog.source.id === source.id ? { source, status: 'ready', preview } : catalog
              )
            );
          } catch (error) {
            console.error(`Failed to read Skill market source ${source.repo}:`, error);
            if (generation !== loadGeneration.current) return;
            setCatalogs((current) =>
              current.map((catalog) =>
                catalog.source.id === source.id ? { source, status: 'error', preview: null } : catalog
              )
            );
          }
        })
      );
    } finally {
      catalogLoadInFlight.current = false;
    }
  }, []);

  useEffect(() => {
    void loadCatalogs();
    return () => {
      loadGeneration.current += 1;
    };
  }, [loadCatalogs]);

  const marketSkills = useMemo<MarketSkill[]>(
    () =>
      catalogs.flatMap((catalog) => {
        if (!catalog.preview) return [];
        return catalog.preview.skills.map((skill) => ({
          ...skill,
          key: skillKey(catalog.source, skill.name),
          source: catalog.source,
          preview: catalog.preview as SynonBiomedSkillRepoPreview,
          topic: classifyTopic(catalog.source, skill),
        }));
      }),
    [catalogs]
  );

  const filteredSkills = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase();
    return marketSkills.filter((skill) => {
      if (sourceFilter !== 'all' && skill.source.id !== sourceFilter) return false;
      if (topicFilter !== 'all' && skill.topic !== topicFilter) return false;
      if (!normalizedQuery) return true;
      return [
        skill.name,
        skill.displayName,
        skill.description,
        skill.path,
        skill.source.title,
        skill.source.repo,
        skill.preview.license,
      ]
        .filter((value): value is string => Boolean(value))
        .some((value) => value.toLowerCase().includes(normalizedQuery));
    });
  }, [marketSkills, query, sourceFilter, topicFilter]);

  const sourceCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const skill of marketSkills) counts.set(skill.source.id, (counts.get(skill.source.id) ?? 0) + 1);
    return counts;
  }, [marketSkills]);

  const loadSkill = useCallback(
    async (skill: MarketSkill) => {
      if (loadingSkill) return;
      setLoadingSkill(skill.key);
      try {
        const result = await importSynonBiomedRepositorySkills(skill.preview, [skill.name]);
        if (result.imported.includes(skill.name)) {
          setLoadedSkillKeys((current) => new Set(current).add(skill.key));
          message.success(t('settings.skillsSettings.marketplace.skillLoaded'));
          try {
            await onSkillLoaded?.();
          } catch (error) {
            console.error('Failed to refresh installed Skills after market import:', error);
          }
        } else {
          message.warning(
            t('settings.skillsSettings.marketplace.skillLoadSkipped', {
              name: skill.displayName || skill.name,
            })
          );
        }
      } catch (error) {
        console.error('Failed to import one marketplace Skill:', error);
        message.error(t('settings.skillsSettings.marketplace.skillLoadFailed'));
      } finally {
        setLoadingSkill(null);
      }
    },
    [loadingSkill, message, onSkillLoaded, t]
  );

  const isLoading = catalogs.some((catalog) => catalog.status === 'loading');
  const hasError = catalogs.some((catalog) => catalog.status === 'error');
  const isLoaded = (skill: MarketSkill): boolean =>
    loadedSkillKeys.has(skill.key) || Boolean(importedSkillKeys?.has(skill.key));

  return (
    <div className='flex flex-col gap-12px' data-testid='synon-biomed-skill-market'>
      {contextHolder}
      <section className='border border-arco-2 bg-fill-1 rd-9px px-12px py-10px'>
        <div className='flex min-w-0 items-center gap-9px'>
          <span className='flex size-30px shrink-0 items-center justify-center rounded-7px bg-fill-2 text-t-secondary'>
            <Link size={15} />
          </span>
          <div className='min-w-0 flex-1'>
            <div className='flex flex-wrap items-center gap-8px'>
              <div className='text-14px font-650 leading-5 text-t-primary'>
                {t('settings.skillsSettings.marketplace.title')}
              </div>
              <Tag size='small' color='arcoblue'>
                {t('settings.skillsSettings.marketplace.pinnedImport')}
              </Tag>
            </div>
            <div className='mt-2px max-w-800px text-11px leading-4 text-t-secondary'>
              {t('settings.skillsSettings.marketplace.description')}
            </div>
          </div>
          <Button
            className='shrink-0'
            size='small'
            icon={<Refresh size={14} />}
            loading={isLoading}
            onClick={() => void loadCatalogs()}
            aria-label={t('settings.skillsSettings.marketplace.refresh')}
          >
            {t('settings.skillsSettings.marketplace.refresh')}
          </Button>
        </div>
        <div className='mt-8px border-l-2 border-warning-4 bg-warning-1 px-10px py-6px text-11px leading-4 text-t-secondary'>
          {t('settings.skillsSettings.marketplace.security')}
        </div>
      </section>

      <section
        data-testid='synon-biomed-skill-market-sources'
        className='border border-arco-2 bg-fill-1 rd-9px px-10px py-10px'
      >
        <div className='flex flex-wrap items-start justify-between gap-10px'>
          <div className='min-w-0'>
            <div className='text-14px font-650 leading-5 text-t-primary'>
              {t('settings.skillsSettings.marketplace.curatedTitle')}
            </div>
            <p className='m-0 mt-2px max-w-800px text-11px leading-4 text-t-secondary'>
              {t('settings.skillsSettings.marketplace.curatedDescription')}
            </p>
          </div>
          <Tag size='small' color='green'>
            {topicLabel(topicFilter, t)}
          </Tag>
        </div>

        <div
          className='mt-8px grid grid-cols-1 gap-8px lg:grid-cols-3'
          role='group'
          aria-label={t('settings.skillsSettings.marketplace.sourceFilter')}
        >
          <SourceFilterCard
            active={sourceFilter === 'all'}
            titleZh={t('settings.skillsSettings.marketplace.allSources')}
            titleEn={t('settings.skillsSettings.marketplace.allSourcesEnglish')}
            descriptionZh={t('settings.skillsSettings.marketplace.allSourcesDescription')}
            count={marketSkills.length}
            onClick={() => setSourceFilter('all')}
            testId='synon-biomed-skill-market-source-all'
          />
          {catalogs.map((catalog) => (
            <SourceFilterCard
              key={catalog.source.id}
              active={sourceFilter === catalog.source.id}
              titleZh={catalog.source.titleZh}
              titleEn={catalog.source.title}
              descriptionZh={catalog.source.descriptionZh}
              count={sourceCounts.get(catalog.source.id) ?? 0}
              status={catalog.status}
              statusLabel={
                catalog.status === 'loading'
                  ? t('settings.skillsSettings.marketplace.sourceLoading')
                  : catalog.status === 'error'
                    ? t('settings.skillsSettings.marketplace.sourceUnavailable')
                    : undefined
              }
              onClick={() => setSourceFilter(catalog.source.id)}
              onBrowse={() => onOpenRepository(catalog.source.repo)}
              repository={catalog.source.repo}
              testId={`synon-biomed-skill-market-source-${catalog.source.id}`}
              browseLabel={t('settings.skillsSettings.marketplace.browseAndInstall')}
              openLabel={t('settings.skillsSettings.marketplace.openSource')}
            />
          ))}
        </div>
      </section>

      <section
        data-testid='synon-biomed-skill-market-results'
        className='border border-arco-2 bg-fill-1 rd-9px px-10px py-10px'
      >
        <div className='flex flex-wrap items-start justify-between gap-10px'>
          <div className='min-w-0'>
            <div className='text-14px font-650 leading-5 text-t-primary'>
              {t('settings.skillsSettings.marketplace.biomedicalTitle')}
            </div>
            <p className='m-0 mt-2px max-w-700px text-11px leading-4 text-t-secondary'>
              {t('settings.skillsSettings.marketplace.biomedicalDescription')}
            </p>
          </div>
          <div
            className='flex flex-wrap justify-end gap-6px'
            role='group'
            aria-label={t('settings.skillsSettings.marketplace.topicFilter')}
          >
            {(['biomedical', 'all', 'science', 'general'] as MarketTopic[]).map((topic) => (
              <Button
                key={topic}
                size='mini'
                type={topicFilter === topic ? 'primary' : 'secondary'}
                onClick={() => setTopicFilter(topic)}
                data-testid={`synon-biomed-skill-market-topic-${topic}`}
              >
                {topicLabel(topic, t)}
              </Button>
            ))}
          </div>
        </div>

        <div className='mt-8px flex flex-wrap items-center justify-between gap-8px border-t border-arco-2 pt-8px'>
          <Tag size='small' color={topicFilter === 'biomedical' ? 'green' : 'arcoblue'}>
            {topicLabel(topicFilter, t)}
          </Tag>
          <div className='text-12px text-t-tertiary'>
            {t('settings.skillsSettings.marketplace.resultCount', { count: filteredSkills.length })}
          </div>
        </div>

        {isLoading && marketSkills.length === 0 ? (
          <div className='mt-12px flex min-h-180px flex-col items-center justify-center gap-8px border border-dashed border-arco-2 bg-fill-1 rd-8px text-12px text-t-secondary'>
            <Spin />
            <span>{t('settings.skillsSettings.marketplace.loadingSkills')}</span>
          </div>
        ) : null}

        {hasError ? (
          <div className='mt-12px flex items-center gap-8px border-l-2 border-warning-4 bg-warning-1 px-10px py-8px text-12px text-t-secondary'>
            <span className='min-w-0 flex-1'>{t('settings.skillsSettings.marketplace.partialLoadFailed')}</span>
            <Button size='mini' onClick={() => void loadCatalogs()}>
              {t('common.retry')}
            </Button>
          </div>
        ) : null}

        {filteredSkills.length > 0 ? (
          <div
            className='mt-10px grid grid-cols-1 items-stretch gap-8px sm:grid-cols-2 xl:grid-cols-4 2xl:grid-cols-5'
            role='list'
            data-testid='synon-biomed-skill-market-grid'
          >
            {filteredSkills.map((skill) => (
              <SkillMarketCard
                key={skill.key}
                skill={skill}
                loaded={isLoaded(skill)}
                loading={loadingSkill === skill.key}
                onLoad={() => void loadSkill(skill)}
                t={t}
              />
            ))}
          </div>
        ) : !isLoading ? (
          <div className='mt-12px border border-dashed border-arco-2 bg-fill-1 rd-8px py-42px text-center text-13px text-t-secondary'>
            {t('settings.skillsSettings.marketplace.noMatches')}
          </div>
        ) : null}
      </section>

      <section className='flex flex-wrap items-center justify-between gap-8px border border-arco-2 bg-fill-1 rd-9px px-10px py-8px'>
        <div className='min-w-0'>
          <div className='text-13px font-650 text-t-primary'>
            {t('settings.skillsSettings.marketplace.moreSourcesTitle')}
          </div>
          <p className='m-0 mt-2px text-11px leading-4 text-t-secondary'>
            {t('settings.skillsSettings.marketplace.moreSourcesDescription')}
          </p>
        </div>
        <div className='flex flex-wrap gap-8px'>
          <a
            className='text-12px text-t-secondary underline underline-offset-3px'
            href='https://github.com/search?q=SKILL.md+%28bioinformatics+OR+chemistry+OR+drug+discovery%29&type=code'
            target='_blank'
            rel='noreferrer'
          >
            {t('settings.skillsSettings.marketplace.searchGithub')}
          </a>
          <Button size='small' onClick={onOpenCustomRepository}>
            {t('settings.skillsSettings.marketplace.customRepository')}
          </Button>
        </div>
      </section>
    </div>
  );
};

function createLoadingCatalogs(): SkillMarketCatalog[] {
  return SKILL_MARKET_SOURCES.map<SkillMarketCatalog>((source) => ({ source, status: 'loading', preview: null }));
}

const SKILL_MARKET_PREVIEW_TIMEOUT_MS = 12_000;

async function previewSynonBiomedSkillRepositoryWithTimeout(repo: string): Promise<SynonBiomedSkillRepoPreview> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      previewSynonBiomedSkillRepository(repo),
      new Promise<never>((_, reject) => {
        timer = setTimeout(
          () => reject(new Error(`Skill market preview timed out after ${SKILL_MARKET_PREVIEW_TIMEOUT_MS}ms`)),
          SKILL_MARKET_PREVIEW_TIMEOUT_MS
        );
      }),
    ]);
  } finally {
    if (timer) clearTimeout(timer);
  }
}

function skillKey(source: SkillMarketSource, name: string): string {
  return `${source.repo}::${name}`;
}

function classifyTopic(source: SkillMarketSource, skill: SynonBiomedSkillRepoEntry): Exclude<MarketTopic, 'all'> {
  const text =
    `${source.title} ${source.description} ${skill.name} ${skill.displayName} ${skill.description}`.toLowerCase();
  const biomedicalPattern =
    /\b(?:bio(?:informatics|medical)?|gene(?:s|omics)?|genom(?:e|ics)|protein|clinical|drug|medic(?:al|ine)?|chem(?:istry|ical)?|molecule|molecular|pdb|admet|assay|cell(?:s|ular)?)\b/;
  const sciencePattern = /\b(?:science|scientific|numerical|research|data|model|compute|simulation)\b/;
  if (source.category === 'biomedical' || biomedicalPattern.test(text)) {
    return 'biomedical';
  }
  if (sciencePattern.test(text) || source.category === 'science') {
    return 'science';
  }
  return 'general';
}

function topicLabel(topic: MarketTopic, t: ReturnType<typeof useTranslation>['t']): string {
  if (topic === 'biomedical') return t('settings.skillsSettings.marketplace.biomedicalTopic');
  if (topic === 'science') return t('settings.skillsSettings.marketplace.scienceTopic');
  if (topic === 'general') return t('settings.skillsSettings.marketplace.generalTopic');
  return t('settings.skillsSettings.marketplace.allTopics');
}

function SourceFilterCard({
  active,
  titleZh,
  titleEn,
  descriptionZh,
  count,
  status,
  statusLabel,
  onClick,
  onBrowse,
  repository,
  browseLabel,
  openLabel,
  testId,
}: {
  active: boolean;
  titleZh: string;
  titleEn: string;
  descriptionZh: string;
  count: number;
  status?: SourceStatus;
  statusLabel?: string;
  onClick: () => void;
  onBrowse?: () => void;
  repository?: string;
  browseLabel?: string;
  openLabel?: string;
  testId: string;
}) {
  const displayLabel = statusLabel || `${count} 个`;
  return (
    <div
      className={`flex min-w-0 flex-col overflow-hidden border rd-9px ${active ? 'border-primary-4' : 'border-arco-2'}`}
    >
      <button
        type='button'
        className={`flex min-h-92px flex-1 flex-col px-9px py-8px text-left transition-colors ${
          active ? 'bg-primary-1' : 'bg-fill-1 hover:bg-fill-2'
        }`}
        onClick={onClick}
        aria-pressed={active}
        data-testid={testId}
      >
        <div className='flex min-w-0 items-start justify-between gap-10px'>
          <div className='flex min-w-0 items-start gap-8px'>
            <span className='mt-1px flex size-24px shrink-0 items-center justify-center rounded-6px bg-fill-2 text-t-secondary'>
              <Github size={13} />
            </span>
            <div className='min-w-0'>
              <div className='break-words text-13px font-650 leading-5 text-t-primary'>{titleZh}</div>
              <div className='mt-2px break-words text-11px leading-4 text-t-tertiary'>{titleEn}</div>
            </div>
          </div>
          <span className='shrink-0 rounded-5px bg-fill-2 px-6px py-3px text-11px text-t-secondary'>
            {displayLabel}
          </span>
        </div>
        <div className='mt-8px line-clamp-1 break-words text-11px leading-4 text-t-secondary'>{descriptionZh}</div>
      </button>
      {repository && onBrowse && browseLabel && openLabel ? (
        <div className='flex flex-wrap items-center justify-between gap-6px border-t border-arco-2 px-9px py-6px'>
          <Button size='mini' type='text' disabled={status === 'loading'} onClick={onBrowse}>
            {browseLabel}
          </Button>
          <a
            className='break-words text-11px text-t-secondary underline underline-offset-3px'
            href={repository}
            target='_blank'
            rel='noreferrer'
          >
            {openLabel}
          </a>
        </div>
      ) : null}
    </div>
  );
}

function SkillMarketCard({
  skill,
  loaded,
  loading,
  onLoad,
  t,
}: {
  skill: MarketSkill;
  loaded: boolean;
  loading: boolean;
  onLoad: () => void;
  t: ReturnType<typeof useTranslation>['t'];
}) {
  const topicColor = skill.topic === 'biomedical' ? 'green' : skill.topic === 'science' ? 'arcoblue' : 'gray';
  const copy = getSkillMarketplaceCopy(skill.source.id, skill.name, skill.description);
  const englishName = skill.displayName && skill.displayName !== skill.name ? skill.displayName : skill.name;
  const skillPath = displaySkillPath(skill.path);
  const license = skill.preview.license || t('settings.skillsSettings.marketplace.notProvided');
  return (
    <article
      role='listitem'
      data-testid={`synon-biomed-skill-market-card-${normalizeTestId(skill.key)}`}
      className='flex h-full min-h-220px min-w-0 flex-col border border-arco-2 bg-fill-1 rd-8px px-10px py-10px transition-colors hover:border-primary-4'
    >
      <div className='flex min-w-0 items-start justify-between gap-10px'>
        <div className='min-w-0 flex-1'>
          <div className='break-words text-13px font-650 leading-5 text-t-primary'>{copy.zhName}</div>
          <div className='mt-1px break-words text-10px leading-4 text-t-tertiary'>{englishName}</div>
          {englishName !== skill.name ? (
            <div className='mt-1px break-all text-10px leading-4 text-t-quaternary'>{skill.name}</div>
          ) : null}
        </div>
        <Tag size='small' color={topicColor} className='shrink-0'>
          {topicLabel(skill.topic, t)}
        </Tag>
      </div>

      <div className='mt-8px flex flex-col gap-5px'>
        <SkillDescriptionBlock
          label={t('settings.skillsSettings.marketplace.chineseDescription')}
          description={copy.zhDescription}
          accent
          compact
        />
        <SkillDescriptionBlock
          label={t('settings.skillsSettings.marketplace.englishDescription')}
          description={skill.description || t('settings.skillsSettings.marketplace.noDescription')}
          compact
        />
      </div>

      <div className='mt-8px flex min-w-0 items-center justify-between gap-6px border-t border-arco-2 pt-7px text-10px leading-4'>
        <span className='min-w-0 break-words text-t-secondary'>{skill.source.titleZh}</span>
        <span className='shrink-0 text-t-tertiary'>{license}</span>
      </div>

      <details className='mt-5px text-10px leading-4 text-t-tertiary'>
        <summary className='cursor-pointer'>{t('settings.skillsSettings.marketplace.viewDetails')}</summary>
        <div className='mt-5px grid gap-4px border-l-2 border-arco-2 pl-7px'>
          <div className='break-words'>
            {t('settings.skillsSettings.marketplace.sourceLabel')}：{skill.source.title}
          </div>
          {skillPath ? (
            <div className='break-all' title={skill.path ?? undefined}>
              {t('settings.skillsSettings.marketplace.pathLabel')}：{skillPath}
            </div>
          ) : null}
          <div className='break-all' title={skill.preview.sha}>
            {t('settings.skillsSettings.marketplace.commitLabel')}：
            {skill.preview.sha ? skill.preview.sha.slice(0, 12) : t('settings.skillsSettings.modals.github.missingSha')}
          </div>
        </div>
      </details>

      <div className='mt-auto flex flex-wrap items-center gap-6px pt-8px'>
        <a
          className='break-words px-4px text-10px text-t-secondary underline underline-offset-3px'
          href={skill.source.repo}
          target='_blank'
          rel='noreferrer'
        >
          {t('settings.skillsSettings.marketplace.openSource')}
        </a>
        <Button
          size='mini'
          className='!min-w-0 !flex-1'
          type={loaded ? 'secondary' : 'primary'}
          loading={loading}
          disabled={loaded}
          onClick={onLoad}
        >
          {loaded
            ? t('settings.skillsSettings.marketplace.loaded')
            : t('settings.skillsSettings.marketplace.loadSkill')}
        </Button>
      </div>
    </article>
  );
}

function SkillDescriptionBlock({
  label,
  description,
  accent = false,
  compact = false,
}: {
  label: string;
  description: string;
  accent?: boolean;
  compact?: boolean;
}) {
  return (
    <div
      className={`min-w-0 rounded-6px border-l-2 ${compact ? 'px-8px py-6px' : 'px-10px py-8px'} ${accent ? 'border-primary-4 bg-primary-1' : 'border-arco-2 bg-fill-2'}`}
    >
      <div className='text-10px font-600 uppercase tracking-0.3px text-t-quaternary'>{label}</div>
      <p
        className={`m-0 mt-2px break-words text-11px leading-4 text-t-secondary ${compact ? 'line-clamp-2' : ''}`}
        title={description}
      >
        {description}
      </p>
    </div>
  );
}

function normalizeTestId(value: string): string {
  return value.replace(/[^A-Za-z0-9_-]+/g, '-');
}

function displaySkillPath(path: string | null): string | null {
  if (!path) return null;
  const normalized = path.replace(/\\/g, '/');
  const skillsMarker = normalized.lastIndexOf('skills/');
  if (skillsMarker >= 0) return normalized.slice(skillsMarker);
  const parts = normalized.split('/').filter(Boolean);
  return parts.slice(-3).join('/') || null;
}
