/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest';
import { act, cleanup } from '@testing-library/react';
import React from 'react';
import { renderWithI18n } from '../i18nTestUtils';

const htmlInfo = vi.hoisted(() => vi.fn());

vi.mock('@/common', () => ({
  ipcBridge: {
    fs: {
      getImageBase64: { invoke: vi.fn(() => Promise.resolve('')) },
      readFile: { invoke: vi.fn(() => Promise.resolve('')) },
    },
  },
}));

vi.mock('@monaco-editor/react', () => ({
  default: ({ value }: { value: string }) => <div data-testid='monaco-editor'>{value}</div>,
}));

vi.mock('@arco-design/web-react', () => ({
  Message: {
    useMessage: () => [{ info: htmlInfo, success: vi.fn(), error: vi.fn() }, null],
  },
}));

import HTMLViewer from '@/renderer/pages/conversation/Preview/components/viewers/HTMLViewer';
import HTMLRenderer from '@/renderer/pages/conversation/Preview/components/renderers/HTMLRenderer';

beforeEach(() => vi.clearAllMocks());
afterEach(cleanup);

describe('HTMLViewer', () => {
  it('renders iframe with HTML content', async () => {
    const { container } = await renderWithI18n(<HTMLViewer content='<h1>Test</h1>' />, 'en-US');
    const iframe = container.querySelector('iframe');
    expect(iframe).toBeInTheDocument();
    expect(iframe).toHaveAttribute('title', 'HTML preview');
  });

  it('hides toolbar when hideToolbar is true', async () => {
    const { container } = await renderWithI18n(<HTMLViewer content='<h1>Test</h1>' hideToolbar />);
    expect(container.querySelector('[class*="toolbar"]')).not.toBeInTheDocument();
  });

  it('accepts file_path prop', async () => {
    const { container } = await renderWithI18n(<HTMLViewer content='<h1>Test</h1>' file_path='/test/index.html' />);
    expect(container.querySelector('iframe')).toBeInTheDocument();
  });

  it('accepts inspector messages only from its own iframe', async () => {
    const { container } = await renderWithI18n(<HTMLViewer content='<h1>Test</h1>' />, 'en-US');
    const iframe = container.querySelector('iframe');
    expect(iframe?.contentWindow).toBeTruthy();
    const instance = /const instance = "([^"]+)"/.exec(iframe?.srcdoc ?? '')?.[1];
    expect(instance).toBeTruthy();

    window.dispatchEvent(
      new MessageEvent('message', {
        source: window,
        data: { type: 'element-selected', data: { path: 'body > h1', html: '<h1>Forged</h1>' } },
      })
    );
    expect(htmlInfo).not.toHaveBeenCalled();

    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: iframe?.contentWindow,
          data: {
            __synonPreviewInstance: instance,
            payload: { type: 'element-selected', data: { path: 'body > h1', html: '<h1>Test</h1>' } },
          },
        })
      );
    });
    expect(htmlInfo).toHaveBeenCalledWith('Selected element: body > h1');
  });
});

describe('HTMLRenderer', () => {
  it('renders clean local HTML through a sandboxed browser iframe', async () => {
    const { container } = await renderWithI18n(
      <HTMLRenderer
        content='<script src="https://cdn.example.com/app.js"></script><script>localStorage.getItem("theme")</script>'
        file_path='/workspace/financial-wechat-miniapp.html'
      />,
      'en-US'
    );

    const iframe = container.querySelector('iframe');
    expect(iframe).toBeInTheDocument();
    expect(iframe).toHaveAttribute('sandbox', 'allow-scripts');
    expect(iframe).toHaveAttribute('title', 'HTML preview');
    expect(iframe?.getAttribute('srcdoc')).toContain('cdn.example.com/app.js');
    expect(container.querySelector('webview')).not.toBeInTheDocument();
  });

  it('passively sanitizes a source document and rejects stale instance messages', async () => {
    const onElementSelected = vi.fn();
    const { container } = await renderWithI18n(
      <HTMLRenderer
        content='<h1 id="source">Source</h1><script>parent.pwned=true</script><img src="https://remote.example/track" onerror="parent.pwned=true">'
        passiveSource
        onElementSelected={onElementSelected}
      />
    );
    const frame = container.querySelector('iframe');
    expect(frame?.srcdoc).not.toContain('parent.pwned');
    expect(frame?.srcdoc).toContain("default-src 'none'");
    expect(frame).toHaveAttribute('sandbox', 'allow-scripts');
    window.dispatchEvent(
      new MessageEvent('message', {
        source: frame?.contentWindow,
        data: {
          __synonPreviewInstance: 'stale-instance',
          payload: { __SYNON_AI_INSPECT_ELEMENT__: { html: '<h1>Forged</h1>', tag: 'h1' } },
        },
      })
    );
    expect(onElementSelected).not.toHaveBeenCalled();
  });

  it('keeps dirty local HTML content in the browser iframe', async () => {
    const dirtyProps = {
      content: '<h1>Unsaved edit</h1>',
      file_path: '/workspace/index.html',
      isDirty: true,
    } as React.ComponentProps<typeof HTMLRenderer> & { isDirty: boolean };

    const { container } = await renderWithI18n(<HTMLRenderer {...dirtyProps} />);

    const iframe = container.querySelector('iframe');
    expect(iframe).toBeInTheDocument();
    expect(iframe?.getAttribute('srcdoc')).toContain('Unsaved edit');
    expect(container.querySelector('webview')).not.toBeInTheDocument();
  });
});
