/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * SettingsLibraryTabHeader — one-line compact header for the merged library
 * page tabs (experts / skills / connectors / environments).
 *
 * Each tab renders the module title, an optional count badge, its category
 * filter controls, and the module's primary action on the right. Every tab
 * keeps the same control row: filters always sit immediately left of the
 * primary action. Long descriptions and result summaries are intentionally
 * omitted; standalone pages keep using SettingsPageHeader unchanged.
 */

import React from 'react';

type SettingsLibraryTabHeaderProps = {
  title: React.ReactNode;
  /** Optional inventory count rendered as a badge after the title. */
  count?: number;
  /** Category and refinement controls, right-aligned before the primary action. */
  filters?: React.ReactNode;
  /** Right-aligned primary action slot. */
  actions?: React.ReactNode;
  /** Disclosed refinement controls rendered under the one-line control row. */
  filterPanel?: React.ReactNode;
  /** Extra testid for the whole header block. */
  'data-testid'?: string;
};

const SettingsLibraryTabHeader: React.FC<SettingsLibraryTabHeaderProps> = ({
  title,
  count,
  filters,
  actions,
  filterPanel,
  'data-testid': dataTestId,
}) => (
  <header data-testid={dataTestId} className='settings-library-tab-header'>
    <div className='settings-library-tab-header__row'>
      <h1 className='settings-library-tab-header__title'>{title}</h1>
      {typeof count === 'number' ? <span className='settings-library-tab-header__count'>{count}</span> : null}
      {filters ? <div className='settings-library-tab-header__filters'>{filters}</div> : null}
      {actions ? <div className='settings-library-tab-header__actions'>{actions}</div> : null}
    </div>
    {filterPanel ? <div className='settings-library-tab-header__filter-panel'>{filterPanel}</div> : null}
  </header>
);

export default SettingsLibraryTabHeader;
