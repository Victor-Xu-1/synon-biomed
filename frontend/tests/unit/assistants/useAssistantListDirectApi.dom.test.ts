import { expect, describe, it, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  loadSynonBiomedAgents: vi.fn(),
  toSynonAIAssistant: vi.fn(),
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    assistants: {
      setState: { invoke: vi.fn() },
    },
  },
}));

vi.mock('@/renderer/services/synonBiomedCapabilities', () => ({
  loadSynonBiomedAgents: mocks.loadSynonBiomedAgents,
  toSynonAIAssistant: mocks.toSynonAIAssistant,
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    i18n: { language: 'en' },
  }),
}));

import { useAssistantList } from '@/renderer/hooks/assistant/useAssistantList';

describe('useAssistantList direct Synon Biomed API', () => {
  it('loads visible backend experts through the capability service instead of the legacy catalog facade', async () => {
    mocks.loadSynonBiomedAgents.mockResolvedValue([
      {
        name: 'OPERON',
        displayName: 'Operon',
        description: 'Coordinate biomedical research workflows.',
        healthy: true,
        enabled: true,
        source: 'managed',
        skillsLocked: true,
        unrestricted: false,
        supportsPlanMode: true,
        userHidden: false,
        skillNames: ['alphafold2'],
      },
    ]);
    mocks.toSynonAIAssistant.mockReturnValue({
      id: 'synonbiomed:operon',
      name: 'Operon',
      enabled: true,
      sort_order: 10000,
    });

    const { result } = renderHook(() => useAssistantList());

    await waitFor(() => expect(result.current.assistants).toHaveLength(1));

    expect(mocks.loadSynonBiomedAgents).toHaveBeenCalledTimes(1);
    expect(mocks.toSynonAIAssistant).toHaveBeenCalledWith(expect.objectContaining({ name: 'OPERON' }), 0);
    expect(result.current.activeAssistantId).toBe('synonbiomed:operon');
  });
});
