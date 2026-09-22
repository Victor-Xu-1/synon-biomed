/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * SettingsLibraryTabHeader — one-line compact header for the merged library
 * page tabs (experts / skills / connectors / environments).
 *
 * Each tab renders the module title, an optional count badge, exactly one
 * domain/category filter, and the module's primary action on the right. Every
 * tab keeps the same control row: the single filter always sits immediately
 * left of the primary action. Long descriptions, result summaries and extra
 * refinement disclosures are intentionally omitted; standalone pages keep
 * using SettingsPageHeader unchanged.
 */

import React from 'react';

type SettingsLibraryTabHeaderProps = {
  title: React.ReactNode;
  /** Optional inventory count rendered as a badge after the title. */
  count?: number;
  /** The tab's single domain/category filter, right-aligned before the primary action. */
  filters?: React.ReactNode;
  /** Right-aligned primary action slot. */
  actions?: React.ReactNode;
  /** Extra testid for the whole header block. */
  'data-testid'?: string;
};

const SettingsLibraryTabHeader: React.FC<SettingsLibraryTabHeaderProps> = ({
  title,
  count,
  filters,
  actions,
  'data-testid': dataTestId,
}) => (
  <header data-testid={dataTestId} className='settings-library-tab-header'>
    <div className='settings-library-tab-header__row'>
      <h1 className='settings-library-tab-header__title'>{title}</h1>
      {typeof count === 'number' ? <span className='settings-library-tab-header__count'>{count}</span> : null}
      {filters ? <div className='settings-library-tab-header__filters'>{filters}</div> : null}
      {actions ? <div className='settings-library-tab-header__actions'>{actions}</div> : null}
    </div>
  </header>
);

export default SettingsLibraryTabHeader;
