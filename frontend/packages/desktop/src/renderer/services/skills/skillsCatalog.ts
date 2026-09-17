/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { loadSynonBiomedSkills } from '@/renderer/services/synonBiomedCapabilities';

export type SynonAISkillInfo = {
  name: string;
  description: string;
  description_i18n?: Record<string, string>;
  location?: string;
  relative_location?: string;
  is_auto_inject?: boolean;
  is_custom?: boolean;
  source?: string;
  skillId?: string;
  displayName?: string;
  category?: string | null;
  license?: string | null;
  attachedAgents?: string[];
  enabled?: boolean;
};

export async function loadAvailableSkillsWithSynonBiomed<T extends SynonAISkillInfo = SynonAISkillInfo>(): Promise<
  T[]
> {
  const skills = await loadSynonBiomedSkills();

  return skills.map((skill) => ({
    name: skill.name,
    description: skill.description,
    description_i18n: skill.description_i18n,
    source: skill.source,
    is_auto_inject: false,
    is_custom: false,
    skillId: skill.skillId,
    displayName: skill.displayName,
    category: skill.category,
    license: skill.license,
    attachedAgents: skill.attachedAgents,
    enabled: skill.enabled,
  })) as T[];
}
