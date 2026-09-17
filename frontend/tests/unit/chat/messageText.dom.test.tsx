/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React from 'react';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { IMessageText } from '@/common/chat/chatLib';
import { ipcBridge } from '@/common';
import { ConversationProvider } from '@/renderer/hooks/context/ConversationContext';
import MessageText from '@/renderer/pages/conversation/Messages/components/MessageText';
import {
  LARGE_TEXT_PREVIEW_MAX_LENGTH,
  LARGE_TEXT_PREVIEW_THRESHOLD,
} from '@/renderer/pages/conversation/Preview/constants';

const previewMocks = vi.hoisted(() => ({
  openPreview: vi.fn(),
}));
const artifactLinkMocks = vi.hoisted(() => ({
  handle: vi.fn().mockResolvedValue(true),
}));
const localFileLinkMocks = vi.hoisted(() => ({
  payload: {
    path: '/missing/report.xlsx',
    reference: undefined as
      | {
          filePath: string;
          rawReference: string;
          line?: number;
          column?: number;
          endLine?: number;
        }
      | undefined,
  },
}));
const mockFilePreview = vi.fn(({ path }: { path: string }) => <div data-testid='file-preview'>{path}</div>);

vi.mock('@/common', () => ({
  ipcBridge: {
    fs: {
      getFileMetadata: { invoke: vi.fn() },
      getImageBase64: { invoke: vi.fn() },
      readFile: { invoke: vi.fn() },
    },
  },
}));

vi.mock('@/renderer/pages/conversation/Preview/context/PreviewContext', () => ({
  usePreviewContext: () => ({
    openPreview: previewMocks.openPreview,
  }),
}));
vi.mock('@/renderer/pages/conversation/Messages/useSynonBiomedArtifactLinkPreview', () => ({
  useSynonBiomedArtifactLinkPreview: () => artifactLinkMocks.handle,
  useSynonBiomedArtifactResolver: () => ({
    handleLink: artifactLinkMocks.handle,
    resolveImage: vi.fn().mockResolvedValue(null),
  }),
}));

vi.mock('@/renderer/components/chat/CollapsibleContent', () => ({
  __esModule: true,
  default: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}));

vi.mock('@/renderer/components/media/FilePreview', () => ({
  __esModule: true,
  default: (props: { path: string }) => mockFilePreview(props),
}));

vi.mock('@/renderer/components/media/HorizontalFileList', () => ({
  __esModule: true,
  default: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
}));

vi.mock('@/renderer/components/Markdown', () => ({
  __esModule: true,
  default: ({
    children,
    onLocalFileLink,
    onLink,
  }: {
    children?: React.ReactNode;
    onLink?: (href: string) => boolean | Promise<boolean>;
    onLocalFileLink?: (
      path: string,
      reference?: {
        filePath: string;
        rawReference: string;
        line?: number;
        column?: number;
        endLine?: number;
      }
    ) => void | Promise<void>;
  }) => (
    <div>
      {children}
      {onLocalFileLink && (
        <button
          type='button'
          onClick={() => void onLocalFileLink(localFileLinkMocks.payload.path, localFileLinkMocks.payload.reference)}
        >
          open local file
        </button>
      )}{' '}
      {onLink && (
        <button type='button' onClick={() => void onLink('README.md')}>
          open artifact link
        </button>
      )}
    </div>
  ),
}));

vi.mock('@/renderer/utils/chat/skillSuggestParser', () => ({
  hasSkillSuggest: () => false,
  stripSkillSuggest: (content: string) => content,
}));

vi.mock('@/renderer/utils/chat/thinkTagFilter', () => ({
  hasThinkTags: () => false,
  stripThinkTags: (content: string) => content,
}));

vi.mock('@/renderer/utils/synonBiomed/runtime/runtimeLogo', () => ({
  useAgentLogos: () => ({}),
  resolveAgentLogo: () => null,
}));

vi.mock('@/renderer/utils/ui/clipboard', () => ({
  copyText: vi.fn().mockResolvedValue(undefined),
}));

vi.mock('@arco-design/web-react', () => ({
  Alert: ({ title, content }: { title?: React.ReactNode; content?: React.ReactNode }) => (
    <div data-testid='terminal-failure-alert'>
      {title}
      {content}
    </div>
  ),
  Message: {
    error: vi.fn(),
    useMessage: () => [{ error: vi.fn(), success: vi.fn() }, null],
  },
  Tooltip: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}));

vi.mock('@icon-park/react', () => ({
  Copy: () => <span data-testid='copy-icon' />,
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? key,
  }),
}));

const fileMetadata = (path: string) => ({
  name: path.split(/[\\/]/).pop() || path,
  path,
  size: 128,
  type: 'file',
  lastModified: 1_717_000_000,
});

describe('MessageText attachment paths', () => {
  beforeEach(() => {
    previewMocks.openPreview.mockClear();
    artifactLinkMocks.handle.mockClear();
    localFileLinkMocks.payload = {
      path: '/missing/report.xlsx',
      reference: undefined,
    };
    vi.mocked(ipcBridge.fs.getFileMetadata.invoke).mockReset();
    vi.mocked(ipcBridge.fs.getImageBase64.invoke).mockReset();
    vi.mocked(ipcBridge.fs.readFile.invoke).mockReset();
  });

  const renderMessageWithLocalLink = (content = '[report](/missing/report.xlsx)') => {
    const message: IMessageText = {
      id: 'msg-local-link',
      msg_id: 'msg-local-link',
      conversation_id: 'conv-1',
      type: 'text',
      position: 'left',
      createdAt: Date.now(),
      content: {
        content,
      },
    };

    render(
      <ConversationProvider
        value={{
          conversationId: 'conv-1',
          workspace: '/workspace/demo',
          type: 'acp',
        }}
      >
        <MessageText message={message} />
      </ConversationProvider>
    );
  };

  it('passes relative assistant links to the Synon Biomed artifact preview handler', () => {
    const message: IMessageText = {
      id: 'msg-artifact-link',
      msg_id: 'msg-artifact-link',
      conversation_id: 'frame-stat6',
      type: 'text',
      position: 'left',
      content: { content: '[README](README.md)' },
    };

    render(
      <ConversationProvider
        value={{
          conversation_id: 'frame-stat6',
          workspace: 'synonbiomed://proj_stat6',
          type: 'acp',
        }}
      >
        <MessageText message={message} />
      </ConversationProvider>
    );

    fireEvent.click(screen.getByRole('button', { name: 'open artifact link' }));
    expect(artifactLinkMocks.handle).toHaveBeenCalledWith('README.md');
  });

  it('presents standalone durable artifact markers as named files and images without leaking identities', () => {
    const imageVersion = '8763aa50-0000-4000-8000-000000000001';
    const tableVersion = '8763aa50-0000-4000-8000-000000000002';
    const missingVersion = '8763aa50-0000-4000-8000-000000000003';
    const message: IMessageText = {
      id: 'msg-standalone-artifacts',
      msg_id: 'msg-standalone-artifacts',
      conversation_id: 'frame-artifacts',
      type: 'text',
      position: 'left',
      content: {
        content: `Results\n\n{{artifact:${imageVersion}}}\n\n{{artifact:${tableVersion}}}\n\n{{artifact:${missingVersion}}}`,
      },
      artifact_refs: [
        {
          artifact_id: 'artifact-image',
          version_id: imageVersion,
          relation: 'produced',
          filename: 'umap_clusters.png',
          content_type: 'image/png',
        },
        {
          artifact_id: 'artifact-table',
          version_id: tableVersion,
          relation: 'produced',
          filename: 'cluster_markers.csv',
          content_type: 'text/csv',
        },
      ],
    };

    render(
      <ConversationProvider
        value={{
          conversation_id: 'frame-artifacts',
          workspace: 'synonbiomed://project-artifacts',
          type: 'acp',
        }}
      >
        <MessageText message={message} />
      </ConversationProvider>
    );

    const content = screen.getByTestId('message-text-content');
    expect(content).toHaveTextContent(`![umap_clusters.png]({{artifact:${imageVersion}}})`);
    expect(content).toHaveTextContent(`[cluster_markers.csv]({{artifact:${tableVersion}}})`);
    expect(content).not.toHaveTextContent(missingVersion);
  });

  it('marks a rejected terminal candidate so completion-looking prose cannot contradict the task capsule', () => {
    const failed: IMessageText = {
      id: 'msg-failed',
      msg_id: 'msg-failed',
      conversation_id: 'conv-1',
      type: 'text',
      position: 'left',
      status: 'error',
      terminal_status: 'failed',
      content: { content: 'partial answer' },
    };
    const view = render(
      <ConversationProvider
        value={{
          conversationId: 'conv-1',
          workspace: '/workspace/demo',
          type: 'acp',
        }}
      >
        <MessageText message={failed} />
      </ConversationProvider>
    );
    expect(screen.getByText('partial answer')).toBeInTheDocument();
    expect(screen.getByTestId('terminal-failure-alert')).toHaveTextContent(
      'conversation.synonRuntime.runtimeOperations.failureResultRejected'
    );
    expect(screen.getByTestId('terminal-failure-alert')).toHaveTextContent(
      'conversation.synonRuntime.runtimeOperations.assistantResponseIncomplete'
    );

    view.rerender(
      <ConversationProvider
        value={{
          conversationId: 'conv-1',
          workspace: '/workspace/demo',
          type: 'acp',
        }}
      >
        <MessageText message={{ ...failed, status: 'finish', terminal_superseded: true }} />
      </ConversationProvider>
    );
    expect(screen.queryByTestId('terminal-failure-alert')).not.toBeInTheDocument();
  });

  it('does not render a durable cancellation reason as assistant content', () => {
    const cancelled: IMessageText = {
      id: 'msg-cancelled',
      msg_id: 'msg-cancelled',
      conversation_id: 'conv-1',
      type: 'text',
      position: 'left',
      status: 'finish',
      terminal_status: 'cancelled',
      content: { content: 'user_cancelled' },
    };
    const view = render(
      <ConversationProvider
        value={{
          conversationId: 'conv-1',
          workspace: '/workspace/demo',
          type: 'acp',
        }}
      >
        <MessageText message={cancelled} />
      </ConversationProvider>
    );

    expect(screen.queryByText('user_cancelled')).not.toBeInTheDocument();
    expect(screen.queryByTestId('message-text-content')).not.toBeInTheDocument();

    view.rerender(
      <ConversationProvider
        value={{
          conversationId: 'conv-1',
          workspace: '/workspace/demo',
          type: 'acp',
        }}
      >
        <MessageText message={{ ...cancelled, position: 'right' }} />
      </ConversationProvider>
    );
    expect(screen.getByText('user_cancelled')).toBeInTheDocument();
  });

  it('resolves relative attachment paths against the current workspace before previewing', () => {
    const message: IMessageText = {
      id: 'msg-1',
      msg_id: 'msg-1',
      conversation_id: 'conv-1',
      type: 'text',
      position: 'right',
      createdAt: Date.now(),
      content: {
        content: 'look at this\n\n[[SYNON_AI_FILES]]\nuploads/photo.png',
      },
    };

    render(
      <ConversationProvider
        value={{
          conversationId: 'conv-1',
          workspace: '/workspace/demo',
          type: 'acp',
        }}
      >
        <MessageText message={message} />
      </ConversationProvider>
    );

    expect(screen.getByTestId('file-preview')).toHaveTextContent('/workspace/demo/uploads/photo.png');
  });

  it('lets text message content use the available row width on desktop', () => {
    const message: IMessageText = {
      id: 'msg-width',
      msg_id: 'msg-width',
      conversation_id: 'conv-1',
      type: 'text',
      position: 'left',
      createdAt: Date.now(),
      content: {
        content: 'wide content',
      },
    };

    render(
      <ConversationProvider
        value={{
          conversationId: 'conv-1',
          workspace: '/workspace/demo',
          type: 'acp',
        }}
      >
        <MessageText message={message} />
      </ConversationProvider>
    );

    const content = screen.getByTestId('message-text-content');
    expect(content.parentElement?.className).toContain('min-w-0');
    expect(content.parentElement?.className).not.toContain('max-w-780px');
  });

  it('keeps absolute attachment paths unchanged before previewing', () => {
    const message: IMessageText = {
      id: 'msg-2',
      msg_id: 'msg-2',
      conversation_id: 'conv-1',
      type: 'text',
      position: 'right',
      createdAt: Date.now(),
      content: {
        content: 'look at this\n\n[[SYNON_AI_FILES]]\n/Users/demo/Desktop/photo.png',
      },
    };

    render(
      <ConversationProvider
        value={{
          conversationId: 'conv-1',
          workspace: '/workspace/demo',
          type: 'acp',
        }}
      >
        <MessageText message={message} />
      </ConversationProvider>
    );

    expect(screen.getByTestId('file-preview')).toHaveTextContent('/Users/demo/Desktop/photo.png');
  });

  it('opens a missing-file preview when a local markdown link no longer exists', async () => {
    vi.mocked(ipcBridge.fs.getFileMetadata.invoke).mockResolvedValue(null);
    localFileLinkMocks.payload = {
      path: '/missing/report.xlsx',
      reference: {
        filePath: '/missing/report.xlsx',
        rawReference: '/missing/report.xlsx:10:2',
        line: 10,
        column: 2,
      },
    };

    renderMessageWithLocalLink();

    fireEvent.click(screen.getByRole('button', { name: 'open local file' }));

    await waitFor(() => {
      expect(previewMocks.openPreview).toHaveBeenCalledWith(
        '',
        'excel',
        expect.objectContaining({
          file_name: 'report.xlsx',
          file_path: '/missing/report.xlsx',
          missingFile: true,
          editable: false,
          targetLine: 10,
          targetColumn: 2,
        }),
        { presentation: 'board' }
      );
    });
  });

  it('opens an existing code local markdown link with read content and target location', async () => {
    const filePath = '/workspace/demo/src/app.ts';
    localFileLinkMocks.payload = {
      path: filePath,
      reference: {
        filePath,
        rawReference: `${filePath}:42:7`,
        line: 42,
        column: 7,
      },
    };
    vi.mocked(ipcBridge.fs.getFileMetadata.invoke).mockResolvedValue(fileMetadata(filePath));
    vi.mocked(ipcBridge.fs.readFile.invoke).mockResolvedValue('const value = 1;\n');

    renderMessageWithLocalLink('[app.ts](/workspace/demo/src/app.ts:42:7)');

    fireEvent.click(screen.getByRole('button', { name: 'open local file' }));

    await waitFor(() => {
      expect(previewMocks.openPreview).toHaveBeenCalledWith(
        'const value = 1;\n',
        'code',
        expect.objectContaining({
          file_name: 'app.ts',
          file_path: filePath,
          workspace: '/workspace/demo',
          language: 'ts',
          targetLine: 42,
          targetColumn: 7,
          truncated: false,
        }),
        { presentation: 'board' }
      );
    });
  });

  it('opens hash range local markdown links with only the start line in preview metadata', async () => {
    const filePath = '/workspace/demo/src/app.ts';
    localFileLinkMocks.payload = {
      path: filePath,
      reference: {
        filePath,
        rawReference: `${filePath}#L10-L20`,
        line: 10,
        endLine: 20,
      },
    };
    vi.mocked(ipcBridge.fs.getFileMetadata.invoke).mockResolvedValue(fileMetadata(filePath));
    vi.mocked(ipcBridge.fs.readFile.invoke).mockResolvedValue('const value = 1;\n');

    renderMessageWithLocalLink('[app.ts](/workspace/demo/src/app.ts#L10-L20)');

    fireEvent.click(screen.getByRole('button', { name: 'open local file' }));

    await waitFor(() => {
      expect(previewMocks.openPreview).toHaveBeenCalledWith(
        'const value = 1;\n',
        'code',
        expect.objectContaining({
          file_name: 'app.ts',
          file_path: filePath,
          workspace: '/workspace/demo',
          language: 'ts',
          targetLine: 10,
          targetColumn: undefined,
          truncated: false,
        }),
        { presentation: 'board' }
      );
    });

    const metadata = previewMocks.openPreview.mock.calls[0]?.[2];
    expect(metadata).not.toHaveProperty('endLine');
    expect(metadata).not.toHaveProperty('targetEndLine');
  });

  it('opens office and pdf local markdown links without reading file content', async () => {
    const filePath = '/workspace/demo/reports/q2.pdf';
    localFileLinkMocks.payload = { path: filePath, reference: undefined };
    vi.mocked(ipcBridge.fs.getFileMetadata.invoke).mockResolvedValue(fileMetadata(filePath));

    renderMessageWithLocalLink('[q2.pdf](/workspace/demo/reports/q2.pdf)');

    fireEvent.click(screen.getByRole('button', { name: 'open local file' }));

    await waitFor(() => {
      expect(previewMocks.openPreview).toHaveBeenCalledWith(
        '',
        'pdf',
        expect.objectContaining({
          file_name: 'q2.pdf',
          file_path: filePath,
          workspace: '/workspace/demo',
          language: 'pdf',
        }),
        { presentation: 'board' }
      );
    });
    expect(ipcBridge.fs.readFile.invoke).not.toHaveBeenCalled();
    expect(ipcBridge.fs.getImageBase64.invoke).not.toHaveBeenCalled();
  });

  it('opens image local markdown links from base64 content without reading text content', async () => {
    const filePath = '/workspace/demo/assets/chart.png';
    localFileLinkMocks.payload = { path: filePath, reference: undefined };
    vi.mocked(ipcBridge.fs.getFileMetadata.invoke).mockResolvedValue(fileMetadata(filePath));
    vi.mocked(ipcBridge.fs.getImageBase64.invoke).mockResolvedValue('data:image/png;base64,abc123');

    renderMessageWithLocalLink('[chart.png](/workspace/demo/assets/chart.png)');

    fireEvent.click(screen.getByRole('button', { name: 'open local file' }));

    await waitFor(() => {
      expect(previewMocks.openPreview).toHaveBeenCalledWith(
        'data:image/png;base64,abc123',
        'image',
        expect.objectContaining({
          file_name: 'chart.png',
          file_path: filePath,
          workspace: '/workspace/demo',
          language: 'png',
          editable: false,
        }),
        { presentation: 'board' }
      );
    });
    expect(ipcBridge.fs.readFile.invoke).not.toHaveBeenCalled();
  });

  it('opens large code local markdown links with truncated read content', async () => {
    const filePath = '/workspace/demo/logs/app.log';
    const content = 'a'.repeat(LARGE_TEXT_PREVIEW_THRESHOLD + 1);
    localFileLinkMocks.payload = { path: filePath, reference: undefined };
    vi.mocked(ipcBridge.fs.getFileMetadata.invoke).mockResolvedValue(fileMetadata(filePath));
    vi.mocked(ipcBridge.fs.readFile.invoke).mockResolvedValue(content);

    renderMessageWithLocalLink('[app.log](/workspace/demo/logs/app.log)');

    fireEvent.click(screen.getByRole('button', { name: 'open local file' }));

    await waitFor(() => {
      expect(previewMocks.openPreview).toHaveBeenCalledWith(
        content.slice(0, LARGE_TEXT_PREVIEW_MAX_LENGTH),
        'code',
        expect.objectContaining({
          file_name: 'app.log',
          file_path: filePath,
          truncated: true,
          editable: false,
        }),
        { presentation: 'board' }
      );
    });
  });
});
