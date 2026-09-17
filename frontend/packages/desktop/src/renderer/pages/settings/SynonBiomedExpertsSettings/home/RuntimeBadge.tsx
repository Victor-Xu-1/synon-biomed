/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { assistantRuntimeKey, type Assistant } from '@/common/types/agent/assistantTypes';
import SynonBiomedAvatar from '@/renderer/components/synonBiomed/SynonBiomedAvatar';
import { resolveAgentLogo, useAgentLogos } from '@/renderer/utils/synonBiomed/runtime/runtimeLogo';
import React from 'react';
import { useTranslation } from 'react-i18next';

/**
 * Shows which Synon Biomed runtime drives an assistant with a muted label and icon.
 */
const RuntimeBadge: React.FC<{ assistant: Assistant; framed?: boolean }> = ({ assistant, framed = false }) => {
  const { t } = useTranslation();
  const logos = useAgentLogos();
  const backend = assistantRuntimeKey(assistant);
  const logo = resolveAgentLogo(logos, { backend });
  const label = backend === 'synonbiomed' ? t('settings.synonBiomedRuntimeLabel') : t('settings.assistantRuntimeLabel');

  return (
    <span
      className={
        framed
          ? 'inline-flex items-center gap-4px rounded-8px border border-solid border-arco-2 bg-fill-1 px-8px py-4px text-11px text-t-tertiary'
          : 'inline-flex items-center gap-4px text-11px text-t-tertiary'
      }
      data-testid={`assistant-runtime-${assistant.id}`}
    >
      <span className='text-t-quaternary'>{label}</span>
      {logo ? <img src={logo} alt='' className='h-15px w-15px object-contain' /> : <SynonBiomedAvatar size={13} />}
    </span>
  );
};

export default RuntimeBadge;
