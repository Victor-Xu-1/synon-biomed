/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Button, Input } from '@arco-design/web-react';
import { Down, Search } from '@icon-park/react';
import React, { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type {
  SynonBiomedSkillCategoryOption,
  SynonBiomedSkillCategorySelection,
} from '@/renderer/services/skills/synonBiomedSkillCategories';

export type SkillStatusFilter = 'all' | 'enabled' | 'disabled';
export type SkillSourceFilter = 'all' | 'recommended' | 'imported' | 'personal';

interface SkillLibraryToolbarProps {
  query: string;
  onQueryChange: (value: string) => void;
  category: SynonBiomedSkillCategorySelection;
  categories: SynonBiomedSkillCategoryOption[];
  onCategoryChange: (value: SynonBiomedSkillCategorySelection) => void;
  source: SkillSourceFilter;
  sourceCounts: Record<SkillSourceFilter, number>;
  onSourceChange: (value: SkillSourceFilter) => void;
  status: SkillStatusFilter;
  onStatusChange: (value: SkillStatusFilter) => void;
}

/** One library; source and enabled state are refinements, not separate destinations. */
export function SkillLibraryToolbar(props: SkillLibraryToolbarProps) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const panelId = useId();
  const activeCount = Number(props.source !== 'all') + Number(props.status !== 'all');
  const sourceOptions: Array<{ value: SkillSourceFilter; label: string }> = [
    { value: 'all', label: t('settings.skillsSettings.allSources') },
    { value: 'recommended', label: t('settings.skillsSettings.sources.builtin') },
    { value: 'imported', label: t('settings.skillsSettings.imported') },
    { value: 'personal', label: t('settings.skillsSettings.sources.personal') },
  ];

  return (
    <div className='settings-skill-library-controls'>
      <div className='settings-skill-library-toolbar' role='search' aria-label={t('settings.skillsSettings.title')}>
        <div className='settings-skill-library-search'>
          <Input
            data-testid='input-search-synon-biomed-skills'
            value={props.query}
            onChange={props.onQueryChange}
            allowClear
            prefix={<Search size={15} />}
            placeholder={t('settings.skillsSettings.search')}
            aria-label={t('settings.skillsSettings.search')}
          />
        </div>
        <select
          className='settings-skill-library-select'
          data-testid='skill-category-filter'
          aria-label={t('settings.skillsSettings.researchField')}
          value={props.category}
          onChange={(event) => props.onCategoryChange(event.target.value as SynonBiomedSkillCategorySelection)}
        >
          {props.categories.map((option) => (
            <option key={option.id} value={option.id}>
              {option.label}
            </option>
          ))}
        </select>
        <Button
          data-testid='synon-biomed-skills-filter'
          aria-expanded={expanded}
          aria-controls={panelId}
          onClick={() => setExpanded((value) => !value)}
        >
          {t('settings.skillsSettings.filters')}
          {activeCount > 0 ? <span className='settings-skill-filter-count'>{activeCount}</span> : null}
          <Down size={14} aria-hidden='true' />
        </Button>
      </div>
      {expanded ? (
        <div
          id={panelId}
          className='settings-skill-filter-panel'
          role='group'
          aria-label={t('settings.skillsSettings.filters')}
        >
          <label>
            <span>{t('settings.skillsSettings.sourceFilter')}</span>
            <select
              className='settings-skill-library-select'
              value={props.source}
              onChange={(event) => props.onSourceChange(event.target.value as SkillSourceFilter)}
            >
              {sourceOptions.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label} ({props.sourceCounts[option.value]})
                </option>
              ))}
            </select>
          </label>
          <label>
            <span>{t('settings.skillsSettings.statusFilter')}</span>
            <select
              className='settings-skill-library-select'
              value={props.status}
              onChange={(event) => props.onStatusChange(event.target.value as SkillStatusFilter)}
            >
              <option value='all'>{t('settings.skillsSettings.all')}</option>
              <option value='enabled'>{t('settings.skillsSettings.enabled')}</option>
              <option value='disabled'>{t('settings.skillsSettings.disabled')}</option>
            </select>
          </label>
          <Button
            type='text'
            disabled={activeCount === 0}
            onClick={() => {
              props.onSourceChange('all');
              props.onStatusChange('all');
            }}
          >
            {t('settings.skillsSettings.resetFilters')}
          </Button>
        </div>
      ) : null}
    </div>
  );
}
