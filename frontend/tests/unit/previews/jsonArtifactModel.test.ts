import {
  MAX_STRUCTURED_JSON_CHARS,
  collectJsonMatchPaths,
  collectJsonContainerPaths,
  jsonValueMatches,
  parseJsonDocument,
} from '@/renderer/pages/conversation/Preview/components/viewers/jsonDocumentModel';
import { describe, expect, it } from 'vitest';

describe('jsonArtifactModel', () => {
  it('parses JSON and counts nested nodes', () => {
    expect(parseJsonDocument('{"name":"screen","phases":[{"title":"Plan"}]}')).toEqual({
      status: 'ready',
      value: { name: 'screen', phases: [{ title: 'Plan' }] },
      nodeCount: 5,
    });
  });

  it('rejects invalid and oversized structured previews without hiding the source', () => {
    expect(parseJsonDocument('{broken')).toMatchObject({ status: 'invalid' });
    expect(parseJsonDocument(' '.repeat(MAX_STRUCTURED_JSON_CHARS + 1))).toMatchObject({ status: 'too-large' });
  });

  it('matches nested keys and values and collects expandable paths', () => {
    const value = { phases: [{ title: 'Download matrix' }], enabled: true };
    expect(jsonValueMatches('root', value, 'download')).toBe(true);
    expect(jsonValueMatches('root', value, 'missing')).toBe(false);
    expect([...collectJsonMatchPaths(value, 'download')!]).toEqual([
      '$.phases[0].title',
      '$.phases[0]',
      '$.phases',
      '$',
    ]);
    expect([...collectJsonContainerPaths(value)]).toEqual(['$', '$.phases', '$.phases[0]']);
  });
});
