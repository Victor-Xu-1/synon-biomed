import React from 'react';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  loadAvailableSkillsWithSynonBiomed: vi.fn(),
  loadDrafts: vi.fn(),
  loadSources: vi.fn(),
  messageError: vi.fn(),
}));

vi.mock('@/renderer/services/skills/skillsCatalog', () => ({
  loadAvailableSkillsWithSynonBiomed: mocks.loadAvailableSkillsWithSynonBiomed,
}));

vi.mock('@/renderer/services/skills/synonBiomedSkillLibrary', () => ({
  loadSynonBiomedSkillDrafts: mocks.loadDrafts,
  loadSynonBiomedSkillSources: mocks.loadSources,
  deleteSynonBiomedPersonalSkill: vi.fn(),
  importSynonBiomedSkillFile: vi.fn(),
  removeSynonBiomedSkillSource: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedCapabilities', () => ({
  setSynonBiomedSkillEnabled: vi.fn(),
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    fs: {
      listSkillImportHistory: { invoke: vi.fn(async () => []) },
      getSkillImportLimits: { invoke: vi.fn(async () => ({ max_file_bytes: 0, max_total_bytes: 0 })) },
    },
    dialog: {
      showOpen: { invoke: vi.fn() },
    },
  },
}));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: {
      ...actual.Message,
      error: mocks.messageError,
      useMessage: () => [{ success: vi.fn(), error: mocks.messageError, warning: vi.fn() }, <div key='messages' />],
    },
  };
});

vi.mock('react-router', () => ({
  useLocation: () => ({ pathname: '/settings/skills' }),
  useNavigate: () => vi.fn(),
  useSearchParams: () => [new URLSearchParams(), vi.fn()],
}));

import SynonBiomedSkillsSettings from '@/renderer/pages/settings/SynonBiomedSkillsSettings';
import { renderWithSettingsI18n } from '../settings/settingsI18nTestUtils';

const synonSkills = [
  {
    name: 'alphafold2',
    description: 'Protein structure prediction workflow.',
    location: '/runtime/assets/skills/alphafold2/SKILL.md',
    relative_location: 'skills/alphafold2/SKILL.md',
    is_auto_inject: false,
    is_custom: false,
    source: 'builtin',
  },
  {
    name: 'boltz2-nim',
    description: 'Boltz2 NIM binding workflow.',
    location: '/runtime/assets/skills/boltz2-nim/SKILL.md',
    relative_location: 'skills/boltz2-nim/SKILL.md',
    is_auto_inject: false,
    is_custom: false,
    source: 'builtin',
  },
];

describe('SynonBiomedSkillsSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.loadAvailableSkillsWithSynonBiomed.mockResolvedValue(synonSkills);
    mocks.loadDrafts.mockResolvedValue([]);
    mocks.loadSources.mockResolvedValue([]);
  });

  it('renders Synon Biomed runtime skills in the shared recommended library', async () => {
    await renderWithSettingsI18n(<SynonBiomedSkillsSettings withWrapper={false} />);

    await waitFor(() => expect(mocks.loadAvailableSkillsWithSynonBiomed).toHaveBeenCalled());

    expect(screen.getByTestId('synon-biomed-skills-section')).toBeInTheDocument();
    expect(screen.getByTestId('synon-biomed-skill-row-alphafold2')).toHaveTextContent('alphafold2');
    expect(screen.getByTestId('synon-biomed-skill-row-boltz2-nim')).toHaveTextContent('boltz2-nim');
    expect(screen.getByRole('heading', { name: '技能 2' })).toBeInTheDocument();
  });

  it('searches within Synon Biomed runtime skills', async () => {
    await renderWithSettingsI18n(<SynonBiomedSkillsSettings withWrapper={false} />);

    await waitFor(() => expect(screen.getByTestId('synon-biomed-skill-row-alphafold2')).toBeInTheDocument());

    fireEvent.change(screen.getByTestId('input-search-synon-biomed-skills'), { target: { value: 'boltz' } });

    expect(screen.queryByTestId('synon-biomed-skill-row-alphafold2')).not.toBeInTheDocument();
    expect(screen.getByTestId('synon-biomed-skill-row-boltz2-nim')).toBeInTheDocument();
  });

  it('shows a Synon Biomed-specific empty state', async () => {
    mocks.loadAvailableSkillsWithSynonBiomed.mockResolvedValue([]);

    await renderWithSettingsI18n(<SynonBiomedSkillsSettings withWrapper={false} />);

    await waitFor(() => expect(screen.getByText('暂无技能')).toBeInTheDocument());
  });
});
