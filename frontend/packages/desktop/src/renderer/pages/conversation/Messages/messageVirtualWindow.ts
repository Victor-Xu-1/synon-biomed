/**
 * Logical index state for a cursor-paged, variable-height message window.
 *
 * The coordinate is intentionally independent from array indexes. When older
 * rows are prepended, an already rendered row keeps the same logical index so
 * a virtual scroller can preserve its exact viewport anchor.
 */
export type MessageVirtualWindow = Readonly<{
  firstItemIndex: number;
  rowKeys: readonly string[];
}>;

// This is coordinate headroom, not a transcript-size or task-lifetime limit.
export const MESSAGE_VIRTUAL_INDEX_ORIGIN = 1_000_000;
export const MESSAGE_DEFAULT_ITEM_HEIGHT = 72;

export function toAbsoluteMessageVirtualIndex(window: MessageVirtualWindow, relativeIndex: number): number {
  if (!Number.isInteger(relativeIndex) || relativeIndex < 0 || relativeIndex >= window.rowKeys.length) {
    throw new RangeError(`message virtual index out of range: ${relativeIndex}`);
  }
  return window.firstItemIndex + relativeIndex;
}

export function reconcileMessageVirtualWindow(
  previous: MessageVirtualWindow | null,
  rowKeys: readonly string[]
): MessageVirtualWindow {
  const nextKeys = [...rowKeys];
  if (nextKeys.length === 0) {
    return { firstItemIndex: MESSAGE_VIRTUAL_INDEX_ORIGIN, rowKeys: nextKeys };
  }
  if (!previous || previous.rowKeys.length === 0) {
    return { firstItemIndex: MESSAGE_VIRTUAL_INDEX_ORIGIN, rowKeys: nextKeys };
  }

  const nextIndexes = new Map(nextKeys.map((key, index) => [key, index]));
  for (let previousIndex = 0; previousIndex < previous.rowKeys.length; previousIndex += 1) {
    const nextIndex = nextIndexes.get(previous.rowKeys[previousIndex]);
    if (nextIndex === undefined) continue;
    const firstItemIndex = previous.firstItemIndex + previousIndex - nextIndex;
    if (firstItemIndex >= 0) return { firstItemIndex, rowKeys: nextKeys };
    break;
  }

  return { firstItemIndex: MESSAGE_VIRTUAL_INDEX_ORIGIN, rowKeys: nextKeys };
}
