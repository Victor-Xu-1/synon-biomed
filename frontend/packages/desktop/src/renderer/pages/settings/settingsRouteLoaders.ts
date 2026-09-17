import type { ComponentType } from 'react';

type SettingsModule = { default: ComponentType };

function cachedLoader(load: () => Promise<SettingsModule>): () => Promise<SettingsModule> {
  let request: Promise<SettingsModule> | undefined;
  return () => {
    if (!request) {
      request = load().catch((error) => {
        request = undefined;
        throw error;
      });
    }
    return request;
  };
}

export const settingsRouteLoaders = {
  account: cachedLoader(() => import('./AccountSettings')),
  'plans-usage': cachedLoader(() => import('./PlansUsageSettings')),
  experts: cachedLoader(() => import('./SynonBiomedExpertsSettings')),
  skills: cachedLoader(() => import('./SynonBiomedSkillsSettings')),
  tools: cachedLoader(() => import('./ToolsSettings')),
  models: cachedLoader(() => import('./SynonBiomedModelsSettings')),
  compute: cachedLoader(() => import('./ComputeSettings')),
  governance: cachedLoader(() => import('./GovernanceSettings')),
  network: cachedLoader(() => import('./NetworkSettings')),
  credentials: cachedLoader(() => import('./CredentialsSettings')),
  storage: cachedLoader(() => import('./StorageSettings')),
  general: cachedLoader(() => import('./GeneralSettings')),
} as const;

export type SettingsRouteId = keyof typeof settingsRouteLoaders;

const settingsNavigationOrder: SettingsRouteId[] = [
  'experts',
  'skills',
  'tools',
  'models',
  'compute',
  'governance',
  'network',
  'credentials',
  'storage',
  'general',
];

export function isSettingsRouteId(value: string | undefined | null): value is SettingsRouteId {
  return typeof value === 'string' && Object.hasOwn(settingsRouteLoaders, value);
}

export function preloadSettingsRoute(id: SettingsRouteId): Promise<SettingsModule> {
  return settingsRouteLoaders[id]();
}

export function getSettingsWarmupOrder(activeRoute: SettingsRouteId): SettingsRouteId[] {
  const activeIndex = settingsNavigationOrder.indexOf(activeRoute);
  const adjacent: SettingsRouteId[] = [];
  if (activeIndex >= 0) {
    const previous = settingsNavigationOrder[activeIndex - 1];
    const next = settingsNavigationOrder[activeIndex + 1];
    if (previous) adjacent.push(previous);
    if (next) adjacent.push(next);
  }
  return adjacent;
}

type IdleWindow = Window & {
  requestIdleCallback?: (callback: () => void, options?: { timeout: number }) => number;
  cancelIdleCallback?: (handle: number) => void;
};

const warmedSettingsRoutes = new Set<SettingsRouteId>();

export function preloadSettingsRoutesDuringIdle(activeRoute: SettingsRouteId): () => void {
  if (typeof window === 'undefined' || warmedSettingsRoutes.has(activeRoute)) return () => {};

  const pending = getSettingsWarmupOrder(activeRoute);
  if (pending.length === 0) return () => {};
  warmedSettingsRoutes.add(activeRoute);
  const idleWindow = window as IdleWindow;
  let cancelled = false;
  const run = () => {
    if (cancelled) return;
    void Promise.allSettled(pending.map((id) => preloadSettingsRoute(id))).then((results) => {
      results.forEach((result, index) => {
        if (result.status === 'rejected') {
          console.error(`[settings-prefetch] module unavailable: ${pending[index]}`);
        }
      });
    });
  };
  const idleHandle = idleWindow.requestIdleCallback?.(run, { timeout: 500 });
  const timerHandle = idleHandle === undefined ? window.setTimeout(run, 50) : undefined;
  return () => {
    cancelled = true;
    if (idleHandle !== undefined) idleWindow.cancelIdleCallback?.(idleHandle);
    if (timerHandle !== undefined) window.clearTimeout(timerHandle);
    warmedSettingsRoutes.delete(activeRoute);
  };
}
