import { describe, expect, it } from 'vitest';
import { resolveSynonBiomedMcpDescription } from '@/renderer/services/mcp/synonBiomedMcpDescriptions';

describe('Synon Biomed MCP descriptions', () => {
  it.each(['synon-research', 'open-targets-official', 'tamarind-bio', 'adaptyv-cloud-lab', 'idc-rest'])(
    'localizes the installed %s connector',
    (name) => {
      expect(resolveSynonBiomedMcpDescription(name, 'English fallback.', 'zh-CN')).toMatch(/[\u4e00-\u9fff]/);
    }
  );
  it('uses the biomedical Chinese copy for the Chinese interface', () => {
    expect(resolveSynonBiomedMcpDescription('pubmed', 'PubMed biomedical literature.', 'zh-CN')).toContain(
      'PubMed 生物医学文献'
    );
  });

  it('keeps the backend English description in the English interface', () => {
    expect(resolveSynonBiomedMcpDescription('pubmed', 'Biomedical literature search and metadata.', 'en-US')).toBe(
      'Biomedical literature search and metadata.'
    );
  });

  it('prefers an API-provided localized description', () => {
    expect(resolveSynonBiomedMcpDescription('pubmed', 'Fallback', 'zh-CN', { 'zh-CN': '本地化 PubMed 说明。' })).toBe(
      '本地化 PubMed 说明。'
    );
  });
});
