import React from 'react';
import { useTranslation } from 'react-i18next';
import SettingsPageHeader from './components/SettingsPageHeader';
import SettingsPageWrapper from './components/SettingsPageWrapper';
import SynonBiomedMemoryManager from './components/SynonBiomedMemoryManager';

export const GovernanceSettingsContent: React.FC = () => {
  const { t } = useTranslation();
  return (
    <div className='settings-governance-page flex min-h-0 flex-col'>
      <SettingsPageHeader
        data-testid='memory-header'
        title={t('settings.governance')}
        description={t('settings.governanceDescription')}
      />
      <SynonBiomedMemoryManager />
    </div>
  );
};

const GovernanceSettings: React.FC = () => (
  <SettingsPageWrapper>
    <GovernanceSettingsContent />
  </SettingsPageWrapper>
);

export default GovernanceSettings;
