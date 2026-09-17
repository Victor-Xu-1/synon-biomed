import { cleanup, fireEvent, screen, within } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { VirtuosoMockContext } from 'react-virtuoso';
const preloadStructureViewerMock = vi.hoisted(() => vi.fn(async () => undefined));
vi.mock('@/renderer/pages/conversation/Preview/components/viewers/scientificPreviewLoaders', () => ({
  preloadSynonBiomedStructureViewer: preloadStructureViewerMock,
}));
import SynonBiomedArtifactLibrary, {
  buildSynonBiomedArtifactGroups,
  buildSynonBiomedArtifactRows,
  synonBiomedArtifactItemKey,
} from '@/renderer/pages/conversation/Workspace/components/SynonBiomedArtifactLibrary';
import { renderWithI18n } from '../i18nTestUtils';

const nodes = [
  {
    name: 'project',
    fullPath: 'synonbiomed://projects/project-a',
    relativePath: '',
    isDir: true,
    isFile: false,
    children: [
      {
        name: '你的上传',
        fullPath: 'synonbiomed://projects/project-a/uploads',
        relativePath: 'uploads',
        isDir: true,
        isFile: false,
        children: [
          {
            name: 'assay.png',
            fullPath: 'synonbiomed://artifacts/artifact-upload',
            relativePath: 'uploads/assay.png',
            isDir: false,
            isFile: true,
            artifactId: 'artifact-upload',
            contentType: 'image/png',
            sizeBytes: 2048,
            contentUrl: '/api/artifacts/artifact-upload/content',
            previewKind: 'image',
          },
        ],
      },
      {
        name: 'STAT6 分析',
        fullPath: 'synonbiomed://projects/project-a/stat6',
        relativePath: 'stat6',
        isDir: true,
        isFile: false,
        children: [
          {
            name: 'STAT6_report.md',
            fullPath: 'synonbiomed://artifacts/artifact-report',
            relativePath: 'stat6/STAT6_report.md',
            isDir: false,
            isFile: true,
            artifactId: 'artifact-report',
            contentType: 'text/markdown',
            sizeBytes: 4096,
            contentUrl: '/api/artifacts/artifact-report/content',
            previewKind: 'markdown',
          },
        ],
      },
    ],
  },
];

describe('SynonBiomedArtifactLibrary', () => {
  afterEach(() => {
    cleanup();
    preloadStructureViewerMock.mockClear();
    vi.unstubAllGlobals();
  });

  it('builds v1.1-style task groups from the real workspace tree contract', () => {
    expect(buildSynonBiomedArtifactGroups(nodes)).toEqual([
      expect.objectContaining({
        label: '你的上传',
        artifacts: [expect.objectContaining({ artifactId: 'artifact-upload' })],
      }),
      expect.objectContaining({
        label: 'STAT6 分析',
        artifacts: [expect.objectContaining({ artifactId: 'artifact-report' })],
      }),
    ]);
    expect(buildSynonBiomedArtifactGroups(nodes, 'report')).toEqual([
      expect.objectContaining({
        label: 'STAT6 分析',
        artifacts: [expect.objectContaining({ name: 'STAT6_report.md' })],
      }),
    ]);
  });

  it('keeps the directly loaded project-files directory as one artifact group', () => {
    const direct = [
      {
        name: 'project-files',
        fullPath: 'synonbiomed://projects/project-a/project-files',
        relativePath: 'project-files',
        isDir: true,
        isFile: false,
        children: nodes[0].children?.flatMap((group) => group.children ?? []),
      },
    ];
    expect(buildSynonBiomedArtifactGroups(direct)).toEqual([
      expect.objectContaining({
        label: 'project-files',
        artifacts: [
          expect.objectContaining({ artifactId: 'artifact-upload' }),
          expect.objectContaining({ artifactId: 'artifact-report' }),
        ],
      }),
    ]);
  });

  it('builds stable group and paired grid rows without changing project-current artifact authority', () => {
    const groups = buildSynonBiomedArtifactGroups(nodes);
    expect(buildSynonBiomedArtifactRows(groups, new Set(), 'grid')).toEqual([
      expect.objectContaining({ type: 'group', id: 'group:uploads', count: 1, collapsed: false }),
      expect.objectContaining({ type: 'artifacts', id: 'artifacts:uploads:artifact-upload' }),
      expect.objectContaining({ type: 'group', id: 'group:stat6', count: 1, collapsed: false }),
      expect.objectContaining({ type: 'artifacts', id: 'artifacts:stat6:artifact-report' }),
    ]);
    expect(buildSynonBiomedArtifactRows(groups, new Set(['stat6']), 'grid')).toHaveLength(3);
  });

  it('packs up to four compact cards into one responsive virtual row', () => {
    const artifacts = Array.from({ length: 5 }, (_, index) => ({
      ...nodes[0].children?.[0].children?.[0],
      name: `artifact-${index}.png`,
      artifactId: `artifact-${index}`,
      relativePath: `uploads/artifact-${index}.png`,
    }));
    const groups = [
      {
        id: 'uploads',
        label: '你的上传',
        artifacts,
      },
    ];

    const rows = buildSynonBiomedArtifactRows(groups, new Set(), 'grid');
    expect(rows).toHaveLength(3);
    expect(rows[1]).toMatchObject({ type: 'artifacts' });
    expect(rows[2]).toMatchObject({ type: 'artifacts' });
    expect(rows[1]?.type === 'artifacts' ? rows[1].artifacts : []).toHaveLength(4);
    expect(rows[2]?.type === 'artifacts' ? rows[2].artifacts : []).toHaveLength(1);
  });

  it('keeps artifact item keys unique when one artifact is represented by multiple paths or versions', () => {
    const first = nodes[0].children?.[0].children?.[0];
    const second = {
      ...first,
      relativePath: 'uploads/assay-v2.png',
      versionId: 'version-2',
    };
    if (!first || !second) throw new Error('artifact fixture is incomplete');
    const keys = [synonBiomedArtifactItemKey('uploads', first, 0), synonBiomedArtifactItemKey('uploads', second, 1)];
    expect(new Set(keys).size).toBe(keys.length);
  });

  it('renders search, grouping, grid/list controls and artifact actions', async () => {
    // Lazy viewport behavior is covered by useNearViewport.dom.test.tsx. This
    // integration case disables the observer so it can exercise the rendered
    // thumbnail and artifact actions deterministically.
    vi.stubGlobal('IntersectionObserver', undefined);
    const artifactFetch = vi.fn();
    vi.stubGlobal('fetch', artifactFetch);
    const onOpen = vi.fn();
    const onMenu = vi.fn();
    const onRefresh = vi.fn();
    await renderWithI18n(
      <VirtuosoMockContext.Provider value={{ viewportHeight: 100_000, itemHeight: 140 }}>
        <SynonBiomedArtifactLibrary
          nodes={nodes}
          loading={false}
          onOpen={onOpen}
          onMenu={onMenu}
          onRefresh={onRefresh}
        />
      </VirtuosoMockContext.Provider>,
      'en-US'
    );

    expect(screen.getByRole('region', { name: 'Project artifact library' })).toBeInTheDocument();
    expect(screen.getByText('你的上传')).toBeInTheDocument();
    expect(screen.getByText('STAT6 分析')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Grid view' })).toHaveAttribute('aria-pressed', 'true');
    expect(document.querySelector('.artifact-library__grid-row')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Expand STAT6 分析' })).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByRole('button', { name: 'Preview STAT6_report.md' })).not.toBeInTheDocument();
    expect(screen.getByRole('img', { name: 'assay.png' })).toHaveAttribute(
      'src',
      '/api/artifacts/artifact-upload/content'
    );

    fireEvent.click(screen.getByRole('button', { name: 'Preview assay.png' }));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ artifactId: 'artifact-upload' }));
    const bubbledMenuClick = vi.fn();
    window.addEventListener('click', bubbledMenuClick);
    fireEvent.click(screen.getByRole('button', { name: 'More actions for assay.png' }));
    window.removeEventListener('click', bubbledMenuClick);
    expect(onMenu).toHaveBeenCalledWith(
      expect.objectContaining({ artifactId: 'artifact-upload' }),
      expect.any(Number),
      expect.any(Number)
    );
    expect(bubbledMenuClick).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Refresh artifacts' }));
    expect(onRefresh).toHaveBeenCalledTimes(1);

    fireEvent.change(screen.getByRole('searchbox', { name: 'Search project artifacts' }), {
      target: { value: 'report' },
    });
    expect(screen.queryByText('你的上传')).not.toBeInTheDocument();
    const reportGroup = screen.getByRole('button', { name: 'Collapse STAT6 分析' });
    fireEvent.click(reportGroup);
    expect(screen.queryByText('STAT6_report.md')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Expand STAT6 分析' }));
    expect(screen.getByRole('img', { name: 'STAT6_report.md' })).toBeInTheDocument();
    expect(artifactFetch).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: 'List view' }));
    const library = screen.getByRole('region', { name: 'Project artifact library' });
    expect(within(library).getByText('STAT6_report.md')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'List view' })).toHaveAttribute('aria-pressed', 'true');
  });

  it('bounds mounted artifact cards for a large project while retaining the complete count', async () => {
    const artifacts = Array.from({ length: 120 }, (_, index) => ({
      name: `result-${index}.md`,
      fullPath: `synonbiomed://artifacts/artifact-${index}`,
      relativePath: `project-files/result-${index}.md`,
      isDir: false,
      isFile: true,
      artifactId: `artifact-${index}`,
      contentType: 'text/markdown',
      sizeBytes: 1024,
      contentUrl: `/api/artifacts/artifact-${index}/content`,
      previewKind: 'markdown',
    }));
    const largeProject = [
      {
        name: 'project-files',
        fullPath: 'synonbiomed://projects/project-a/project-files',
        relativePath: 'project-files',
        isDir: true,
        isFile: false,
        children: artifacts,
      },
    ];

    await renderWithI18n(
      <VirtuosoMockContext.Provider value={{ viewportHeight: 300, itemHeight: 140 }}>
        <SynonBiomedArtifactLibrary
          nodes={largeProject}
          loading={false}
          onOpen={vi.fn()}
          onMenu={vi.fn()}
          onRefresh={vi.fn()}
        />
      </VirtuosoMockContext.Provider>,
      'en-US'
    );

    expect(screen.getByTestId('artifact-library-scroller')).toBeInTheDocument();
    expect(screen.getAllByText('120')).toHaveLength(2);
    const mountedCards = document.querySelectorAll('article.artifact-library__card').length;
    expect(mountedCards).toBeGreaterThan(0);
    expect(mountedCards).toBeLessThan(artifacts.length);
  });

  it('preloads the structure viewer from artifact-library pointer intent', async () => {
    vi.stubGlobal('IntersectionObserver', undefined);
    const structureProject = [
      {
        name: 'project-files',
        fullPath: 'synonbiomed://projects/project-a/project-files',
        relativePath: 'project-files',
        isDir: true,
        isFile: false,
        children: [
          {
            name: 'complex.pdb',
            fullPath: 'synonbiomed://artifacts/artifact-structure',
            relativePath: 'project-files/complex.pdb',
            isDir: false,
            isFile: true,
            artifactId: 'artifact-structure',
            contentType: 'chemical/x-pdb',
            sizeBytes: 1024,
            contentUrl: '/api/artifacts/artifact-structure/content',
            previewKind: 'structure',
          },
        ],
      },
    ];

    await renderWithI18n(
      <VirtuosoMockContext.Provider value={{ viewportHeight: 100_000, itemHeight: 140 }}>
        <SynonBiomedArtifactLibrary
          nodes={structureProject}
          loading={false}
          onOpen={vi.fn()}
          onMenu={vi.fn()}
          onRefresh={vi.fn()}
        />
      </VirtuosoMockContext.Provider>,
      'en-US'
    );

    expect(preloadStructureViewerMock).not.toHaveBeenCalled();
    fireEvent.pointerEnter(screen.getByRole('button', { name: 'Preview complex.pdb' }));
    expect(preloadStructureViewerMock).toHaveBeenCalledOnce();
  });
});
