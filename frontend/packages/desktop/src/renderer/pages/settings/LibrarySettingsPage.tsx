/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import classNames from 'classnames';
import React from 'react';
import { useParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import SettingsPageWrapper from './components/SettingsPageWrapper';
import { navigateSettingsRoute } from './settingsNavigation';
import type { SettingsRouteId } from './settingsRouteLoaders';
import { ExpertWorkbenchContent } from './SynonBiomedExpertsSettings/ExpertWorkbench';
import { SynonBiomedSkillsSettingsContent } from './SynonBiomedSkillsSettings';
import { SynonBiomedMcpSettingsContent } from './ToolsSettings/SynonBiomedMcpSettings';
import { ScientificEnvironmentSettingsContent } from './ScientificEnvironmentSettings';

type LibraryTabKey = 'experts' | 'skills' | 'tools' | 'environments';

const LIBRARY_TABS: { key: LibraryTabKey; labelKey: string }[] = [
  { key: 'experts', labelKey: 'settings.synonBiomedExperts' },
  { key: 'skills', labelKey: 'settings.skills' },
  { key: 'tools', labelKey: 'settings.tools' },
  { key: 'environments', labelKey: 'settings.environments.title' },
];

const isLibraryTabKey = (value: string | undefined): value is LibraryTabKey =>
  value === 'experts' || value === 'skills' || value === 'tools' || value === 'environments';

/**
 * Merged catalog page: 专家 / 技能 / 连接器(MCP) / 科研环境 behind a single top
 * tab row. The four legacy routes each open this page with the matching tab
 * preselected; the page never paginates — the whole catalog scrolls.
 */
const LibrarySettingsPage: React.FC = () => {
  const { section } = useParams<{ section: string }>();
  const { t } = useTranslation();
  const activeTab: LibraryTabKey = isLibraryTabKey(section) ? section : 'experts';

  const handleTabChange = (key: LibraryTabKey) => {
    if (key !== activeTab) navigateSettingsRoute(key as SettingsRouteId);
  };

  return (
    <SettingsPageWrapper>
      <div className='settings-library-page flex min-h-0 flex-col'>
        <div className='settings-library-tabs-row sticky top-0 z-1' style={{ background: 'var(--color-bg-1)' }}>
          <div className='settings-page-header__tabs-row' style={{ paddingTop: 4 }}>
            <div className='settings-page-header__tabs' role='tablist'>
              {LIBRARY_TABS.map((tab) => {
                const isActive = tab.key === activeTab;
                return (
                  <button
                    key={tab.key}
                    type='button'
                    role='tab'
                    aria-selected={isActive}
                    data-testid={`settings-tab-${tab.key}`}
                    onClick={() => handleTabChange(tab.key)}
                    className={classNames(
                      'relative inline-flex cursor-pointer items-center border-none bg-transparent px-2px pb-12px text-14px leading-none transition-colors',
                      isActive ? 'font-600 text-t-primary' : 'font-500 text-t-tertiary hover:text-t-secondary'
                    )}
                  >
                    <span>{t(tab.labelKey)}</span>
                    {isActive ? (
                      <span className='absolute inset-x-0 -bottom-1px h-2px rounded-2px bg-primary-6' />
                    ) : null}
                  </button>
                );
              })}
            </div>
          </div>
        </div>
        <div className='settings-library-page__content min-h-0 flex-1 pt-16px'>
          {activeTab === 'experts' ? <ExpertWorkbenchContent /> : null}
          {activeTab === 'skills' ? <SynonBiomedSkillsSettingsContent /> : null}
          {activeTab === 'tools' ? <SynonBiomedMcpSettingsContent /> : null}
          {activeTab === 'environments' ? <ScientificEnvironmentSettingsContent /> : null}
        </div>
      </div>
    </SettingsPageWrapper>
  );
};

export default LibrarySettingsPage;
