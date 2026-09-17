/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React from 'react';
import { act, cleanup, render, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import ShadowView from '@/renderer/components/Markdown/ShadowView';

vi.mock('@/renderer/hooks/context/LayoutContext', () => ({
  useLayoutContext: () => ({ isMobile: false }),
}));

vi.mock('@/renderer/hooks/context/ThemeContext', () => ({
  useThemeContext: () => ({
    theme: 'light',
    activeTheme: {
      id: 'light',
      name: 'Light',
      appearance: 'light',
      builtin: true,
      created_at: 1,
      updated_at: 1,
    },
    fontScale: 1,
    fontSizes: { chat: 16, code: 13, ui: 14 },
  }),
}));

afterEach(() => {
  cleanup();
  document.documentElement.removeAttribute('style');
  vi.restoreAllMocks();
});

describe('ShadowView shared theme styles', () => {
  it('reads computed theme variables once per document style generation for all Markdown roots', async () => {
    document.documentElement.style.setProperty('--bg-1', 'rgb(1, 2, 3)');
    const getComputedStyleSpy = vi.spyOn(window, 'getComputedStyle');
    const { container } = render(
      <>
        <ShadowView>
          <span>first message</span>
        </ShadowView>
        <ShadowView>
          <span>second message</span>
        </ShadowView>
      </>
    );

    const hosts = [...container.querySelectorAll<HTMLElement>('.markdown-shadow')];
    await waitFor(() => {
      expect(hosts).toHaveLength(2);
      expect(hosts[0].shadowRoot?.textContent).toContain('first message');
      expect(hosts[1].shadowRoot?.textContent).toContain('second message');
    });
    expect(hosts[0].style.minHeight).toBe('1px');
    expect(hosts[1].style.minHeight).toBe('1px');
    expect(getComputedStyleSpy).toHaveBeenCalledTimes(1);

    await act(async () => {
      document.documentElement.style.setProperty('--bg-1', 'rgb(4, 5, 6)');
      await Promise.resolve();
    });

    await waitFor(() => expect(getComputedStyleSpy).toHaveBeenCalledTimes(2));
    expect(hosts[0].shadowRoot?.textContent).toContain('first message');
    expect(hosts[1].shadowRoot?.textContent).toContain('second message');
  });
});
