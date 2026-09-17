/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React, { useEffect, useState } from 'react';
import ExpertWorkbench from './ExpertWorkbench';
import SynonBiomedExpertProfileModal from './SynonBiomedExpertProfileModal';
import SettingsPageWrapper from '../components/SettingsPageWrapper';

const createRequestedFromHash = (): boolean => {
  if (typeof window === 'undefined') return false;
  const query = window.location.hash.split('?', 2)[1] ?? '';
  return new URLSearchParams(query).get('create') === '1';
};

const clearCreateRequestFromHash = (): void => {
  if (typeof window === 'undefined' || !window.location.hash.includes('?')) return;
  const [route, query = ''] = window.location.hash.split('?', 2);
  const params = new URLSearchParams(query);
  if (!params.has('create')) return;
  params.delete('create');
  const suffix = params.size > 0 ? `?${params.toString()}` : '';
  window.history.replaceState(
    window.history.state,
    '',
    `${window.location.pathname}${window.location.search}${route}${suffix}`
  );
};

const SynonBiomedExpertsSettings: React.FC = () => {
  const [createVisible, setCreateVisible] = useState(createRequestedFromHash);
  const [refreshToken, setRefreshToken] = useState(0);

  useEffect(() => {
    const handleHashChange = () => {
      if (createRequestedFromHash()) setCreateVisible(true);
    };
    window.addEventListener('hashchange', handleHashChange);
    return () => window.removeEventListener('hashchange', handleHashChange);
  }, []);

  const closeCreate = () => {
    setCreateVisible(false);
    clearCreateRequestFromHash();
  };

  return (
    <SettingsPageWrapper>
      <ExpertWorkbench onCreate={() => setCreateVisible(true)} refreshToken={refreshToken} />
      <SynonBiomedExpertProfileModal
        visible={createVisible}
        profile={null}
        onClose={closeCreate}
        onChanged={() => setRefreshToken((value) => value + 1)}
      />
    </SettingsPageWrapper>
  );
};

export default SynonBiomedExpertsSettings;
