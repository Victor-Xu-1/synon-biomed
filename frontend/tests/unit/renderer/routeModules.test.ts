import { describe, expect, it, vi } from 'vitest';

const loads = vi.hoisted(() => ({
  authenticatedWorkspace: 0,
  guid: 0,
  project: 0,
  artifact: 0,
  settings: 0,
  settingsSider: 0,
  settingsPage: 0,
  fullLocale: 0,
  loadedSettingsPages: new Map<string, Promise<{ default: () => null }>>(),
}));

vi.mock('@renderer/services/i18n', () => ({
  ensureFullLocale: async () => {
    loads.fullLocale += 1;
    return 'zh-CN';
  },
}));

vi.mock('@renderer/components/layout/AuthenticatedWorkspace', () => {
  loads.authenticatedWorkspace += 1;
  return { default: () => null };
});

vi.mock('@renderer/pages/guid', () => {
  loads.guid += 1;
  return { default: () => null };
});
vi.mock('@renderer/pages/project/ProjectRoute', () => {
  loads.project += 1;
  return { default: () => null };
});
vi.mock('@renderer/pages/artifact/ArtifactPreview', () => {
  loads.artifact += 1;
  return { default: () => null };
});
vi.mock('@renderer/pages/settings/SettingsRoute', () => {
  loads.settings += 1;
  return { default: () => null };
});
vi.mock('@renderer/pages/settings/components/SettingsSider', () => {
  loads.settingsSider += 1;
  return { default: () => null };
});
vi.mock('@renderer/pages/settings/settingsRouteLoaders', () => ({
  preloadSettingsRoute: (id: string) => {
    let request = loads.loadedSettingsPages.get(id);
    if (!request) {
      loads.settingsPage += 1;
      request = Promise.resolve({ default: () => null });
      loads.loadedSettingsPages.set(id, request);
    }
    return request;
  },
}));

import {
  loadAuthenticatedWorkspaceRoute,
  loadArtifactPreviewRoute,
  loadGuidRoute,
  loadProjectRoute,
  loadSettingsRoute,
  prefetchProjectRoute,
  prefetchSettingsRoute,
} from '@/renderer/components/layout/routeModules';

describe('routeModules', () => {
  it('reuses one module promise per route instead of restarting dynamic imports', async () => {
    expect(loadAuthenticatedWorkspaceRoute()).toBe(loadAuthenticatedWorkspaceRoute());
    expect(loadGuidRoute()).toBe(loadGuidRoute());
    expect(loadProjectRoute()).toBe(loadProjectRoute());
    expect(loadArtifactPreviewRoute()).toBe(loadArtifactPreviewRoute());
    expect(loadSettingsRoute()).toBe(loadSettingsRoute());

    await Promise.all([
      loadAuthenticatedWorkspaceRoute(),
      loadGuidRoute(),
      loadProjectRoute(),
      loadArtifactPreviewRoute(),
      loadSettingsRoute(),
    ]);
    expect(loads).toMatchObject({
      authenticatedWorkspace: 1,
      guid: 1,
      project: 1,
      artifact: 1,
      settings: 1,
      fullLocale: 1,
    });
  });

  it('prefetches the visible settings shell and project route without duplicate imports', async () => {
    await Promise.all([
      prefetchSettingsRoute('tools'),
      prefetchSettingsRoute('tools'),
      prefetchProjectRoute(),
      prefetchProjectRoute(),
    ]);

    expect(loads.settings).toBe(1);
    expect(loads.settingsSider).toBe(1);
    expect(loads.settingsPage).toBe(1);
    expect(loads.project).toBe(1);
  });
});
