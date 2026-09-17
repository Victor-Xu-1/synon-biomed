import { describe, expect, it } from 'vitest';

import type { SynonBiomedAgent } from '@/renderer/services/synonBiomedCapabilities';
import { toSynonAIAssistant } from '@/renderer/services/synonBiomedCapabilities';

describe('toSynonAIAssistant', () => {
  it('maps a bundled expert to consistent Chinese and English presentation metadata', () => {
    const agent: SynonBiomedAgent = {
      name: 'OPERON',
      displayName: 'Operon',
      description: 'Coordinate biomedical research workflows and specialist tools.',
      healthy: true,
      enabled: true,
      source: 'managed',
      skillsLocked: true,
      unrestricted: false,
      supportsPlanMode: true,
      userHidden: false,
      skillNames: ['alphafold2', 'pubmed-search'],
    };

    expect(toSynonAIAssistant(agent, 2)).toMatchObject({
      id: 'synonbiomed:operon',
      name: 'General Research Assistant',
      name_i18n: {
        'en-US': 'General Research Assistant',
        'zh-CN': '通用科研助手',
      },
      description_i18n: {
        'en-US': expect.stringContaining('scientific computing assistant'),
        'zh-CN': expect.stringContaining('通用科研计算助手'),
      },
      enabled: true,
      sort_order: 10002,
      agent_id: 'OPERON',
      agent_status: 'online',
      enabled_skills: ['alphafold2', 'pubmed-search'],
      agent: {
        type: 'synonbiomed',
        source: 'builtin',
        acp_backend: 'synonbiomed',
      },
    });
  });

  it('preserves the backend health and enabled state for an unavailable expert', () => {
    const agent: SynonBiomedAgent = {
      name: 'PROTEOMICS',
      displayName: 'Proteomics',
      description: 'Analyze proteomics data.',
      healthy: false,
      enabled: false,
      source: 'managed',
      skillsLocked: false,
      unrestricted: false,
      supportsPlanMode: false,
      userHidden: false,
      skillNames: [],
    };

    expect(toSynonAIAssistant(agent, 0)).toMatchObject({
      id: 'synonbiomed:proteomics',
      enabled: false,
      agent_status: 'offline',
      agent_status_message: 'Synon Biomed expert is not healthy.',
    });
  });
});
