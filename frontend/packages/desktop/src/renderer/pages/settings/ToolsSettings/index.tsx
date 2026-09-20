/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * ToolsSettings renders Synon Biomed MCP runtime tools as an SynonAI settings module.
 */

import React from 'react';
import { SynonBiomedMcpSettingsContent } from './SynonBiomedMcpSettings';
import SettingsPageWrapper from '../components/SettingsPageWrapper';

const ToolsSettings: React.FC = () => {
  return (
    <SettingsPageWrapper>
      <div className='settings-tools-page flex flex-col'>
        <SynonBiomedMcpSettingsContent />
      </div>
    </SettingsPageWrapper>
  );
};

export default ToolsSettings;
