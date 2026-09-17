/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  getSynonBiomedSkillCategoryOptions,
  resolveSynonBiomedSkillCategory,
} from '@/renderer/services/skills/synonBiomedSkillCategories';

const presentation = JSON.parse(
  readFileSync(path.resolve(process.cwd(), '../skills/synonbiomed/catalog-ui.json'), 'utf8')
) as { skills: Record<string, { category: string; description_i18n: Record<string, string> }> };

function categoryForName(name: string) {
  return resolveSynonBiomedSkillCategory({ name, ...presentation.skills[name] });
}

describe('Synon Biomed broad skill areas', () => {
  it('maps the complete drug-development lifecycle into broad research areas', () => {
    expect(categoryForName('medicinal-chemistry-optimization')).toBe('drug-discovery');
    expect(categoryForName('single-cell-rna-analysis')).toBe('omics-bioinformatics');
    expect(categoryForName('dmpk-adme-strategy')).toBe('dmpk-nonclinical');
    expect(categoryForName('formulation-development')).toBe('cmc-manufacturing');
    expect(categoryForName('clinical-trial-protocol')).toBe('clinical-regulatory');
    expect(categoryForName('instrument-data-to-allotrope')).toBe('research-workflows');
    expect(categoryForName('presentations')).toBe('research-workflows');
    expect(categoryForName('synon-research')).toBe('research-workflows');
    expect(categoryForName('capability-acquisition')).toBe('compute-platform');
    expect(categoryForName('sbdd-ppi-workflow')).toBe('drug-discovery');
    expect(categoryForName('customize')).toBe('compute-platform');
    expect(categoryForName('pocket2mol-local')).toBe('drug-discovery');
    expect(categoryForName('structure-based-molecule-generation')).toBe('drug-discovery');
    expect(categoryForName('antibody-design-strategy')).toBe('structural-biology');
    expect(categoryForName('protein-design-strategy')).toBe('structural-biology');
    expect(categoryForName('rna-design-strategy')).toBe('structural-biology');
    expect(categoryForName('unknown-skill')).toBeNull();
  });

  it('uses declared metadata without borrowing labels from a matching name', () => {
    expect(resolveSynonBiomedSkillCategory({ name: 'autodock-vina' })).toBeNull();
    expect(resolveSynonBiomedSkillCategory({ name: 'custom', category: 'drug-discovery' })).toBe('drug-discovery');
    expect(resolveSynonBiomedSkillCategory({ name: 'custom', category: 'unknown-field' })).toBeNull();
    expect(getSynonBiomedSkillCategoryOptions([{ name: 'custom' }], 'en-US')).toEqual([
      { id: 'all', label: 'All fields', count: 1 },
      { id: 'uncategorized', label: 'Uncategorized', count: 1 },
    ]);
  });

  it('counts skills under broad areas without losing uncategorized skills', () => {
    const options = getSynonBiomedSkillCategoryOptions(
      [
        { name: 'autodock-vina' },
        { name: 'medicinal-chemistry-optimization' },
        { name: 'alphafold2' },
        { name: 'scgpt' },
        { name: 'nextflow-development' },
        { name: 'dmpk-adme-strategy' },
        { name: 'nonclinical-safety-strategy' },
        { name: 'formulation-development' },
        { name: 'cmc-control-strategy' },
        { name: 'clinical-trial-protocol' },
        { name: 'regulatory-submission-strategy' },
        { name: 'literature-review' },
        { name: 'instrument-data-to-allotrope' },
        { name: 'compute-env-setup' },
        { name: 'customize' },
        { name: 'unknown-skill' },
      ].map(({ name }) => ({ name, category: presentation.skills[name]?.category })),
      'zh-CN'
    );

    expect(options).toEqual([
      { id: 'all', label: '全部领域', count: 16 },
      { id: 'drug-discovery', label: '药物发现与计算化学', count: 2 },
      { id: 'structural-biology', label: '结构生物学与蛋白质工程', count: 1 },
      { id: 'omics-bioinformatics', label: '组学与生物信息学', count: 2 },
      { id: 'dmpk-nonclinical', label: 'DMPK、非临床与安全性', count: 2 },
      { id: 'cmc-manufacturing', label: '药剂、CMC与生产工艺', count: 2 },
      { id: 'clinical-regulatory', label: '临床开发、注册与上市后', count: 2 },
      { id: 'research-workflows', label: '科研知识与实验数据工作流', count: 2 },
      { id: 'compute-platform', label: '计算基础设施与平台能力', count: 2 },
      { id: 'uncategorized', label: '未分类', count: 1 },
    ]);
  });

  it('categorizes every shipped skill by its declared skill name', () => {
    const repositoryRoot = path.resolve(process.cwd(), '..');
    const manifest = JSON.parse(
      readFileSync(path.join(repositoryRoot, 'assets/synonbiomed/skills.manifest.json'), 'utf8')
    ) as { skills: string[] };
    const skillNames = manifest.skills.map((folder) => {
      const content = readFileSync(path.join(repositoryRoot, 'skills/synonbiomed', folder, 'SKILL.md'), 'utf8');
      const match = content.match(/^name:\s*['"]?([^'"\r\n]+)['"]?\s*$/m);
      if (!match) throw new Error(`Missing skill name in ${folder}/SKILL.md`);
      return match[1].trim();
    });
    expect(Object.keys(presentation.skills).toSorted()).toEqual(skillNames.toSorted());

    const uncategorized = skillNames.filter((name) => categoryForName(name) === null);
    expect(uncategorized).toEqual([]);

    const options = getSynonBiomedSkillCategoryOptions(
      skillNames.map((name) => ({ name, category: presentation.skills[name]?.category })),
      'zh-CN'
    );
    expect(skillNames.length).toBeGreaterThanOrEqual(94);
    expect(options[0]).toEqual({ id: 'all', label: '全部领域', count: skillNames.length });
    expect(options.some((option) => option.id === 'uncategorized')).toBe(false);
  });
});
