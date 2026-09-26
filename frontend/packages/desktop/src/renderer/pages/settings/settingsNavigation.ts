import { isSettingsRouteId, type SettingsRouteId } from './settingsRouteLoaders';

/**
 * Experts, skills, connectors and scientific environments all render the one
 * merged catalog page. The sider (desktop and mobile) exposes that page as a
 * single row — `scientificToolkit` — so every one of the four routes resolves
 * to the same entry and the row stays highlighted while the user switches
 * tabs inside the merged page.
 */
export const LIBRARY_ENTRY_ID: SettingsRouteId = 'experts';

const LIBRARY_ROUTE_IDS: ReadonlySet<SettingsRouteId> = new Set<SettingsRouteId>([
  'experts',
  'skills',
  'tools',
  'environments',
]);

/** Maps a settings route to the one navigation entry that represents it. */
export function resolveSiderEntryId(route: SettingsRouteId): SettingsRouteId {
  return LIBRARY_ROUTE_IDS.has(route) ? LIBRARY_ENTRY_ID : route;
}

export function readSettingsRoute(hash = window.location.hash): SettingsRouteId | null {
  const section = hash.match(/^#?\/settings\/([^/?#]+)/)?.[1];
  return isSettingsRouteId(section) ? section : null;
}

export function navigateSettingsRoute(id: SettingsRouteId): void {
  const hash = `#/settings/${id}`;
  if (window.location.hash !== hash) {
    window.history.pushState(window.history.state, '', `${window.location.pathname}${window.location.search}${hash}`);
    window.dispatchEvent(new PopStateEvent('popstate', { state: window.history.state }));
  }
}

export function subscribeToSettingsRoute(listener: (id: SettingsRouteId) => void): () => void {
  const readLocation = () => {
    const id = readSettingsRoute();
    if (id) listener(id);
  };
  window.addEventListener('hashchange', readLocation);
  window.addEventListener('popstate', readLocation);

  return () => {
    window.removeEventListener('hashchange', readLocation);
    window.removeEventListener('popstate', readLocation);
  };
}
