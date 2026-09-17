import { isSettingsRouteId, type SettingsRouteId } from './settingsRouteLoaders';

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
