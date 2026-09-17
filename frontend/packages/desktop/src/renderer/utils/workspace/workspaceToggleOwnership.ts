export type WorkspaceToggleOwner = 'titlebar' | 'conversation-header' | 'workspace-panel';

export type WorkspaceToggleEnvironment = {
  isElectronDesktop: boolean;
  isMac: boolean;
  isWindows: boolean;
};

/** Keep exactly one persistent workspace toggle for each supported runtime shell. */
export function resolveWorkspaceToggleOwner(environment: WorkspaceToggleEnvironment): WorkspaceToggleOwner {
  if (!environment.isElectronDesktop || environment.isMac) return 'titlebar';
  if (environment.isWindows) return 'conversation-header';
  return 'workspace-panel';
}
