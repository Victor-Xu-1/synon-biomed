import { act, renderHook, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

const themeMocks = vi.hoisted(() => ({
  applyTheme: vi.fn(),
  setActiveTheme: vi.fn().mockResolvedValue(undefined),
  setActiveRelay: vi.fn().mockResolvedValue(undefined),
  subscribe: vi.fn(() => vi.fn()),
}));

vi.mock('@/common/config/configService', () => ({
  configService: {
    whenReady: vi.fn().mockResolvedValue(undefined),
    get: vi.fn((key: string) => {
      if (key === 'theme.activeId') return 'light';
      if (key === 'theme.userThemes') return [];
      return undefined;
    }),
  },
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    theme: {
      setActive: { invoke: themeMocks.setActiveRelay },
      changed: { on: themeMocks.subscribe },
    },
  },
}));

vi.mock('@/renderer/utils/theme/applyTheme', () => ({
  applyTheme: themeMocks.applyTheme,
  setActiveTheme: themeMocks.setActiveTheme,
}));

vi.mock('@/renderer/utils/theme/systemAppearance', () => ({
  getSystemPrefersDark: vi.fn(() => false),
}));

vi.mock('@/renderer/utils/theme/systemThemeWatcher', () => ({
  startSystemThemeWatcher: vi.fn(() => vi.fn()),
}));

import useTheme from '@/renderer/hooks/system/useTheme';

describe('useTheme', () => {
  it('updates local appearance on every selection without waiting for a broadcast or reload', async () => {
    const { result } = renderHook(() => useTheme());

    await waitFor(() => expect(result.current[0]?.appearance).toBe('light'));

    await act(async () => {
      await result.current[1]('dark');
    });
    expect(result.current[0]?.appearance).toBe('dark');
    expect(result.current[2]).toBe('dark');

    await act(async () => {
      await result.current[1]('light');
    });
    expect(result.current[0]?.appearance).toBe('light');
    expect(result.current[2]).toBe('light');
    expect(themeMocks.setActiveTheme).toHaveBeenNthCalledWith(1, 'dark');
    expect(themeMocks.setActiveTheme).toHaveBeenNthCalledWith(2, 'light');
  });

  it('normalizes removed template ids back to the Claude builtin', async () => {
    const { result } = renderHook(() => useTheme());

    await waitFor(() => expect(result.current[0]?.id).toBe('light'));

    await act(async () => {
      await result.current[1]('legacy-theme-id');
    });

    expect(result.current[0]?.id).toBe('light');
    expect(result.current[2]).toBe('light');
    expect(themeMocks.setActiveTheme).toHaveBeenLastCalledWith('light');
  });
});
