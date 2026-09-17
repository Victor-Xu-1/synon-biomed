const BLOCKING_LAYER_SELECTOR = [
  '[role="menu"]',
  '[role="listbox"]',
  '[role="dialog"]',
  '[role="alertdialog"]',
  '[aria-modal="true"]',
  '.bg-backdrop',
].join(',');

export function shouldDismissBtwOverlay(
  event: Pick<KeyboardEvent, 'key' | 'defaultPrevented' | 'isComposing' | 'target'>,
  panel: HTMLElement | null,
  doc: Document = document
): boolean {
  if (event.key !== 'Escape' || event.defaultPrevented || event.isComposing) return false;
  const target = event.target instanceof HTMLElement ? event.target : null;
  if (target && !target.isConnected) return false;
  const blockingLayer = doc.querySelector(BLOCKING_LAYER_SELECTOR);
  if (blockingLayer && blockingLayer !== panel && !panel?.contains(blockingLayer)) return false;
  return !target || target === doc.body || target === doc.documentElement || Boolean(panel?.contains(target));
}
