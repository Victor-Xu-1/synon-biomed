/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import {
  useSynonBiomedArtifactLinkPreview,
  useSynonBiomedArtifactResolver,
} from '@/renderer/pages/conversation/Messages/useSynonBiomedArtifactLinkPreview';
import type { IConversationArtifact } from '@/common/adapter/ipcBridge';
import { SYNON_BIOMED_TEXT_ACCEPT_HEADER } from '@/renderer/services/synonBiomedArtifactPreview';
import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const previewMocks = vi.hoisted(() => ({ openPreview: vi.fn() }));
const messageMocks = vi.hoisted(() => ({ error: vi.fn() }));
const ipcMocks = vi.hoisted(() => ({ listArtifacts: vi.fn() }));
let mockConversationArtifacts: IConversationArtifact[] = [];

vi.mock('@/common', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/common')>();
  return {
    ...actual,
    ipcBridge: {
      ...actual.ipcBridge,
      conversation: {
        ...actual.ipcBridge.conversation,
        listArtifacts: {
          ...actual.ipcBridge.conversation.listArtifacts,
          invoke: ipcMocks.listArtifacts,
        },
      },
    },
  };
});

vi.mock('@/renderer/pages/conversation/Preview/context/PreviewContext', () => ({
  usePreviewContext: () => ({ openPreview: previewMocks.openPreview }),
}));
vi.mock('@arco-design/web-react', () => ({
  Message: { error: messageMocks.error },
}));
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
vi.mock('@/renderer/pages/conversation/Messages/artifacts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/renderer/pages/conversation/Messages/artifacts')>();
  return {
    ...actual,
    useConversationArtifactIndex: () => actual.createConversationArtifactIndex(mockConversationArtifacts),
  };
});

describe('useSynonBiomedArtifactLinkPreview', () => {
  beforeEach(() => {
    previewMocks.openPreview.mockReset();
    messageMocks.error.mockReset();
    ipcMocks.listArtifacts.mockReset();
    ipcMocks.listArtifacts.mockResolvedValue([]);
    mockConversationArtifacts = [];
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('resolves a relative Markdown link against real frame artifacts and opens native preview', async () => {
    mockConversationArtifacts = [
      {
        id: 'synonbiomed-files:frame-stat6',
        conversation_id: 'frame-stat6',
        kind: 'scientific_files',
        status: 'active',
        payload: {
          project_id: 'proj_stat6',
          root_frame_id: 'frame-stat6',
          files: [
            {
              artifact_id: 'artifact-report',
              version_id: 'version-report',
              version_number: 1,
              project_id: 'proj_stat6',
              root_frame_id: 'frame-stat6',
              frame_id: 'frame-stat6',
              creating_frame_id: 'frame-stat6',
              filename: 'README.md',
              content_type: 'text/markdown',
              size_bytes: 2048,
              preview_kind: 'markdown',
              content_url: '/api/artifacts/artifact-report/versions/version-report',
              created_at: 1,
              updated_at: 1,
              agent_name: 'OPERON',
              is_user_upload: false,
              is_intermediate: false,
            },
          ],
        },
        created_at: 1,
        updated_at: 1,
      },
    ];
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const url = String(input);
      if (url === '/api/artifacts/artifact-report/versions/version-report') {
        return new Response('# Synon Biomed report', {
          status: 200,
          headers: { 'content-type': 'text/markdown' },
        });
      }
      return new Response('not found', { status: 404 });
    });
    vi.stubGlobal('fetch', fetchMock);

    const { result } = renderHook(() =>
      useSynonBiomedArtifactLinkPreview({
        conversationId: 'frame-stat6',
        workspace: 'synonbiomed://proj_stat6',
      })
    );

    let handled = false;
    await act(async () => {
      handled = await result.current('./README.md');
    });

    expect(handled).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledWith('/api/artifacts/artifact-report/versions/version-report', {
      headers: { accept: SYNON_BIOMED_TEXT_ACCEPT_HEADER },
    });
    expect(previewMocks.openPreview).toHaveBeenCalledWith(
      '# Synon Biomed report',
      'markdown',
      expect.objectContaining({
        title: 'README.md',
        file_name: 'README.md',
        editable: true,
        artifactId: 'artifact-report',
        versionId: 'version-report',
        contentUrl: '/api/artifacts/artifact-report/versions/version-report',
      }),
      { presentation: 'board' }
    );
    expect(messageMocks.error).not.toHaveBeenCalled();
  });

  it('declines external links without loading the artifact index', async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    const { result } = renderHook(() =>
      useSynonBiomedArtifactLinkPreview({
        conversationId: 'frame-stat6',
        workspace: 'synonbiomed://proj_stat6',
      })
    );

    let handled = true;
    await act(async () => {
      handled = await result.current('https://example.com/report');
    });

    expect(handled).toBe(false);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(previewMocks.openPreview).not.toHaveBeenCalled();
  });

  it('opens a URL-encoded artifact reference by id instead of treating it as an external link', async () => {
    mockConversationArtifacts = [
      {
        kind: 'scientific_files',
        id: 'synonbiomed-files:frame-crbn',
        conversation_id: 'frame-crbn',
        status: 'active',
        payload: {
          project_id: 'proj_crbn',
          root_frame_id: 'frame-crbn',
          files: [
            scientificFile({
              artifact_id: 'f0af1025-7698-432a-b4ad-841099d2cd4b',
              version_id: 'version-crbn-report',
              filename: 'crbn_drug_discovery_report.md',
              content_type: 'text/markdown',
              preview_kind: 'markdown',
              content_url: '/api/artifacts/f0af1025-7698-432a-b4ad-841099d2cd4b/versions/version-crbn-report',
            }),
            scientificFile({
              artifact_id: 'newer-report-with-the-same-filename',
              version_id: 'newer-report-version',
              filename: 'crbn_drug_discovery_report.md',
              content_type: 'text/markdown',
              preview_kind: 'markdown',
              content_url: '/api/artifacts/newer-report-with-the-same-filename/versions/newer-report-version',
            }),
          ],
        },
        created_at: 1,
        updated_at: 1,
      },
    ];
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const url = String(input);
      if (url === '/api/artifacts/f0af1025-7698-432a-b4ad-841099d2cd4b/versions/version-crbn-report') {
        return new Response('# CRBN drug discovery report', { status: 200 });
      }
      return new Response('not found', { status: 404 });
    });
    vi.stubGlobal('fetch', fetchMock);
    const { result } = renderHook(() =>
      useSynonBiomedArtifactLinkPreview({
        conversationId: 'frame-crbn',
        workspace: 'synonbiomed://proj_crbn',
      })
    );

    let handled = false;
    await act(async () => {
      handled = await result.current('%7B%7Bartifact:f0af1025-7698-432a-b4ad-841099d2cd4b%7D%7D');
    });

    expect(handled).toBe(true);
    expect(previewMocks.openPreview).toHaveBeenCalledWith(
      '# CRBN drug discovery report',
      'markdown',
      expect.objectContaining({
        artifactId: 'f0af1025-7698-432a-b4ad-841099d2cd4b',
        title: 'crbn_drug_discovery_report.md',
        workspace: 'synonbiomed://proj_crbn',
      }),
      { presentation: 'board' }
    );
    expect(messageMocks.error).not.toHaveBeenCalled();
  });

  it('loads an exact body-link version when the projected message omitted artifact_refs', async () => {
    const versionId = 'version-historical-report';
    ipcMocks.listArtifacts.mockResolvedValue([
      {
        kind: 'scientific_files',
        id: 'synonbiomed-files:frame-history',
        conversation_id: 'frame-history',
        status: 'active',
        payload: {
          project_id: 'proj_history',
          root_frame_id: 'frame-history',
          files: [
            scientificFile({
              artifact_id: 'artifact-history-report',
              version_id: versionId,
              filename: 'final_report.md',
              content_type: 'text/markdown',
              preview_kind: 'markdown',
              content_url: `/api/artifacts/artifact-history-report/versions/${versionId}`,
            }),
          ],
        },
        created_at: 1,
        updated_at: 1,
      },
    ]);
    const fetchMock = vi.fn(async () => new Response('# Historical report', { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);

    const { result } = renderHook(() =>
      useSynonBiomedArtifactLinkPreview({
        conversationId: 'frame-history',
        workspace: 'synonbiomed://proj_history',
      })
    );

    await act(async () => {
      await result.current(`%7B%7Bartifact%3A${versionId}%7D%7D`);
    });

    expect(ipcMocks.listArtifacts).toHaveBeenCalledWith({
      conversation_id: 'frame-history',
      references: [],
      version_ids: [versionId],
    });
    expect(previewMocks.openPreview).toHaveBeenCalledWith(
      '# Historical report',
      'markdown',
      expect.objectContaining({
        artifactId: 'artifact-history-report',
        versionId,
        title: 'final_report.md',
      }),
      { presentation: 'board' }
    );
    expect(messageMocks.error).not.toHaveBeenCalled();
  });

  it('does not issue an extra version lookup when an artifact id is already in the message references', async () => {
    const reference = { artifact_id: 'artifact-window', version_id: 'version-window' };
    ipcMocks.listArtifacts.mockResolvedValue([
      {
        kind: 'scientific_files',
        id: 'synonbiomed-files:frame-window',
        conversation_id: 'frame-window',
        status: 'active',
        payload: {
          project_id: 'proj_window',
          root_frame_id: 'frame-window',
          files: [
            scientificFile({
              ...reference,
              filename: 'window.png',
              content_url: '/api/artifacts/artifact-window/versions/version-window',
            }),
          ],
        },
        created_at: 1,
        updated_at: 1,
      },
    ]);
    const { result } = renderHook(() =>
      useSynonBiomedArtifactResolver({
        conversationId: 'frame-window',
        workspace: 'synonbiomed://proj_window',
        artifactReferences: [reference],
      })
    );

    await expect(result.current.resolveImage('{{artifact:artifact-window}}')).resolves.toBe(
      '/api/artifacts/artifact-window/versions/version-window'
    );
    expect(ipcMocks.listArtifacts).toHaveBeenCalledWith({
      conversation_id: 'frame-window',
      references: [reference],
      version_ids: [],
    });
  });

  it('resolves a relative Markdown image to its real scientific artifact URL', async () => {
    mockConversationArtifacts = [
      {
        kind: 'scientific_files',
        id: 'synonbiomed-files:frame-stat6',
        conversation_id: 'frame-stat6',
        status: 'active',
        payload: {
          project_id: 'proj_stat6',
          root_frame_id: 'frame-stat6',
          files: [
            scientificFile({
              artifact_id: 'artifact-figure',
              version_id: 'version-figure',
              filename: 'coverage_vs_cells.png',
              content_url: '/api/artifacts/artifact-figure/versions/version-figure',
            }),
          ],
        },
        created_at: 1,
        updated_at: 1,
      },
    ];
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    const { result } = renderHook(() =>
      useSynonBiomedArtifactResolver({
        conversationId: 'frame-stat6',
        workspace: 'synonbiomed://proj_stat6',
      })
    );

    await expect(result.current.resolveImage('{{artifact:version-figure}}')).resolves.toBe(
      '/api/artifacts/artifact-figure/versions/version-figure'
    );
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('resolves an encoded artifact link to its exact version URL', async () => {
    mockConversationArtifacts = [
      {
        kind: 'scientific_files',
        id: 'synonbiomed-files:frame-link',
        conversation_id: 'frame-link',
        status: 'active',
        payload: {
          project_id: 'proj_link',
          root_frame_id: 'frame-link',
          files: [
            scientificFile({
              artifact_id: 'artifact-report',
              version_id: 'version-report',
              filename: 'final_report.md',
              content_type: 'text/markdown',
              preview_kind: 'markdown',
              content_url: '/api/artifacts/artifact-report/versions/version-report',
            }),
          ],
        },
        created_at: 1,
        updated_at: 1,
      },
    ];
    const { result } = renderHook(() =>
      useSynonBiomedArtifactResolver({
        conversationId: 'frame-link',
        workspace: 'synonbiomed://proj_link',
      })
    );

    await expect(result.current.resolveLinkHref('%7B%7Bartifact%3Aversion-report%7D%7D')).resolves.toBe(
      '/api/artifacts/artifact-report/versions/version-report'
    );
  });

  it('resolves historical version ids exactly while artifact ids and filenames use the current version', async () => {
    const oldVersion = scientificFile({
      artifact_id: 'artifact-report',
      version_id: 'version-old',
      version_number: 1,
      filename: 'report.png',
      content_url: '/api/artifacts/artifact-report/versions/version-old',
      created_at: 1,
      updated_at: 1,
    });
    const currentVersion = scientificFile({
      artifact_id: 'artifact-report',
      version_id: 'version-current',
      version_number: 2,
      filename: 'report.png',
      content_url: '/api/artifacts/artifact-report/versions/version-current',
      created_at: 2,
      updated_at: 2,
    });
    mockConversationArtifacts = [
      {
        kind: 'scientific_files',
        id: 'synonbiomed-files:frame-stat6',
        conversation_id: 'frame-stat6',
        status: 'active',
        payload: {
          project_id: 'proj_stat6',
          root_frame_id: 'frame-stat6',
          files: [currentVersion, oldVersion],
        },
        created_at: 1,
        updated_at: 2,
      },
    ];
    const { result } = renderHook(() =>
      useSynonBiomedArtifactResolver({
        conversationId: 'frame-stat6',
        workspace: 'synonbiomed://proj_stat6',
      })
    );

    await expect(result.current.resolveImage('{{artifact:version-old}}')).resolves.toBe(
      '/api/artifacts/artifact-report/versions/version-old'
    );
    await expect(result.current.resolveImage('{{artifact:artifact-report}}')).resolves.toBe(
      '/api/artifacts/artifact-report/versions/version-current'
    );
    await expect(result.current.resolveImage('report.png')).resolves.toBe(
      '/api/artifacts/artifact-report/versions/version-current'
    );
  });
});

function scientificFile(
  overrides: Partial<Extract<IConversationArtifact, { kind: 'scientific_files' }>['payload']['files'][number]>
): Extract<IConversationArtifact, { kind: 'scientific_files' }>['payload']['files'][number] {
  return {
    artifact_id: 'artifact',
    version_id: 'version',
    version_number: 1,
    project_id: 'project',
    root_frame_id: 'frame',
    frame_id: 'frame',
    creating_frame_id: 'frame',
    filename: 'artifact.bin',
    content_type: 'application/octet-stream',
    size_bytes: 128,
    preview_kind: 'binary',
    content_url: '/api/artifacts/artifact/versions/version',
    created_at: 1,
    updated_at: 1,
    agent_name: 'OPERON',
    is_user_upload: false,
    is_intermediate: false,
    ...overrides,
  };
}
