import { Button, Switch, Tag } from '@arco-design/web-react';
import React from 'react';
import { useTranslation } from 'react-i18next';
import { SettingsGeneratedIcon, type SettingsGeneratedIconId } from '../components/SettingsGeneratedAsset';
import {
  getSynonBiomedSkillCategoryLabel,
  resolveSynonBiomedSkillCategory,
} from '@/renderer/services/skills/synonBiomedSkillCategories';
import { resolveSkillDescription } from '@/renderer/services/skills/synonBiomedSkillDescriptions';
import type { SynonBiomedSkillDraft } from '@/renderer/services/skills/synonBiomedSkillLibrary';
import type { SynonBiomedSkillUsage } from '@/renderer/services/skills/synonBiomedSkillUsage';

export interface SkillInfo {
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

const normalizeTestId = (name: string): string => name.replace(/[:/\s<>"'|?*]/g, '-');

function resolveSkillIcon(skill: SkillInfo): SettingsGeneratedIconId {
  switch (resolveSynonBiomedSkillCategory(skill)) {
    case 'structural-biology':
      return 'connector-protein';
    case 'drug-discovery':
    case 'cmc-manufacturing':
      return 'connector-molecule';
    case 'omics-bioinformatics':
      return 'connector-dna';
    case 'clinical-regulatory':
    case 'dmpk-nonclinical':
      return 'connector-clipboard';
    case 'compute-platform':
      return 'skills';
    default:
      return 'connector-book';
  }
}

export function SkillRow({
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
  const categoryLabel = getSynonBiomedSkillCategoryLabel(skill.category, i18n.language);
  const description = resolveSkillDescription(skill.name, skill.description, i18n.language, skill.description_i18n);
  return (
    <div
      data-testid={`synon-biomed-skill-row-${normalizeTestId(skill.name)}`}
      role='button'
      tabIndex={0}
      aria-label={t('settings.skillsSettings.viewNamed', { name: displayName })}
      className='settings-entity-card settings-skill-card settings-library-card'
      onClick={() => onOpen(skill)}
      onKeyDown={(event) => {
        if (event.target === event.currentTarget && (event.key === 'Enter' || event.key === ' ')) {
          event.preventDefault();
          onOpen(skill);
        }
      }}
    >
      <div className='settings-skill-card__heading settings-library-card__heading'>
        <span className='settings-skill-card__icon settings-library-card__icon' aria-hidden='true'>
          <SettingsGeneratedIcon id={resolveSkillIcon(skill)} className='settings-skill-card__icon-image' />
        </span>
        <span className='settings-skill-card__title settings-library-card__title'>{displayName}</span>
      </div>
      <p className='settings-skill-card__description settings-library-card__description'>{description}</p>
      <div
        className='settings-skill-card__footer settings-library-card__footer'
        onClick={(event) => event.stopPropagation()}
      >
        <span className='settings-skill-card__meta settings-library-card__meta'>
          {categoryLabel ? <span className='settings-skill-card__category'>{categoryLabel}</span> : null}
          <SkillUsageSummary skillName={skill.name} usage={usage} available={usageAvailable} />
        </span>
        <span className='settings-library-card__control'>
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
            checked={skill.enabled !== false}
            loading={pendingSkill === skill.name}
            disabled={pendingSkill !== null && pendingSkill !== skill.name}
            aria-label={t('settings.skillsSettings.enableNamed', { name: displayName })}
            onChange={(enabled) => void onToggle(skill, enabled)}
          />
        </span>
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
  const count = t('settings.skillsSettings.usageCount', { count: usage?.invocationCount ?? 0 });

  return (
    <span
      data-testid={`synon-biomed-skill-usage-${normalizeTestId(skillName)}`}
      className='settings-skill-card__usage'
      title={available ? `${lastUsed} · ${count}` : t('settings.skillsSettings.usageUnavailable')}
    >
      {available ? `${lastUsed} · ${count}` : t('settings.skillsSettings.usageUnavailable')}
    </span>
  );
}

function formatSkillUsageDate(value: string, language: string | undefined): string {
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return value;
  const locale = language?.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en-US';
  return new Intl.DateTimeFormat(locale, { dateStyle: 'short', timeStyle: 'short' }).format(new Date(timestamp));
}

export function SkillDraftRow({
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
      className='settings-entity-card settings-skill-card settings-skill-card--draft settings-library-card'
      onClick={() => onOpen(draft)}
    >
      <span className='settings-skill-card__heading settings-library-card__heading'>
        <span className='settings-skill-card__icon settings-library-card__icon' aria-hidden='true'>
          <SettingsGeneratedIcon id='skills' className='settings-skill-card__icon-image' />
        </span>
        <span className='settings-skill-card__title settings-library-card__title'>{draft.displayName}</span>
      </span>
      <span className='settings-skill-card__description settings-library-card__description'>
        {draft.description || draft.name}
      </span>
      <span className='settings-skill-card__footer settings-library-card__footer'>
        <span className='settings-skill-card__meta settings-library-card__meta'>
          <Tag size='small' color='orange'>
            {t('settings.skillsSettings.draft')}
          </Tag>
        </span>
      </span>
    </button>
  );
}
