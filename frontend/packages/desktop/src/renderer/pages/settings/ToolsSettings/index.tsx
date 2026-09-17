/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * ToolsSettings renders Synon Biomed MCP runtime tools as an SynonAI settings module.
 */

import React from 'react';
import { useTranslation } from 'react-i18next';
import { SynonBiomedMcpSettingsContent } from './SynonBiomedMcpSettings';
import SettingsPageWrapper from '../components/SettingsPageWrapper';
import SettingsPageHeader from '../components/SettingsPageHeader';

const ToolsSettings: React.FC = () => {
  const { t } = useTranslation();

  return (
    <SettingsPageWrapper>
      <div className='settings-tools-page flex flex-col'>
        <SettingsPageHeader
          data-testid='tools-header'
          title={t('settings.tools')}
          description={t('settings.toolsDescription')}
        />
        <SynonBiomedMcpSettingsContent />
      </div>
    </SettingsPageWrapper>
  );
};

export default ToolsSettings;
