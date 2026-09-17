import React from 'react';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { IScientificFilesArtifact } from '@/common/adapter/ipcBridge';
import { SYNON_BIOMED_TEXT_ACCEPT_HEADER } from '@/renderer/services/synonBiomedArtifactPreview';
import { renderWithI18n } from '../i18nTestUtils';

const openPreviewMock = vi.fn();
const { preloadStructureViewerMock } = vi.hoisted(() => ({
  preloadStructureViewerMock: vi.fn(async () => undefined),
}));
let artifactImageObserver: IntersectionObserverCallback | undefined;
let observedArtifactImages: Element[] = [];

vi.mock('@/renderer/pages/conversation/Preview/context/PreviewContext', () => ({
  usePreviewContext: () => ({ openPreview: openPreviewMock }),
}));

vi.mock('@/renderer/pages/conversation/Preview/components/viewers/scientificPreviewLoaders', () => ({
  preloadSynonBiomedStructureViewer: preloadStructureViewerMock,
}));

vi.mock('@/renderer/hooks/context/ConversationContext', () => ({
  useConversationContextSafe: () => ({
    conversation_id: 'frame-1',
    workspace: 'synonbiomed://project/proj-1',
    type: 'acp',
  }),
}));

vi.mock('@arco-design/web-react', () => ({
  Message: {
    error: vi.fn(),
  },
}));

vi.mock('@icon-park/react', () => ({
  FileText: () => <span>file</span>,
}));

import MessageScientificFiles, {
  ScientificFilePreviewStrip,
} from '@/renderer/pages/conversation/Messages/components/MessageScientificFiles';

const artifact: IScientificFilesArtifact = {
  id: 'synonbiomed-files:frame-1',
  conversation_id: 'frame-1',
  kind: 'scientific_files',
  status: 'active',
  payload: {
    project_id: 'proj-1',
    root_frame_id: 'frame-1',
    files: [
      {
        artifact_id: 'artifact-image',
        version_id: 'version-image',
        project_id: 'proj-1',
        root_frame_id: 'frame-1',
        frame_id: 'frame-1',
        filename: 'binding.png',
        content_type: 'image/png',
        size_bytes: 2048,
        preview_kind: 'image',
        content_url: '/api/artifacts/artifact-image',
        created_at: 10,
        agent_name: 'AIDD_EXPERT',
        is_intermediate: false,
      },
      {
        artifact_id: 'artifact-report',
        version_id: 'version-report',
        project_id: 'proj-1',
        root_frame_id: 'frame-1',
        frame_id: 'frame-1',
        filename: 'report.md',
        content_type: 'text/markdown',
        size_bytes: 4096,
        preview_kind: 'markdown',
        content_url: '/api/artifacts/artifact-report',
        created_at: 20,
        agent_name: 'AIDD_EXPERT',
        is_intermediate: false,
      },
    ],
  },
  created_at: 30,
  updated_at: 30,
};

describe('MessageScientificFiles', () => {
  beforeEach(() => {
    openPreviewMock.mockReset();
    preloadStructureViewerMock.mockClear();
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('# STAT6 report', { status: 200, headers: { 'content-type': 'text/markdown' } }))
    );
    artifactImageObserver = undefined;
    observedArtifactImages = [];
    vi.stubGlobal(
      'IntersectionObserver',
      class {
        constructor(callback: IntersectionObserverCallback) {
          artifactImageObserver = callback;
        }
        observe(target: Element) {
          observedArtifactImages.push(target);
        }
        unobserve() {}
        disconnect() {}
      }
    );
  });

  afterEach(() => vi.unstubAllGlobals());

  it('renders real generated files and opens image and markdown in the native SynonAI preview panel', async () => {
    await renderWithI18n(<MessageScientificFiles artifact={artifact} />, 'en-US');

    expect(screen.getByText('Generated files · 2')).toBeInTheDocument();
    expect(screen.queryByRole('img', { name: 'binding.png' })).not.toBeInTheDocument();
    expect(observedArtifactImages).toHaveLength(2);
    act(() =>
      artifactImageObserver?.(
        observedArtifactImages.map(
          (target) =>
            ({
              target,
              isIntersecting: true,
              intersectionRatio: 1,
            }) as IntersectionObserverEntry
        ),
        {} as IntersectionObserver
      )
    );
    await waitFor(() =>
      expect(screen.getByRole('img', { name: 'binding.png' })).toHaveAttribute('src', '/api/artifacts/artifact-image')
    );
    expect(screen.getByRole('img', { name: 'binding.png' })).toHaveAttribute('decoding', 'async');
    expect(screen.getByRole('button', { name: 'Preview binding.png' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Preview report.md' })).toBeInTheDocument();
    expect(screen.getByRole('img', { name: 'report.md' })).toHaveClass('bg-white');

    fireEvent.click(screen.getByRole('button', { name: 'Preview binding.png' }));
    expect(openPreviewMock).toHaveBeenLastCalledWith(
      '/api/artifacts/artifact-image',
      'image',
      expect.objectContaining({
        title: 'binding.png',
        file_name: 'binding.png',
        editable: false,
      }),
      { presentation: 'board' }
    );

    fireEvent.click(screen.getByRole('button', { name: 'Preview report.md' }));
    await waitFor(() => {
      expect(openPreviewMock).toHaveBeenLastCalledWith(
        '# STAT6 report',
        'markdown',
        expect.objectContaining({
          title: 'report.md',
          file_name: 'report.md',
          editable: true,
          artifactId: 'artifact-report',
          versionId: 'version-report',
          contentUrl: '/api/artifacts/artifact-report',
          workspace: 'synonbiomed://project/proj-1',
          companionArtifactUrls: {
            'binding.png': '/api/artifacts/artifact-image',
            'report.md': '/api/artifacts/artifact-report',
          },
        }),
        { presentation: 'board' }
      );
    });
    expect(fetch).toHaveBeenCalledWith('/api/artifacts/artifact-report', {
      headers: { accept: SYNON_BIOMED_TEXT_ACCEPT_HEADER },
    });
  });

  it('uses the compact workspace turn tray and expands only on demand', async () => {
    const files = Array.from({ length: 7 }, (_, index) => ({
      ...artifact.payload.files[1],
      artifact_id: `artifact-${index}`,
      version_id: `version-${index}`,
      filename: `result-${index}.md`,
      content_url: `/api/artifacts/artifact-${index}`,
    }));
    await renderWithI18n(<ScientificFilePreviewStrip files={files} inline />, 'en-US');

    const tray = screen.getByRole('list', { name: 'Generated files' });
    expect(screen.getByText('GENERATED · 7')).toBeInTheDocument();
    expect(tray).toHaveClass('flex', 'flex-wrap');
    expect(screen.getAllByRole('button', { name: /^Preview result-/ })).toHaveLength(5);
    const firstCard = screen.getByRole('button', { name: 'Preview result-0.md' });
    expect(firstCard).toHaveClass('message-scientific-files__card');
    expect(firstCard).toHaveStyle({
      borderRadius: '10px',
    });
    const firstPreview = firstCard.querySelector('.message-scientific-files__preview');
    expect(firstPreview).not.toHaveClass('border-solid');
    expect(firstPreview).not.toHaveClass('border-[var(--color-border-2)]');
    const showMore = screen.getByRole('button', { name: '+2 more' });
    expect(showMore).toHaveClass('message-scientific-files__more--compact');
    expect(showMore).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(showMore);
    expect(screen.getAllByRole('button', { name: /^Preview result-/ })).toHaveLength(7);
    const showLess = screen.getByRole('button', { name: 'Show fewer files' });
    expect(showLess).toHaveAttribute('aria-expanded', 'true');
    fireEvent.click(showLess);
    expect(screen.getAllByRole('button', { name: /^Preview result-/ })).toHaveLength(5);
  });

  it('preloads the heavy structure runtime only on pointer or keyboard intent', async () => {
    const structure = {
      ...artifact.payload.files[0],
      artifact_id: 'artifact-structure',
      version_id: 'version-structure',
      filename: 'complex.pdb',
      content_type: 'chemical/x-pdb',
      preview_kind: 'structure',
      content_url: '/api/artifacts/artifact-structure',
    };
    await renderWithI18n(<ScientificFilePreviewStrip files={[structure]} inline />, 'en-US');

    expect(preloadStructureViewerMock).not.toHaveBeenCalled();
    const card = screen.getByRole('button', { name: 'Preview complex.pdb' });
    fireEvent.pointerEnter(card);
    expect(preloadStructureViewerMock).toHaveBeenCalledOnce();
    fireEvent.focus(card);
    expect(preloadStructureViewerMock).toHaveBeenCalledTimes(2);
  });

  it('deduplicates repeated exact artifact versions while retaining distinct versions', async () => {
    const first = artifact.payload.files[1];
    const secondVersion = {
      ...first,
      version_id: 'version-report-2',
      content_url: '/api/artifacts/artifact-report/versions/version-report-2',
    };
    await renderWithI18n(<ScientificFilePreviewStrip files={[first, first, secondVersion]} inline />, 'en-US');

    expect(screen.getByText('GENERATED · 2')).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: 'Preview report.md' })).toHaveLength(2);
  });

  it('shows an exact deleted version as unavailable and never substitutes or fetches it', async () => {
    const deleted = {
      ...artifact.payload.files[1],
      artifact_id: 'artifact-deleted',
      version_id: 'version-deleted',
      filename: 'deleted-report.md',
      content_url: '',
      availability: 'deleted' as const,
    };

    await renderWithI18n(<ScientificFilePreviewStrip files={[deleted]} inline />, 'en-US');

    const unavailable = screen.getByRole('button', { name: 'Unavailable artifact deleted-report.md' });
    expect(unavailable).toBeDisabled();
    expect(screen.getByText('Deleted version')).toBeInTheDocument();
    fireEvent.click(unavailable);
    expect(fetch).not.toHaveBeenCalled();
    expect(openPreviewMock).not.toHaveBeenCalled();
  });
});
