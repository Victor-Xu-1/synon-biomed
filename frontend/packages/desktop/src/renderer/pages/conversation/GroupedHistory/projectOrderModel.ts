export const normalizeProjectOrder = (value: unknown): string[] => {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  const normalized: string[] = [];
  for (const item of value) {
    if (typeof item !== 'string') continue;
    const id = item.trim();
    if (!id || seen.has(id)) continue;
    seen.add(id);
    normalized.push(id);
  }
  return normalized;
};

export const reconcileProjectOrder = (storedOrder: unknown, availableProjectIds: readonly string[]): string[] => {
  const available = normalizeProjectOrder(availableProjectIds);
  const availableSet = new Set(available);
  const listed = normalizeProjectOrder(storedOrder).filter((id) => availableSet.has(id));
  const listedSet = new Set(listed);
  return [...listed, ...available.filter((id) => !listedSet.has(id))];
};

export const reorderProjectIds = (order: readonly string[], activeId: string, overId: string): string[] => {
  const normalized = normalizeProjectOrder(order);
  const from = normalized.indexOf(activeId);
  const to = normalized.indexOf(overId);
  if (from < 0 || to < 0 || from === to) return normalized;
  const next = [...normalized];
  const [moved] = next.splice(from, 1);
  next.splice(to, 0, moved);
  return next;
};

export const moveProjectByOffset = (order: readonly string[], projectId: string, offset: -1 | 1): string[] => {
  const normalized = normalizeProjectOrder(order);
  const from = normalized.indexOf(projectId);
  if (from < 0) return normalized;
  const to = Math.max(0, Math.min(normalized.length - 1, from + offset));
  return reorderProjectIds(normalized, projectId, normalized[to]);
};

export const sameProjectOrder = (left: readonly string[], right: readonly string[]): boolean =>
  left.length === right.length && left.every((id, index) => id === right[index]);
