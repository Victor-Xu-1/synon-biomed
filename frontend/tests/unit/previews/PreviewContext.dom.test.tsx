/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';
import { renderHook, act, cleanup } from '@testing-library/react';
import React, { type ReactNode } from 'react';
import { I18nextProvider } from 'react-i18next';
import type { i18n } from 'i18next';
import { createTestI18n } from '../i18nTestUtils';
import { ipcBridge } from '@/common';
import { emitter } from '@/renderer/utils/emitter';
import {
  PreviewProvider,
  shouldPollPreviewFileMetadata,
  usePreviewContext,
} from '@/renderer/pages/conversation/Preview/context/PreviewContext';

vi.mock('@/common', () => ({
  ipcBridge: {
    fileStream: {
      contentUpdate: { on: vi.fn(() => vi.fn()) },
    },
    preview: {
      open: { on: vi.fn(() => vi.fn()) },
    },
    fs: {
      writeFile: { invoke: vi.fn() },
      getFileMetadata: { invoke: vi.fn() },
      readFile: { invoke: vi.fn() },
      getImageBase64: { invoke: vi.fn() },
    },
  },
}));

vi.mock('@/renderer/utils/emitter', () => ({
  emitter: {
    on: vi.fn(),
    off: vi.fn(),
  },
}));

describe('PreviewContext', () => {
  let testI18n: i18n;
  const wrapper = ({ children }: { children: ReactNode }) => (
    <I18nextProvider i18n={testI18n}>
      <PreviewProvider>{children}</PreviewProvider>
    </I18nextProvider>
  );

  beforeEach(async () => {
    testI18n = await createTestI18n('en-US');
    vi.clearAllMocks();
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it('initializes with closed state', () => {
    const { result } = renderHook(() => usePreviewContext(), { wrapper });
    expect(result.current.isOpen).toBe(false);
    expect(result.current.tabs).toEqual([]);
    expect(result.current.activeTabId).toBe(null);
    expect(result.current.presentationMode).toBe('single');
  });

  it('uses browser-native preview channels without unavailable desktop IPC subscriptions', () => {
    renderHook(() => usePreviewContext(), { wrapper });

    expect(emitter.on).toHaveBeenCalledWith('preview.open', expect.any(Function));
    expect(ipcBridge.fileStream.contentUpdate.on).not.toHaveBeenCalled();
    expect(ipcBridge.preview.open.on).not.toHaveBeenCalled();
  });

  it('opens preview and creates tab', () => {
    const { result } = renderHook(() => usePreviewContext(), { wrapper });
    act(() => {
      result.current.openPreview('# Hello', 'markdown', { title: 'test.md' });
    });
    expect(result.current.isOpen).toBe(true);
    expect(result.current.tabs).toHaveLength(1);
    expect(result.current.tabs[0].content).toBe('# Hello');
    expect(result.current.tabs[0].content_type).toBe('markdown');
  });

  it('parses persisted preview state only once across provider rerenders', () => {
    const getItem = vi.spyOn(Storage.prototype, 'getItem');
    const { result, rerender } = renderHook(() => usePreviewContext(), { wrapper });
    const readsAfterMount = getItem.mock.calls.length;

    act(() => {
      result.current.openPreview('# Changed', 'markdown', { title: 'changed.md' });
    });
    rerender();

    expect(getItem).toHaveBeenCalled();
    expect(getItem).toHaveBeenCalledTimes(readsAfterMount);
    getItem.mockRestore();
  });

  it('translates generated tab titles when the product language changes', async () => {
    const { result } = renderHook(() => usePreviewContext(), { wrapper });
    act(() => {
      result.current.openPreview('data:image/png;base64,AAAA', 'image');
    });
    expect(result.current.tabs[0].title).toBe('Image');

    await act(async () => {
      await testI18n.changeLanguage('zh-CN');
    });
    expect(result.current.tabs[0].title).toBe('图片');
  });

  it('closes preview and clears all tabs', () => {
    const { result } = renderHook(() => usePreviewContext(), { wrapper });
    act(() => {
      result.current.openPreview('content', 'code');
    });
    act(() => {
      result.current.closePreview();
    });
    expect(result.current.isOpen).toBe(false);
    expect(result.current.tabs).toEqual([]);
    expect(result.current.presentationMode).toBe('single');
  });

  it('accumulates project files in board mode without duplicating the same file', () => {
    const { result } = renderHook(() => usePreviewContext(), { wrapper });

    act(() => {
      result.current.openPreview(
        '# First',
        'markdown',
        { file_name: 'first.md', file_path: '/project/first.md' },
        {
          presentation: 'board',
        }
      );
    });
    const firstTabId = result.current.tabs[0].id;
    expect(result.current.presentationMode).toBe('board');
    expect(result.current.activeTabId).toBe(firstTabId);

    act(() => {
      result.current.openPreview(
        'data:image/png;base64,AAAA',
        'image',
        {
          file_name: 'second.png',
          file_path: '/project/second.png',
        },
        {
          presentation: 'board',
        }
      );
    });
    const secondTabId = result.current.tabs[1].id;
    expect(result.current.tabs.map((tab) => tab.title)).toEqual(['first.md', 'second.png']);
    expect(result.current.activeTabId).toBe(secondTabId);

    act(() => {
      result.current.openPreview(
        '# First updated',
        'markdown',
        {
          file_name: 'first.md',
          file_path: '/project/first.md',
        },
        {
          presentation: 'board',
        }
      );
    });
    expect(result.current.tabs).toHaveLength(2);
    expect(result.current.activeTabId).toBe(firstTabId);
    expect(result.current.tabs[0].content).toBe('# First updated');

    act(() => result.current.closeTab(secondTabId));
    expect(result.current.isOpen).toBe(true);
    expect(result.current.presentationMode).toBe('single');

    act(() => result.current.closeTab(firstTabId));
    expect(result.current.isOpen).toBe(false);
    expect(result.current.presentationMode).toBe('single');
  });

  it('keeps board presentation when another file entry opens without explicit presentation options', () => {
    const { result } = renderHook(() => usePreviewContext(), { wrapper });

    act(() => {
      result.current.openPreview('# First', 'markdown', { file_name: 'first.md' }, { presentation: 'board' });
    });
    act(() => {
      result.current.openPreview('# Second', 'markdown', { file_name: 'second.md' });
    });

    expect(result.current.presentationMode).toBe('board');
    expect(result.current.tabs.map((tab) => tab.title)).toEqual(['first.md', 'second.md']);
  });

  it('keeps different immutable versions of the same artifact in separate board tiles', () => {
    const { result } = renderHook(() => usePreviewContext(), { wrapper });

    act(() => {
      result.current.openPreview(
        '# Version 1',
        'markdown',
        {
          file_name: 'report.md',
          artifactId: 'artifact-report',
          versionId: 'version-1',
          contentUrl: '/api/artifacts/artifact-report/versions/version-1',
        },
        { presentation: 'board' }
      );
      result.current.openPreview(
        '# Version 2',
        'markdown',
        {
          file_name: 'report.md',
          artifactId: 'artifact-report',
          versionId: 'version-2',
          contentUrl: '/api/artifacts/artifact-report/versions/version-2',
        },
        { presentation: 'board' }
      );
    });

    expect(result.current.tabs).toHaveLength(2);
    expect(result.current.tabs.map((tab) => tab.metadata?.versionId)).toEqual(['version-1', 'version-2']);
  });

  it('provides all context API methods', () => {
    const { result } = renderHook(() => usePreviewContext(), { wrapper });
    expect(typeof result.current.openPreview).toBe('function');
    expect(typeof result.current.closePreview).toBe('function');
    expect(typeof result.current.updateContent).toBe('function');
    expect(typeof result.current.findPreviewTab).toBe('function');
  });

  it('updates content and marks tab as dirty', () => {
    const { result } = renderHook(() => usePreviewContext(), { wrapper });
    act(() => {
      result.current.openPreview('original', 'code');
    });
    expect(result.current.activeTab?.isDirty).toBe(false);
    act(() => {
      result.current.updateContent('modified');
    });
    expect(result.current.activeTab?.content).toBe('modified');
    expect(result.current.activeTab?.isDirty).toBe(true);
  });

  it('polls local files but never routes remote artifacts through local filesystem metadata', () => {
    expect(
      shouldPollPreviewFileMetadata({
        id: 'local',
        title: 'local.csv',
        content: '',
        content_type: 'table',
        metadata: { file_path: '/workspace/local.csv', workspace: '/workspace' },
      })
    ).toBe(true);
    expect(
      shouldPollPreviewFileMetadata(
        {
          id: 'hidden-local',
          title: 'local.csv',
          content: '',
          content_type: 'table',
          metadata: { file_path: '/workspace/local.csv', workspace: '/workspace' },
        },
        'hidden'
      )
    ).toBe(false);
    expect(
      shouldPollPreviewFileMetadata({
        id: 'remote',
        title: 'remote.smi',
        content: '',
        content_type: 'molecule',
        metadata: {
          file_path: 'synonbiomed://proj_example/project-files/artifact-1',
          workspace: 'synonbiomed://proj_example',
          artifactId: 'artifact-1',
          contentUrl: '/api/artifacts/artifact-1',
        },
      })
    ).toBe(false);
  });
});
