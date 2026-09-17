/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * SettingsPageHeader — the shared header paradigm for settings pages.
 *
 * Layout (top to bottom):
 *   1. Semantic heading row: page title + description and an optional action slot.
 *   2. Tabs (optional): underline tabs with an optional count badge and a
 *      route-owned action slot on the same baseline.
 *
 * The v3 visual system intentionally collapses the repeated title/description
 * block while retaining the heading in the accessibility tree. Route-specific
 * actions and tabs remain in the visual flow.
 *
 * Pages own everything below the header (their list/content). This keeps the
 * title sizing, description, action placement, tab styling and responsive
 * breakpoints identical across Agents / Skills / Tools.
 */

import classNames from 'classnames';
import React from 'react';

export type SettingsPageTab = {
  key: string;
  label: string;
  /** Optional count badge shown after the label. */
  count?: number;
};

type SettingsPageHeaderProps = {
  title: React.ReactNode;
  /** Secondary description under the title; may contain inline links. */
  description?: React.ReactNode;
  /** Right-aligned action slot (search, create button, dropdowns, …). */
  actions?: React.ReactNode;
  /** Optional actions aligned with the tab row rather than the heading row. */
  tabsActions?: React.ReactNode;
  tabs?: SettingsPageTab[];
  activeTab?: string;
  onTabChange?: (key: string) => void;
  /** Extra testid for the whole header block. */
  'data-testid'?: string;
};

const SettingsPageHeader: React.FC<SettingsPageHeaderProps> = ({
  title,
  description,
  actions,
  tabsActions,
  tabs,
  activeTab,
  onTabChange,
  'data-testid': dataTestId,
}) => {
  return (
    <header data-testid={dataTestId} className='settings-page-header'>
      <div className='settings-page-header__row'>
        <h1 className='settings-page-header__title'>{title}</h1>
        {actions ? <div className='settings-page-header__actions'>{actions}</div> : null}
      </div>
      {description ? <p className='settings-page-header__description'>{description}</p> : null}

      {tabs && tabs.length > 0 ? (
        <div className='settings-page-header__tabs-row'>
          <div className='settings-page-header__tabs' role='tablist'>
            {tabs.map((tab) => {
              const isActive = tab.key === activeTab;
              return (
                <button
                  key={tab.key}
                  type='button'
                  role='tab'
                  aria-selected={isActive}
                  data-testid={`settings-tab-${tab.key}`}
                  onClick={() => onTabChange?.(tab.key)}
                  className={classNames(
                    'relative inline-flex cursor-pointer items-center border-none bg-transparent px-2px pb-12px text-14px leading-none transition-colors',
                    isActive ? 'font-600 text-t-primary' : 'font-500 text-t-tertiary hover:text-t-secondary'
                  )}
                >
                  <span>{tab.label}</span>
                  {typeof tab.count === 'number' ? (
                    <span
                      className={classNames(
                        'ml-6px inline-flex h-16px min-w-16px items-center justify-center rounded-999px px-5px text-10px font-500 leading-none',
                        isActive ? 'bg-primary-1 text-primary-6' : 'bg-fill-2 text-t-quaternary'
                      )}
                    >
                      {tab.count}
                    </span>
                  ) : null}
                  {isActive ? <span className='absolute inset-x-0 -bottom-1px h-2px rounded-2px bg-primary-6' /> : null}
                </button>
              );
            })}
          </div>
          {tabsActions ? <div className='settings-page-header__tabs-actions'>{tabsActions}</div> : null}
        </div>
      ) : null}
    </header>
  );
};

export default SettingsPageHeader;
