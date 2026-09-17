/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ensureFullLocale } from '@renderer/services/i18n';
import {
  preloadSettingsRoute as preloadSettingsPageRoute,
  type SettingsRouteId,
} from '@renderer/pages/settings/settingsRouteLoaders';

type GuidRouteModule = typeof import('@renderer/pages/guid');
type AuthenticatedWorkspaceRouteModule = typeof import('@renderer/components/layout/AuthenticatedWorkspace');
type ProjectRouteModule = typeof import('@renderer/pages/project/ProjectRoute');
type ArtifactPreviewModule = typeof import('@renderer/pages/artifact/ArtifactPreview');
type SettingsRouteModule = typeof import('@renderer/pages/settings/SettingsRoute');
type SettingsSiderModule = typeof import('@renderer/pages/settings/components/SettingsSider');

let guidRoutePromise: Promise<GuidRouteModule> | null = null;
let authenticatedWorkspaceRoutePromise: Promise<AuthenticatedWorkspaceRouteModule> | null = null;
let projectRoutePromise: Promise<ProjectRouteModule> | null = null;
let artifactPreviewPromise: Promise<ArtifactPreviewModule> | null = null;
let settingsRoutePromise: Promise<SettingsRouteModule> | null = null;
let settingsSiderPromise: Promise<SettingsSiderModule> | null = null;

export function loadGuidRoute(): Promise<GuidRouteModule> {
  guidRoutePromise ??= import('@renderer/pages/guid');
  return guidRoutePromise;
}

export function loadAuthenticatedWorkspaceRoute(): Promise<AuthenticatedWorkspaceRouteModule> {
  authenticatedWorkspaceRoutePromise ??= Promise.all([
    ensureFullLocale(),
    import('@renderer/components/layout/AuthenticatedWorkspace'),
  ]).then(([, module]) => module);
  return authenticatedWorkspaceRoutePromise;
}

export function loadProjectRoute(): Promise<ProjectRouteModule> {
  projectRoutePromise ??= import('@renderer/pages/project/ProjectRoute');
  return projectRoutePromise;
}

export function loadArtifactPreviewRoute(): Promise<ArtifactPreviewModule> {
  artifactPreviewPromise ??= import('@renderer/pages/artifact/ArtifactPreview');
  return artifactPreviewPromise;
}

export function loadSettingsRoute(): Promise<SettingsRouteModule> {
  settingsRoutePromise ??= import('@renderer/pages/settings/SettingsRoute');
  return settingsRoutePromise;
}

export function loadSettingsSider(): Promise<SettingsSiderModule> {
  settingsSiderPromise ??= import('@renderer/pages/settings/components/SettingsSider');
  return settingsSiderPromise;
}

export async function prefetchProjectRoute(): Promise<void> {
  await loadProjectRoute();
}

export async function prefetchSettingsRoute(section: SettingsRouteId = 'general'): Promise<void> {
  await Promise.all([loadSettingsRoute(), loadSettingsSider(), preloadSettingsPageRoute(section)]);
}
