/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * SettingsLibraryFilterSelect — the one category/filter dropdown shared by every
 * merged library tab header (experts / skills / connectors / environments).
 *
 * A single element keeps the four headers on one control height, radius, type
 * size and padding instead of each tab restating its own toolbar style. The
 * standalone pages keep their own toolbars untouched.
 */

import classNames from 'classnames';
import React from 'react';

type SettingsLibraryFilterSelectProps = {
  'aria-label': string;
  value: string;
  onChange: (value: string) => void;
  'data-testid'?: string;
  className?: string;
  children: React.ReactNode;
};

const SettingsLibraryFilterSelect: React.FC<SettingsLibraryFilterSelectProps> = ({
  'aria-label': ariaLabel,
  value,
  onChange,
  'data-testid': dataTestId,
  className,
  children,
}) => (
  <select
    className={classNames('settings-library-filter-select', className)}
    value={value}
    aria-label={ariaLabel}
    data-testid={dataTestId}
    onChange={(event) => onChange(event.target.value)}
  >
    {children}
  </select>
);

export default SettingsLibraryFilterSelect;
