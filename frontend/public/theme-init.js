// Restore the persisted appearance and visual family before the application
// renders, without relaxing the page CSP.
(function () {
  try {
    var theme = localStorage.getItem('__synon-ai_theme');
    var family = localStorage.getItem('__synon-ai_theme_family');
    var hasTheme = theme === 'light' || theme === 'dark';
    var normalizedFamily = family === 'claude' ? 'warm' : family === 'codex' ? 'cool' : family;
    var hasFamily =
      normalizedFamily === 'warm' ||
      normalizedFamily === 'cool' ||
      normalizedFamily === 'white' ||
      normalizedFamily === 'night';
    if (!hasTheme && !hasFamily) return;

    var root = document.documentElement;
    if (hasTheme) root.setAttribute('data-theme', theme);
    if (hasFamily) root.setAttribute('data-theme-family', normalizedFamily);
    if (!hasTheme) return;

    var observer;
    var applyBodyTheme = function () {
      if (!document.body) return false;
      document.body.setAttribute('arco-theme', theme);
      if (observer) observer.disconnect();
      return true;
    };
    if (applyBodyTheme()) return;

    observer = new MutationObserver(applyBodyTheme);
    observer.observe(root, { childList: true });
  } catch (error) {
    // Storage can be unavailable in hardened browser contexts; the document
    // defaults remain usable.
  }
})();
