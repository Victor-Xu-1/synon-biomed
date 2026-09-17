/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { resolveAgentLogo, useAgentLogos } from '@/renderer/utils/synonBiomed/runtime/runtimeLogo';
import SynonBiomedAvatar, { isRobotAvatar } from '@/renderer/components/synonBiomed/SynonBiomedAvatar';
import React, { useCallback } from 'react';
import { useNavigate } from 'react-router';

export type SynonBiomedRuntimeBadgeProps = {
  /** Synon Biomed runtime backend type */
  backend?: string;
  /** Display name for the Synon Biomed runtime */
  agent_name?: string;
  /** Synon Biomed runtime logo (SVG path or emoji string) */
  agentLogo?: string;
  /** Whether the logo is an emoji */
  agentLogoIsEmoji?: boolean;
  /** Whether the explicit assistant logo is intentionally empty. */
  agentLogoIsFallback?: boolean;
  /** Assistant ID — when provided, clicking the badge navigates to SynonBiomedExpertsSettings */
  assistantId?: string;
};

/** Render Synon Biomed runtime logo from catalog metadata or the shared avatar. */
export const SynonBiomedRuntimeLogoIcon: React.FC<
  Pick<
    SynonBiomedRuntimeBadgeProps,
    'backend' | 'agentLogo' | 'agentLogoIsEmoji' | 'agentLogoIsFallback' | 'agent_name'
  >
> = ({ backend, agentLogo, agentLogoIsEmoji, agentLogoIsFallback, agent_name }) => {
  const logos = useAgentLogos();
  const logoContent = (() => {
    if (agentLogoIsFallback) {
      return <SynonBiomedAvatar size={16} />;
    }
    if (agentLogo) {
      if (agentLogoIsEmoji && !isRobotAvatar(agentLogo)) {
        return <span className='text-14px leading-none'>{agentLogo}</span>;
      }
      return (
        <img src={agentLogo} alt={`${agent_name || 'agent'} logo`} className='block w-16px h-16px object-contain' />
      );
    }
    const logo = resolveAgentLogo(logos, { backend });
    if (logo) {
      return <img src={logo} alt={`${backend} logo`} className='block w-16px h-16px object-contain' />;
    }
    return <SynonBiomedAvatar size={16} />;
  })();

  return (
    <span className='inline-flex w-16px h-16px items-center justify-center shrink-0 leading-none'>{logoContent}</span>
  );
};

/**
 * SynonBiomedRuntimeBadge - Synon Biomed runtime identity badge (logo + name)
 *
 * When `assistantId` is provided, clicking navigates to SynonBiomedExpertsSettings editor.
 * Otherwise renders as a static display badge.
 */
const SynonBiomedRuntimeBadge: React.FC<SynonBiomedRuntimeBadgeProps> = ({
  backend,
  agent_name,
  agentLogo,
  agentLogoIsEmoji,
  agentLogoIsFallback,
  assistantId,
}) => {
  const navigate = useNavigate();
  const handleClick = useCallback(() => {
    if (!assistantId) return;
    navigate(`/settings/experts?highlight=${encodeURIComponent(assistantId)}`);
  }, [assistantId, navigate]);

  return (
    <div
      className={`flex items-center gap-2 bg-2 w-fit rounded-full px-[8px] py-[2px] ${assistantId ? 'cursor-pointer hover:bg-3' : ''}`}
      data-testid='agent-badge'
      onClick={handleClick}
    >
      <SynonBiomedRuntimeLogoIcon
        backend={backend}
        agent_name={agent_name}
        agentLogo={agentLogo}
        agentLogoIsEmoji={agentLogoIsEmoji}
        agentLogoIsFallback={agentLogoIsFallback}
      />
      <span className='text-sm text-t-primary'>{agent_name || backend}</span>
    </div>
  );
};

export default SynonBiomedRuntimeBadge;
