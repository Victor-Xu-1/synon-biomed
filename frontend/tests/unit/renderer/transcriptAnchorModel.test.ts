import { describe, expect, it } from 'vitest';
import { resolveTranscriptAnchor } from '@/renderer/pages/conversation/Messages/components/transcriptAnchorModel';

const annotation = (anchorText: string, startOffset: number | null, endOffset: number | null) => ({
  anchorText,
  startOffset,
  endOffset,
});

describe('transcript anchor resolution', () => {
  it('uses stored offsets to distinguish repeated identical text', () => {
    expect(resolveTranscriptAnchor('STAT6 then STAT6', annotation('STAT6', 11, 16))).toEqual({
      status: 'resolved',
      startOffset: 11,
      endOffset: 16,
      relocated: false,
    });
  });

  it('relocates only when the persisted text has one exact occurrence', () => {
    expect(resolveTranscriptAnchor('prefix β-catenin suffix', annotation('β-catenin', 0, 9))).toEqual({
      status: 'resolved',
      startOffset: 7,
      endOffset: 16,
      relocated: true,
    });
  });

  it('does not guess after stale offsets when identical text is repeated', () => {
    expect(resolveTranscriptAnchor('STAT6 x STAT6', annotation('STAT6', 3, 8))).toEqual({
      status: 'orphaned',
      reason: 'ambiguous',
    });
  });

  it('marks removed text orphaned and keeps legacy whole-message records valid', () => {
    expect(resolveTranscriptAnchor('new text', annotation('old text', 0, 8))).toEqual({
      status: 'orphaned',
      reason: 'missing',
    });
    expect(resolveTranscriptAnchor('message', annotation('message', null, null))).toEqual({
      status: 'legacy',
      startOffset: null,
      endOffset: null,
    });
  });

  it('preserves UTF-16 offsets used by DOM Range for emoji and CJK text', () => {
    const text = '结果🧬可靠';
    expect(resolveTranscriptAnchor(text, annotation('🧬可靠', 2, 6))).toEqual({
      status: 'resolved',
      startOffset: 2,
      endOffset: 6,
      relocated: false,
    });
  });
});
