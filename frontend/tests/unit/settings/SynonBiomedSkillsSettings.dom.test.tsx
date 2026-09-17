import { ConfigProvider } from '@arco-design/web-react';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import SynonBiomedSkillsSettings from '@/renderer/pages/settings/SynonBiomedSkillsSettings';
import { SKILL_MARKET_SOURCES } from '@/renderer/pages/settings/skills/SynonBiomedSkillMarketPanel';
import { renderWithSettingsI18n } from './settingsI18nTestUtils';

const mocks = vi.hoisted(() => ({
  loadSkills: vi.fn(),
  setEnabled: vi.fn(),
  loadDrafts: vi.fn(),
  loadSources: vi.fn(),
  loadUsage: vi.fn(),
  importFile: vi.fn(),
  previewRepository: vi.fn(),
  importRepository: vi.fn(),
  deleteSkill: vi.fn(),
  removeSource: vi.fn(),
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

vi.mock('@/renderer/services/skills/skillsCatalog', () => ({
  loadAvailableSkillsWithSynonBiomed: mocks.loadSkills,
}));

vi.mock('@/renderer/services/synonBiomedCapabilities', () => ({
  setSynonBiomedSkillEnabled: mocks.setEnabled,
}));

vi.mock('@/renderer/services/skills/synonBiomedSkillLibrary', () => ({
  loadSynonBiomedSkillDrafts: mocks.loadDrafts,
  loadSynonBiomedSkillSources: mocks.loadSources,
  importSynonBiomedSkillFile: mocks.importFile,
  previewSynonBiomedSkillRepository: mocks.previewRepository,
  importSynonBiomedRepositorySkills: mocks.importRepository,
  deleteSynonBiomedPersonalSkill: mocks.deleteSkill,
  removeSynonBiomedSkillSource: mocks.removeSource,
}));

vi.mock('@/renderer/services/skills/synonBiomedSkillUsage', () => ({
  findSynonBiomedSkillUsage: (usage: Record<string, unknown>, name: string) => usage[name] ?? null,
  loadSynonBiomedSkillUsage: mocks.loadUsage,
}));

vi.mock('@/renderer/pages/settings/skills/SynonBiomedSkillLibraryModals', () => ({
  GitHubSkillImportModal: ({ visible, initialRepo }: { visible: boolean; initialRepo: string }) =>
    visible ? <div data-testid='github-skill-import-modal'>{initialRepo || 'new-repository'}</div> : null,
  CreatePersonalSkillModal: ({ visible }: { visible: boolean }) =>
    visible ? <div data-testid='create-personal-skill-modal' /> : null,
  SkillDetailModal: ({ visible, skill }: { visible: boolean; skill: { name: string } | null }) =>
    visible ? <div data-testid='skill-detail-modal'>{skill?.name}</div> : null,
}));

const skills = [
  {
    name: 'alphafold2',
    displayName: 'AlphaFold2',
    description: 'Protein structure prediction',
    description_i18n: { 'zh-CN': '预测蛋白质结构，为结构分析和药物研究提供模型结果。' },
    source: 'bundled',
    category: 'structural-biology',
    license: 'Apache-2.0',
    enabled: true,
  },
  {
    name: 'github-evidence',
    displayName: 'GitHub Evidence',
    description: 'Imported evidence synthesis',
    source: 'marketplace',
    category: 'research',
    enabled: false,
  },
  {
    name: 'my-literature-review',
    displayName: 'My Literature Review',
    description: 'Personal evidence workflow',
    source: 'personal',
    enabled: true,
  },
];

describe('Synon Biomed Skills settings', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.loadSkills.mockResolvedValue(skills);
    mocks.loadDrafts.mockResolvedValue([
      {
        name: 'draft-review',
        displayName: 'Draft Review',
        description: 'Draft evidence workflow',
        files: ['SKILL.md'],
        updatedAt: '2026-07-14T00:00:00Z',
      },
    ]);
    mocks.loadSources.mockResolvedValue([
      {
        slug: 'research-skills',
        repo: 'https://github.com/example/research-skills',
        sha: '0123456789abcdef',
        license: 'Apache-2.0',
        skills: ['github-evidence'],
        importedAt: '2026-07-14T00:00:00Z',
        removable: true,
      },
    ]);
    mocks.loadUsage.mockResolvedValue({});
    mocks.setEnabled.mockResolvedValue(undefined);
    mocks.previewRepository.mockImplementation(async (repo: string) => ({
      repo,
      slug: repo.split('/').at(-1),
      sha: '0123456789abcdef0123456789abcdef',
      license: 'Apache-2.0',
      skills: [
        {
          name: 'protein-skill',
          displayName: 'Protein Skill',
          description: 'Protein structure analysis',
          path: '/tmp/synon-skill-marketplace-test/skills/protein-skill/SKILL.md',
          selected: true,
        },
      ],
    }));
    mocks.importRepository.mockResolvedValue({
      imported: ['protein-skill'],
      skipped: [],
      slug: 'anthropics-skills',
      sha: '0123456789abcdef0123456789abcdef',
    });
  });

  it('renders the v1.1 skill sections and their real grouped content', async () => {
    await renderSettings();

    const alphaRow = await screen.findByTestId('synon-biomed-skill-row-alphafold2');
    expect(alphaRow).toBeInTheDocument();
    expect(screen.getByTestId('synon-biomed-skill-grid')).toHaveClass('settings-entity-grid');
    expect(alphaRow).toHaveClass('settings-entity-card', 'settings-skill-card');
    const tabsRow = document.querySelector('.settings-page-header__tabs-row');
    expect(tabsRow).not.toBeNull();
    expect(tabsRow?.querySelector('[role="tab"]')).not.toBeNull();
    expect(tabsRow?.querySelector('[data-testid="input-search-synon-biomed-skills"]')).not.toBeNull();
    expect(tabsRow?.querySelector('[data-testid="add-skill-button"]')).not.toBeNull();
    expect(document.querySelector('.skills-toolbar')).toBeNull();
    const categoryFilter = screen.getByTestId('skill-category-filter');
    expect(categoryFilter).toHaveClass('synon-skill-category-filter');
    expect(categoryFilter).toHaveAttribute('title', expect.stringContaining('按药物研发'));
    expect(categoryFilter).toHaveTextContent('科研领域筛选');
    const allCategories = screen.getByTestId('skill-category-filter-all');
    const structuralCategory = screen.getByTestId('skill-category-filter-structural-biology');
    expect(allCategories).toHaveAttribute('aria-pressed', 'true');
    expect(structuralCategory).toHaveAttribute('aria-pressed', 'false');
    expect(structuralCategory).toHaveTextContent('结构生物学与蛋白质工程');
    fireEvent.click(structuralCategory);
    await waitFor(() => {
      expect(structuralCategory).toHaveAttribute('aria-pressed', 'true');
      expect(allCategories).toHaveAttribute('aria-pressed', 'false');
    });
    expect(alphaRow).toHaveTextContent('预测蛋白质结构');
    expect(alphaRow).toHaveTextContent('结构生物学与蛋白质工程');
    expect(alphaRow).not.toHaveTextContent('structural-biology');
    expect(screen.getByRole('tab', { name: '推荐 (1)' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: '已导入 (1)' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: '个人 (2)' })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('tab', { name: '已导入 (1)' }));
    expect(await screen.findByTestId('imported-skill-sources')).toHaveTextContent('research-skills');
    expect(screen.getByTestId('synon-biomed-skill-row-github-evidence')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('tab', { name: '个人 (2)' }));
    expect(await screen.findByTestId('synon-biomed-skill-row-my-literature-review')).toBeInTheDocument();
    expect(screen.getByTestId('synon-biomed-skill-draft-draft-review')).toBeInTheDocument();
  });

  it('uses the English source description in the English interface', async () => {
    await renderWithSettingsI18n(
      <ConfigProvider>
        <SynonBiomedSkillsSettings withWrapper={false} />
      </ConfigProvider>,
      'en-US'
    );

    const alphaRow = await screen.findByTestId('synon-biomed-skill-row-alphafold2');
    expect(alphaRow).toHaveTextContent('Protein structure prediction');
    expect(alphaRow).not.toHaveTextContent('预测蛋白质结构');
  });

  it('opens the online Skill market without replacing the existing import flow', async () => {
    await renderSettings();

    fireEvent.click(screen.getByRole('tab', { name: '在线市场' }));

    expect(await screen.findByTestId('synon-biomed-skill-market')).toBeInTheDocument();
    const marketGrid = await screen.findByTestId('synon-biomed-skill-market-grid');
    expect(marketGrid).toBeInTheDocument();
    expect(marketGrid).toHaveClass('grid-cols-1', 'xl:grid-cols-4', '2xl:grid-cols-5');
    const marketResults = screen.getByTestId('synon-biomed-skill-market-results');
    expect(within(marketResults).getByText('生物医药 Skill')).toBeInTheDocument();
    expect(screen.getAllByText('Bioconductor 生物医药技能库')).toHaveLength(2);
    expect(screen.getByTestId('synon-biomed-skill-market-source-bioconductor-ai-agent-skills')).toHaveAttribute(
      'aria-pressed',
      'true'
    );
    await waitFor(() => expect(screen.getAllByText('Protein Skill')).toHaveLength(1));
    expect(screen.getAllByText('中文介绍')).toHaveLength(1);
    expect(screen.getAllByText('英文原文')).toHaveLength(1);

    expect(marketGrid.querySelectorAll('details')).toHaveLength(1);
    expect(
      Array.from(marketGrid.querySelectorAll('details')).filter((details) =>
        details.textContent?.includes('skills/protein-skill/SKILL.md')
      )
    ).toHaveLength(1);
    expect(screen.queryByText(/\/tmp\//)).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Bioconductor AI Agent Skills/ })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '在 GitHub 搜索' })).toHaveAttribute(
      'href',
      expect.stringContaining('SKILL.md')
    );
  });

  it('filters the market by source and loads one Skill through the pinned import path', async () => {
    await renderSettings();
    fireEvent.click(screen.getByRole('tab', { name: '在线市场' }));

    expect(await screen.findByTestId('synon-biomed-skill-market-grid')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /Anthropic Skills/ }));
    expect(screen.getAllByText('Protein Skill')).toHaveLength(1);

    fireEvent.click(screen.getByRole('button', { name: '加载到个人' }));
    await waitFor(() =>
      expect(mocks.importRepository).toHaveBeenCalledWith(
        expect.objectContaining({ repo: 'https://github.com/anthropics/skills', sha: expect.any(String) }),
        ['protein-skill']
      )
    );
    expect(await screen.findByRole('button', { name: '已加载' })).toBeDisabled();
  });

  it('finishes in a recoverable state when every external market source is unavailable', async () => {
    mocks.previewRepository.mockRejectedValue(new Error('registry unavailable'));
    await renderSettings();
    fireEvent.click(screen.getByRole('tab', { name: '在线市场' }));

    expect(await screen.findByText('部分来源暂不可用，已加载的 Skill 仍可使用。')).toBeInTheDocument();
    expect(screen.getAllByText('暂不可用')).toHaveLength(SKILL_MARKET_SOURCES.length);
    expect(screen.queryByTestId('synon-biomed-skill-market-grid')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '重试' })).toBeVisible();
  });

  it('searches the active section and persists an enabled change', async () => {
    await renderSettings();

    const alphaSwitch = await screen.findByRole('switch', { name: '启用 AlphaFold2' });
    fireEvent.click(alphaSwitch);
    await waitFor(() => expect(mocks.setEnabled).toHaveBeenCalledWith('alphafold2', false));
    await waitFor(() => expect(screen.getByRole('switch', { name: '启用 AlphaFold2' })).not.toBeChecked());

    fireEvent.change(screen.getByRole('textbox', { name: '搜索 Skills' }), {
      target: { value: 'missing' },
    });
    expect(screen.queryByTestId('synon-biomed-skill-row-alphafold2')).not.toBeInTheDocument();
    expect(screen.getByText('没有匹配的 Skill')).toBeInTheDocument();
  });

  it('keeps personal skills searchable and shows durable usage statistics per skill', async () => {
    mocks.loadUsage.mockResolvedValue({
      'my-literature-review': {
        invocationCount: 3,
        lastUsedAt: '2026-08-15T10:20:00Z',
      },
    });

    await renderSettings();

    await screen.findByTestId('synon-biomed-skill-row-alphafold2');
    fireEvent.click(screen.getByRole('tab', { name: '个人 (2)' }));
    fireEvent.change(screen.getByRole('textbox', { name: '搜索 Skills' }), {
      target: { value: 'my-literature-review' },
    });

    const row = await screen.findByTestId('synon-biomed-skill-row-my-literature-review');
    expect(row).toHaveTextContent('最近使用');
    expect(row).toHaveTextContent('调用 3 次');
    expect(screen.getByTestId('synon-biomed-skill-usage-my-literature-review')).toHaveTextContent('调用 3 次');
    expect(screen.queryByTestId('synon-biomed-skill-row-alphafold2')).not.toBeInTheDocument();
  });

  it('opens source update, create, and skill detail workflows from the page', async () => {
    await renderSettings();
    await screen.findByTestId('synon-biomed-skill-row-alphafold2');

    const alphaRow = screen.getByTestId('synon-biomed-skill-row-alphafold2');
    expect(screen.getAllByRole('button', { name: '查看 AlphaFold2' })).toHaveLength(1);
    fireEvent.click(alphaRow);
    expect(screen.getByTestId('skill-detail-modal')).toHaveTextContent('alphafold2');

    fireEvent.click(screen.getByRole('tab', { name: '已导入 (1)' }));
    fireEvent.click(await screen.findByRole('button', { name: '检查更新' }));
    expect(screen.getByTestId('github-skill-import-modal')).toHaveTextContent(
      'https://github.com/example/research-skills'
    );
  });

  it('keeps fulfilled skill data available when one catalog source fails', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    mocks.loadDrafts.mockRejectedValueOnce(new Error('draft service unavailable'));

    await renderSettings();

    expect(await screen.findByTestId('synon-biomed-skill-row-alphafold2')).toBeInTheDocument();
    expect(screen.getByTestId('synon-biomed-skills-load-error')).toHaveTextContent(
      '部分技能数据加载失败，已加载的结果仍可使用。'
    );
    expect(screen.getByRole('button', { name: '重试' })).toBeEnabled();
    consoleError.mockRestore();
  });
});

function renderSettings() {
  return renderWithSettingsI18n(
    <ConfigProvider>
      <SynonBiomedSkillsSettings withWrapper={false} />
    </ConfigProvider>
  );
}
