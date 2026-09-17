import fs from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

type GuidLocale = {
  promptExamplesHint: string;
  defaultPrompts: Record<'capabilities' | 'skills' | 'tools', string>;
};

const readGuidLocale = (locale: string): GuidLocale => {
  const filePath = path.join(process.cwd(), 'packages/desktop/src/renderer/services/i18n/locales', locale, 'guid.json');
  return JSON.parse(fs.readFileSync(filePath, 'utf8')) as GuidLocale;
};

describe('Guid Synon Biomed locale', () => {
  it('uses biomedical workflow prompt examples instead of generic SynonAI capability questions', () => {
    const zh = readGuidLocale('zh-CN');
    const en = readGuidLocale('en-US');

    expect(Object.values(zh.defaultPrompts)).toEqual([
      '\u8bbe\u8ba1\u4e00\u4e2a STAT6 SBDD PPI \u5206\u6790\u6d41\u7a0b',
      '\u8c03\u7528 Boltz2 \u751f\u6210\u5e76\u8bc4\u4f30\u86cb\u767d-\u914d\u4f53\u590d\u5408\u7269\u7ed3\u6784',
      '\u6574\u7406 CRBN \u9879\u76ee\u7684\u7ed3\u679c\u6587\u4ef6\u5e76\u751f\u6210\u62a5\u544a',
    ]);
    expect(Object.values(en.defaultPrompts)).toEqual([
      'Design a STAT6 SBDD PPI analysis workflow',
      'Run Boltz2 to generate and evaluate a protein-ligand complex',
      'Organize CRBN project result files and draft a report',
    ]);
    expect(Object.values(zh.defaultPrompts)).not.toContain(
      '\u4f60\u80fd\u4e3a\u6211\u505a\u54ea\u4e9b\u4e8b\u60c5\uff1f'
    );
    expect(Object.values(en.defaultPrompts)).not.toContain('What can you do for me?');
  });
});
