import { randomBytes } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { loadSynonBiomedSkills } from '@/renderer/services/synonBiomedCapabilities';
import {
  deleteSynonBiomedPersonalSkill,
  importSynonBiomedSkillFile,
  loadSynonBiomedSkillDrafts,
  loadSynonBiomedSkillFileContent,
  loadSynonBiomedSkillFiles,
  loadSynonBiomedSkillSources,
  saveSynonBiomedSkillDraftFile,
} from '@/renderer/services/skills/synonBiomedSkillLibrary';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';

type CatalogSkill = { name: string; source?: string };

describe('Synon Biomed skill library gateway', () => {
  it('reads library metadata and atomically edits/restores a real personal Skill', async () => {
    const fetchImpl = await createSynonBiomedTestFetch(gatewayBaseUrl);
    const options = { baseUrl: gatewayBaseUrl, fetchImpl };
    const name = `real-skill-${Date.now().toString(36)}-${randomBytes(3).toString('hex')}`;
    const original = `---
name: ${name}
description: Temporary real integration Skill used to verify the writable library lifecycle.
---

# ${name}

Return the exact text requested by the caller.
`;
    let imported = false;
    try {
      await importSynonBiomedSkillFile(new File([original], 'SKILL.md', { type: 'text/markdown' }), name, options);
      imported = true;

      const skills = (await loadSynonBiomedSkills(options)) as CatalogSkill[];
      expect(skills).toContainEqual(expect.objectContaining({ name, source: 'personal' }));
      await expect(loadSynonBiomedSkillDrafts(options)).resolves.toEqual(expect.any(Array));
      await expect(loadSynonBiomedSkillSources(options)).resolves.toEqual(expect.any(Array));

      const files = await loadSynonBiomedSkillFiles(name, options);
      expect(files).toContain('SKILL.md');
      await expect(loadSynonBiomedSkillFileContent(name, 'SKILL.md', options)).resolves.toBe(original);

      const marker = `\n<!-- synon-ai-real-edit-${Date.now()} -->\n`;
      await saveSynonBiomedSkillDraftFile(name, 'SKILL.md', original, original + marker, options);
      await expect(loadSynonBiomedSkillFileContent(name, 'SKILL.md', options)).resolves.toContain(marker.trim());
      await saveSynonBiomedSkillDraftFile(name, 'SKILL.md', original + marker, original, options);
      await expect(loadSynonBiomedSkillFileContent(name, 'SKILL.md', options)).resolves.toBe(original);
    } finally {
      if (imported) await deleteSynonBiomedPersonalSkill(name, options);
    }
  });
});
