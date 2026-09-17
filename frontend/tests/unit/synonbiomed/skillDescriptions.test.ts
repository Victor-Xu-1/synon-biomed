import { describe, expect, it } from 'vitest';
import { resolveSkillDescription } from '@/renderer/services/skills/synonBiomedSkillDescriptions';

describe('Skill descriptions from the catalog', () => {
  it('uses the declared locale and keeps the source description as its fallback', () => {
    const locales = { 'zh-CN': '科研证据', en: 'Research evidence' };
    expect(resolveSkillDescription('custom', 'Source', 'zh-CN', locales)).toBe('科研证据');
    expect(resolveSkillDescription('custom', 'Source', 'en-US', locales)).toBe('Research evidence');
    expect(resolveSkillDescription('custom', 'Source', 'zh-CN')).toBe('Source');
  });

  it('does not assign a bundled description to an external Skill sharing its name', () => {
    expect(resolveSkillDescription('alphafold2', 'My custom workflow', 'zh-CN')).toBe('My custom workflow');
    expect(resolveSkillDescription('custom', '', 'zh-CN')).toBe('custom');
  });
});
