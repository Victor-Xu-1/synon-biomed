import type { FileSelectionItem } from '@/renderer/utils/file/fileSelection';

const BTW_COMMAND_RE = /^\/btw(?:\s+([\s\S]*))?$/i;

export const getSelectedItemMatchKeys = (item: FileSelectionItem): string[] => {
  if (typeof item === 'string') return [item];
  return [item.relativePath, item.path].filter((value): value is string => Boolean(value));
};

export const getSelectedItemPath = (item: FileSelectionItem): string | undefined =>
  typeof item === 'string' ? item : item.path;

export const getSelectedItemDisplayLabel = (item: FileSelectionItem): string => {
  if (typeof item === 'string') return item.split(/[\\/]/).pop() || item;
  return item.relativePath || item.name || item.path;
};

export const rememberSelectedItem = (itemsByPath: Map<string, FileSelectionItem>, item: FileSelectionItem): void => {
  const path = getSelectedItemPath(item);
  if (!path) return;
  const existing = itemsByPath.get(path);
  if (typeof existing === 'string' && typeof item !== 'string') {
    itemsByPath.set(path, item);
  } else if (!existing) {
    itemsByPath.set(path, item);
  }
};

export const areSelectionItemsEquivalent = (left: FileSelectionItem[], right: FileSelectionItem[]): boolean => {
  if (left.length !== right.length) return false;
  return left.every((leftItem, index) => {
    const rightItem = right[index];
    return (
      leftItem === rightItem ||
      (typeof leftItem === typeof rightItem && getSelectedItemPath(leftItem) === getSelectedItemPath(rightItem))
    );
  });
};

export const buildOwnedSelectionItems = (
  currentItems: FileSelectionItem[],
  mentionOwnedPaths: Set<string>,
  externalOwnedPaths: Set<string>,
  itemsByPath: Map<string, FileSelectionItem>
): FileSelectionItem[] => {
  const ownedPaths = new Set([...mentionOwnedPaths, ...externalOwnedPaths]);
  const nextItems: FileSelectionItem[] = [];
  const seenPaths = new Set<string>();
  for (const item of currentItems) {
    const path = getSelectedItemPath(item);
    if (!path || seenPaths.has(path) || !ownedPaths.has(path)) continue;
    nextItems.push(item);
    seenPaths.add(path);
  }
  for (const path of ownedPaths) {
    if (seenPaths.has(path)) continue;
    nextItems.push(itemsByPath.get(path) ?? path);
    seenPaths.add(path);
  }
  return nextItems;
};

export const extractBtwQuestion = (value: string): string | null => {
  const match = value.trim().match(BTW_COMMAND_RE);
  return match ? match[1] || '' : null;
};
