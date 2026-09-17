import { ConfigProvider } from '@arco-design/web-react';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { SkillDetailModal } from '@/renderer/pages/settings/skills/SynonBiomedSkillLibraryModals';
import { renderWithSettingsI18n } from './settingsI18nTestUtils';

const mocks = vi.hoisted(() => ({
  loadFiles: vi.fn(),
  loadContent: vi.fn(),
  saveFile: vi.fn(),
  duplicate: vi.fn(),
  publish: vi.fn(),
  deleteDraft: vi.fn(),
}));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: {
      useMessage: () => [{ success: vi.fn(), warning: vi.fn(), error: vi.fn() }, null],
    },
  };
});

vi.mock('@/renderer/components/Markdown', () => ({
  default: ({ children }: { children: React.ReactNode }) => <div data-testid='skill-markdown'>{children}</div>,
}));

vi.mock('@/renderer/services/skills/synonBiomedSkillLibrary', () => ({
  loadSynonBiomedSkillFiles: mocks.loadFiles,
  loadSynonBiomedSkillFileContent: mocks.loadContent,
  saveSynonBiomedSkillDraftFile: mocks.saveFile,
  duplicateSynonBiomedSkill: mocks.duplicate,
  publishSynonBiomedSkillDraft: mocks.publish,
  deleteSynonBiomedSkillDraft: mocks.deleteDraft,
  importSynonBiomedSkillFile: vi.fn(),
  importSynonBiomedRepositorySkills: vi.fn(),
  previewSynonBiomedSkillRepository: vi.fn(),
}));

const bundledSkill = {
  name: 'alphafold2',
  displayName: 'AlphaFold2',
  description: 'Predict protein structures with ColabFold.',
  source: 'synon_llm',
  category: 'biomodels',
  license: 'Apache-2.0',
  attachedAgents: ['OPERON'],
  thirdParty: [
    {
      kind: 'weights',
      name: 'AlphaFold2',
      provider: 'Google DeepMind',
      license: 'CC-BY-4.0',
      termsUrl: 'https://example.test/terms',
    },
  ],
};

describe('Synon Biomed skill library detail modal', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.loadFiles.mockResolvedValue(['SKILL.md']);
    mocks.loadContent.mockResolvedValue('# AlphaFold2\n\nDetailed workflow.');
    mocks.duplicate.mockResolvedValue({});
  });

  it('loads once, renders the v1.1-style detail, and does not loop on Message identity changes', async () => {
    mocks.loadContent.mockResolvedValue(
      '\n---\nname: alphafold2\ndescription: Internal metadata\n---\n\n# AlphaFold2\n\nDetailed workflow.'
    );
    await renderModal({ skill: bundledSkill, draft: false, editable: false });

    expect(await screen.findByTestId('skill-markdown')).toHaveTextContent('Detailed workflow.');
    expect(screen.queryByText('Internal metadata')).not.toBeInTheDocument();
    expect(screen.getByText('Predict protein structures with ColabFold.')).toBeInTheDocument();
    expect(screen.getByText('Synon Biomed')).toBeInTheDocument();
    expect(screen.getByText('Apache-2.0')).toBeInTheDocument();
    expect(screen.getByText('OPERON')).toBeInTheDocument();
    expect(screen.getByText(/AlphaFold2 · Google DeepMind/)).toBeInTheDocument();
    expect(screen.queryByText('第三方 LLM')).not.toBeInTheDocument();

    await waitFor(() => expect(mocks.loadFiles).toHaveBeenCalledTimes(1));
    expect(mocks.loadContent).toHaveBeenCalledTimes(1);
  });

  it('creates an editable personal draft from a read-only bundled skill', async () => {
    const onChanged = vi.fn();
    const onClose = vi.fn();
    await renderModal({ skill: bundledSkill, draft: false, editable: false, onChanged, onClose });
    await screen.findByTestId('skill-markdown');

    fireEvent.click(screen.getByRole('button', { name: '创建可编辑副本' }));
    fireEvent.change(screen.getByRole('textbox', { name: '副本 Skill 名称' }), {
      target: { value: 'alphafold2-lab' },
    });
    fireEvent.click(screen.getByRole('button', { name: '创建副本' }));

    await waitFor(() => expect(mocks.duplicate).toHaveBeenCalledWith('alphafold2', 'alphafold2-lab'));
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('edits and saves a draft file with the backend old/new content contract', async () => {
    mocks.saveFile.mockResolvedValue({});
    await renderModal({ skill: { ...bundledSkill, source: 'personal-draft' }, draft: true, editable: true });

    const editor = await screen.findByRole('textbox', { name: 'Skill 文件内容' });
    fireEvent.change(editor, { target: { value: '# Updated workflow' } });
    fireEvent.click(screen.getByRole('button', { name: '保存' }));

    await waitFor(() =>
      expect(mocks.saveFile).toHaveBeenCalledWith(
        'alphafold2',
        'SKILL.md',
        '# AlphaFold2\n\nDetailed workflow.',
        '# Updated workflow'
      )
    );
  });
});

function renderModal({
  skill,
  draft,
  editable,
  onChanged = vi.fn(),
  onClose = vi.fn(),
}: {
  skill: typeof bundledSkill;
  draft: boolean;
  editable: boolean;
  onChanged?: () => void | Promise<void>;
  onClose?: () => void;
}) {
  return renderWithSettingsI18n(
    <ConfigProvider>
      <SkillDetailModal
        visible
        skill={skill}
        draft={draft}
        editable={editable}
        onChanged={onChanged}
        onClose={onClose}
      />
    </ConfigProvider>
  );
}
