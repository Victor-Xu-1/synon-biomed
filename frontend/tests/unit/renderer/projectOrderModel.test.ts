import { describe, expect, it } from 'vitest';
import {
  moveProjectByOffset,
  normalizeProjectOrder,
  reconcileProjectOrder,
  reorderProjectIds,
  sameProjectOrder,
} from '@/renderer/pages/conversation/GroupedHistory/projectOrderModel';

describe('projectOrderModel', () => {
  it('normalizes malformed, duplicate and empty stored ids', () => {
    expect(normalizeProjectOrder([' p2 ', '', 'p1', 'p2', null, 3, 'p3'])).toEqual(['p2', 'p1', 'p3']);
    expect(normalizeProjectOrder({ projectId: 'p1' })).toEqual([]);
  });

  it('prunes deleted ids and deterministically appends new projects in API order', () => {
    expect(reconcileProjectOrder(['deleted', 'p2', 'p2'], ['p1', 'p2', 'p3'])).toEqual(['p2', 'p1', 'p3']);
  });

  it('reorders by target and clamps keyboard movement at list boundaries', () => {
    expect(reorderProjectIds(['p1', 'p2', 'p3'], 'p1', 'p3')).toEqual(['p2', 'p3', 'p1']);
    expect(moveProjectByOffset(['p1', 'p2', 'p3'], 'p2', -1)).toEqual(['p2', 'p1', 'p3']);
    expect(moveProjectByOffset(['p1', 'p2', 'p3'], 'p1', -1)).toEqual(['p1', 'p2', 'p3']);
    expect(sameProjectOrder(['p1', 'p2'], ['p1', 'p2'])).toBe(true);
  });
});
