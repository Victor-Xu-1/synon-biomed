import { describe, expect, it } from 'vitest';

import { resolveWorkspaceToggleOwner } from '@/renderer/utils/workspace/workspaceToggleOwnership';

describe('workspace toggle ownership', () => {
  it.each([
    {
      runtime: 'Windows web',
      environment: { isElectronDesktop: false, isMac: false, isWindows: true },
      expected: 'titlebar',
    },
    {
      runtime: 'macOS web',
      environment: { isElectronDesktop: false, isMac: true, isWindows: false },
      expected: 'titlebar',
    },
    {
      runtime: 'Linux web',
      environment: { isElectronDesktop: false, isMac: false, isWindows: false },
      expected: 'titlebar',
    },
    {
      runtime: 'macOS Electron',
      environment: { isElectronDesktop: true, isMac: true, isWindows: false },
      expected: 'titlebar',
    },
    {
      runtime: 'Windows Electron',
      environment: { isElectronDesktop: true, isMac: false, isWindows: true },
      expected: 'conversation-header',
    },
    {
      runtime: 'Linux Electron',
      environment: { isElectronDesktop: true, isMac: false, isWindows: false },
      expected: 'workspace-panel',
    },
  ] as const)('assigns one owner for $runtime', ({ environment, expected }) => {
    expect(resolveWorkspaceToggleOwner(environment)).toBe(expected);
  });
});
