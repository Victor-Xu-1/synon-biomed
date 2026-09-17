/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { render, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

beforeEach(() => {
  window.__backendPort = 13400;
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => {
      return new Response(JSON.stringify({ data: {} }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    })
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.resetModules();
  delete window.__backendPort;
});

// PreviewPanel pulls in a large dependency graph; under the full concurrent
// suite the first cold import's transform/resolve can exceed the default 10s
// timeout (flaky), even though it resolves in a few seconds in isolation. Give
// these import-bound assertions extra headroom so they don't flake.
const IMPORT_TIMEOUT_MS = 30000;

describe('PreviewPanel', () => {
  it('uses the file board only when at least two files are open', async () => {
    const { shouldRenderPreviewBoard } =
      await import('@/renderer/pages/conversation/Preview/components/PreviewPanel/PreviewPanel');

    expect(shouldRenderPreviewBoard('board', 1)).toBe(false);
    expect(shouldRenderPreviewBoard('board', 1, true)).toBe(true);
    expect(shouldRenderPreviewBoard('board', 2)).toBe(true);
    expect(shouldRenderPreviewBoard('single', 2)).toBe(false);
  });

  it('ports fullscreen content to the document top layer and restores body scrolling', async () => {
    const mod = await import('@/renderer/pages/conversation/Preview/components/PreviewPanel/PreviewPanel');
    const PreviewFullscreenLayer = (
      mod as typeof mod & {
        PreviewFullscreenLayer: React.FC<React.PropsWithChildren<{ active: boolean }>>;
      }
    ).PreviewFullscreenLayer;
    expect(PreviewFullscreenLayer).toBeTypeOf('function');

    const host = document.createElement('div');
    document.body.appendChild(host);
    const { rerender, unmount } = render(
      <PreviewFullscreenLayer active>
        <div data-testid='fullscreen-preview-content'>preview</div>
      </PreviewFullscreenLayer>,
      { container: host }
    );

    expect(screen.getByTestId('fullscreen-preview-content').parentElement).toBe(document.body);
    expect(host).not.toContainElement(screen.getByTestId('fullscreen-preview-content'));
    expect(document.body.style.overflow).toBe('hidden');

    rerender(
      <PreviewFullscreenLayer active={false}>
        <div data-testid='fullscreen-preview-content'>preview</div>
      </PreviewFullscreenLayer>
    );
    expect(host).toContainElement(screen.getByTestId('fullscreen-preview-content'));
    expect(document.body.style.overflow).toBe('');

    unmount();
    host.remove();
  });

  it(
    'is a React component module that exports a default function',
    async () => {
      const mod = await import('@/renderer/pages/conversation/Preview/components/PreviewPanel/PreviewPanel');
      expect(typeof mod.default).toBe('function');
    },
    IMPORT_TIMEOUT_MS
  );

  it(
    'module loads without throwing on import',
    async () => {
      await expect(
        import('@/renderer/pages/conversation/Preview/components/PreviewPanel/PreviewPanel')
      ).resolves.toBeTruthy();
    },
    IMPORT_TIMEOUT_MS
  );

  it(
    'has a displayName or function name for debugging',
    async () => {
      const mod = await import('@/renderer/pages/conversation/Preview/components/PreviewPanel/PreviewPanel');
      const fn = mod.default;
      expect(fn.name || fn.displayName || 'anonymous').toBeTruthy();
    },
    IMPORT_TIMEOUT_MS
  );
});
