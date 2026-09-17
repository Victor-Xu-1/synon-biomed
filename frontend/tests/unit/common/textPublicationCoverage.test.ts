import { describe, expect, it } from 'vitest';
import { isTextPublicationCovered, mergeTextPublicationCoverage } from '@/common/chat/textPublicationCoverage';

const coordinate = (sequence: number) => ({
  conversation_id: 'conversation',
  msg_id: 'segment',
  source_publication_sequence: sequence,
});

describe('text publication coverage', () => {
  it('keeps contiguous long-stream receipt metadata compressed', () => {
    let observed = { ...coordinate(1), ...mergeTextPublicationCoverage(coordinate(1), coordinate(1)) };
    for (let sequence = 2; sequence <= 1000; sequence++)
      observed = { ...observed, ...mergeTextPublicationCoverage(observed, coordinate(sequence)) };
    expect(observed.text_publication_ranges).toEqual([[1, 1000]]);
  });
  it('coalesces adjacent receipts without treating an unseen hole as a replay', () => {
    const sparse = { ...coordinate(3), ...mergeTextPublicationCoverage(coordinate(1), coordinate(3)) };
    expect(sparse.text_publication_ranges).toEqual([
      [1, 1],
      [3, 3],
    ]);
    expect(isTextPublicationCovered(sparse, coordinate(2))).toBe(false);
    const complete = { ...sparse, ...mergeTextPublicationCoverage(sparse, coordinate(2)) };
    expect(complete.text_publication_ranges).toEqual([[1, 3]]);
    expect(complete.source_publication_sequence).toBe(3);
    for (const sequence of [1, 2, 3]) expect(isTextPublicationCovered(complete, coordinate(sequence))).toBe(true);
  });

  it('retires receipt ranges covered by an authoritative history snapshot without mutation', () => {
    const live = Object.freeze({
      ...coordinate(8),
      text_publication_ranges: Object.freeze([Object.freeze([1, 3] as const), Object.freeze([7, 8] as const)]),
    });
    const history = { conversation_id: 'conversation', msg_id: 'segment', history_coverage_through: 7 };
    const merged = { ...live, ...mergeTextPublicationCoverage(live, history) };
    expect(merged.text_publication_ranges).toEqual([[8, 8]]);
    expect(live.text_publication_ranges).toEqual([
      [1, 3],
      [7, 8],
    ]);
    expect(isTextPublicationCovered(merged, coordinate(6))).toBe(true);
    expect(isTextPublicationCovered(merged, coordinate(9))).toBe(false);
  });

  it('does not borrow coverage across conversations or segments', () => {
    expect(isTextPublicationCovered(coordinate(1), { ...coordinate(1), conversation_id: 'other' })).toBe(false);
    expect(isTextPublicationCovered(coordinate(1), { ...coordinate(1), msg_id: 'other' })).toBe(false);
    expect(mergeTextPublicationCoverage(coordinate(1), { ...coordinate(1), msg_id: 'other' })).toEqual({});
  });

  it('ignores invalid sequence bounds instead of creating false coverage', () => {
    for (const sequence of [0, -1, NaN, Infinity, 1.5]) {
      expect(isTextPublicationCovered(coordinate(3), coordinate(sequence))).toBe(false);
      expect(
        mergeTextPublicationCoverage({ ...coordinate(sequence), history_coverage_through: sequence }, coordinate(2))
      ).toEqual({ source_publication_sequence: 2, text_publication_ranges: [[2, 2]] });
    }
  });
});
