/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { SynonBiomedRuntimeLogoIcon } from '@/renderer/components/synonBiomed/runtime/SynonBiomedRuntimeBadge';
import { getConversationOrNull } from '@/renderer/pages/conversation/utils/conversationCache';
import { usePresetAssistantInfo } from '@/renderer/hooks/synonBiomed/runtime/usePresetAssistantInfo';
import { resolveConversationBackend } from '@/renderer/pages/conversation/utils/conversationAssistantIdentity';
import React from 'react';
import useSWR from 'swr';

type MobileConversationBrandProps = {
  conversation_id: string;
  fallbackTitle: string;
};

const MobileConversationBrand: React.FC<MobileConversationBrandProps> = ({ conversation_id, fallbackTitle }) => {
  const { data: conversation } = useSWR(
    conversation_id ? `mobile-titlebar.conversation.${conversation_id}` : null,
    () => getConversationOrNull(conversation_id)
  );
  const { info: presetAssistant } = usePresetAssistantInfo(conversation || undefined);
  const backend = resolveConversationBackend(conversation, presetAssistant?.backend);

  const showLogo = Boolean(backend || presetAssistant);
  const title = conversation?.name || fallbackTitle;

  return (
    <span className='app-titlebar__brand-mobile'>
      {showLogo && (
        <SynonBiomedRuntimeLogoIcon
          backend={backend}
          agent_name={title}
          agentLogo={presetAssistant?.logo}
          agentLogoIsEmoji={presetAssistant?.isEmoji}
          agentLogoIsFallback={presetAssistant?.isFallback}
        />
      )}
      <span className='app-titlebar__brand-text'>{title}</span>
    </span>
  );
};

export default MobileConversationBrand;
