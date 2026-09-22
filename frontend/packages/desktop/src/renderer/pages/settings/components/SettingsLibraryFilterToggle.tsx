/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * SettingsLibraryFilterToggle — the one "筛选" disclosure shared by the merged
 * library tab headers that refine a tab beyond its category dropdown.
 *
 * It is deliberately the same height as SettingsLibraryFilterSelect so the
 * filter cluster reads as one control row on every tab. The refined controls
 * stay in the tab's own filter panel; nothing new is filtered here.
 */

import { Button } from '@arco-design/web-react';
import { Down } from '@icon-park/react';
import React from 'react';
import { useTranslation } from 'react-i18next';

type SettingsLibraryFilterToggleProps = {
  expanded: boolean;
  /** Id of the filter panel this toggle discloses. */
  controls: string;
  /** Number of active refinements, omitted when nothing is refined. */
  activeCount?: number;
  'data-testid'?: string;
  onClick: () => void;
};

const SettingsLibraryFilterToggle: React.FC<SettingsLibraryFilterToggleProps> = ({
  expanded,
  controls,
  activeCount = 0,
  'data-testid': dataTestId,
  onClick,
}) => {
  const { t } = useTranslation();

  return (
    <Button
      className='settings-library-filter-toggle'
      data-testid={dataTestId}
      aria-expanded={expanded}
      aria-controls={controls}
      onClick={onClick}
    >
      {t('settings.skillsSettings.filters')}
      {activeCount > 0 ? <span className='settings-library-filter-count'>{activeCount}</span> : null}
      <Down size={14} aria-hidden='true' />
    </Button>
  );
};

export default SettingsLibraryFilterToggle;
