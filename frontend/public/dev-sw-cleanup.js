(() => {
  const hasViteClient = Array.from(document.scripts).some((script) => {
    if (!script.src) return false;
    return new URL(script.src, window.location.href).pathname === '/@vite/client';
  });
  if (!hasViteClient || !('serviceWorker' in navigator)) return;

  const reloadKey = 'synon-biomed:dev-service-worker-cleanup';
  const wasControlled = navigator.serviceWorker.controller !== null;

  void Promise.all([
    navigator.serviceWorker
      .getRegistrations()
      .then((registrations) =>
        Promise.all(
          registrations
            .filter((registration) => registration.scope.startsWith(window.location.origin))
            .map((registration) => registration.unregister())
        )
      ),
    'caches' in window
      ? caches
          .keys()
          .then((keys) =>
            Promise.all(keys.filter((key) => key.startsWith('synon-ai-webui-')).map((key) => caches.delete(key)))
          )
      : Promise.resolve([]),
  ])
    .then(() => {
      if (!wasControlled) {
        sessionStorage.removeItem(reloadKey);
        return;
      }
      if (sessionStorage.getItem(reloadKey) === 'reloaded') return;
      sessionStorage.setItem(reloadKey, 'reloaded');
      window.location.reload();
    })
    .catch(() => undefined);
})();
