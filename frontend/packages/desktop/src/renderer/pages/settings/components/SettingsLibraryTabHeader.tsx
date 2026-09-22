/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * SettingsLibraryTabHeader — one-line compact header for the merged library
 * page tabs (experts / skills / connectors / environments).
 *
 * Each tab renders the module title, an optional count badge, and the module's
 * primary action on the right. Long descriptions are intentionally omitted so
 * every tab shares the same compact header rhythm; standalone pages keep using
 * SettingsPageHeader unchanged.
 */

import React from 'react';

type SettingsLibraryTabHeaderProps = {
  title: React.ReactNode;
  /** Optional inventory count rendered as a badge after the title. */
  count?: number;
  /** Right-aligned primary action slot. */
  actions?: React.ReactNode;
  /** Extra testid for the whole header block. */
  'data-testid'?: string;
};

const SettingsLibraryTabHeader: React.FC<SettingsLibraryTabHeaderProps> = ({
  title,
  count,
  actions,
  'data-testid': dataTestId,
}) => (
  <header data-testid={dataTestId} className='settings-library-tab-header'>
    <div className='settings-library-tab-header__row'>
      <h1 className='settings-library-tab-header__title'>{title}</h1>
      {typeof count === 'number' ? <span className='settings-library-tab-header__count'>{count}</span> : null}
      {actions ? <div className='settings-library-tab-header__actions'>{actions}</div> : null}
    </div>
  </header>
);

export default SettingsLibraryTabHeader;
