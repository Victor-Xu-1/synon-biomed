import { describe, expect, it } from 'vitest';
import {
  MESSAGE_VIRTUAL_INDEX_ORIGIN,
  reconcileMessageVirtualWindow,
  toAbsoluteMessageVirtualIndex,
} from '@/renderer/pages/conversation/Messages/messageVirtualWindow';

describe('messageVirtualWindow', () => {
  it('keeps existing logical indexes stable across append, prepend, and trimming', () => {
    const initial = reconcileMessageVirtualWindow(null, ['b', 'c']);
    expect(initial.firstItemIndex).toBe(MESSAGE_VIRTUAL_INDEX_ORIGIN);

    const appended = reconcileMessageVirtualWindow(initial, ['b', 'c', 'd']);
    expect(appended.firstItemIndex).toBe(initial.firstItemIndex);

    const prepended = reconcileMessageVirtualWindow(appended, ['a-1', 'a-2', 'b', 'c', 'd']);
    expect(prepended.firstItemIndex).toBe(initial.firstItemIndex - 2);

    const trimmed = reconcileMessageVirtualWindow(prepended, ['a-2', 'b', 'c', 'd']);
    expect(trimmed.firstItemIndex).toBe(prepended.firstItemIndex + 1);
  });

  it('starts a new coordinate space only when a replacement has no stable row overlap', () => {
    const initial = reconcileMessageVirtualWindow(null, ['old-a', 'old-b']);
    const replaced = reconcileMessageVirtualWindow(initial, ['new-a', 'new-b']);

    expect(replaced).toEqual({
      firstItemIndex: MESSAGE_VIRTUAL_INDEX_ORIGIN,
      rowKeys: ['new-a', 'new-b'],
    });
  });

  it('uses a later surviving row when the previous first row is removed', () => {
    const initial = reconcileMessageVirtualWindow(null, ['removed', 'survives', 'tail']);
    const next = reconcileMessageVirtualWindow(initial, ['survives', 'tail']);

    expect(next.firstItemIndex).toBe(initial.firstItemIndex + 1);
  });

  it('translates relative rows into the stable virtual coordinate space', () => {
    const window = { firstItemIndex: MESSAGE_VIRTUAL_INDEX_ORIGIN - 2, rowKeys: ['old-1', 'old-2', 'current'] };
    expect(toAbsoluteMessageVirtualIndex(window, 2)).toBe(MESSAGE_VIRTUAL_INDEX_ORIGIN);
  });

  it('rejects indexes that would scroll into an empty virtual range', () => {
    const window = { firstItemIndex: MESSAGE_VIRTUAL_INDEX_ORIGIN, rowKeys: ['only'] };
    expect(() => toAbsoluteMessageVirtualIndex(window, -1)).toThrow(RangeError);
    expect(() => toAbsoluteMessageVirtualIndex(window, 1)).toThrow(RangeError);
  });
});
