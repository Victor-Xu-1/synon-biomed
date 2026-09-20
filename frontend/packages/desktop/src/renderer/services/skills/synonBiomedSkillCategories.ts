/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

export type SynonBiomedSkillCategoryId =
  | 'drug-discovery'
  | 'structural-biology'
  | 'omics-bioinformatics'
  | 'dmpk-nonclinical'
  | 'cmc-manufacturing'
  | 'clinical-regulatory'
  | 'research-workflows'
  | 'compute-platform';

export type SynonBiomedSkillCategorySelection = 'all' | 'uncategorized' | SynonBiomedSkillCategoryId;

type SkillCategoryDefinition = {
  id: SynonBiomedSkillCategoryId;
  zh: string;
  en: string;
};

// Skill category assignments come from the API; only presentation labels live here.
const CATEGORY_DEFINITIONS: SkillCategoryDefinition[] = [
  {
    id: 'drug-discovery',
    zh: '药物发现与计算化学',
    en: 'Drug Discovery & Computational Chemistry',
  },
  {
    id: 'structural-biology',
    zh: '结构生物学与蛋白质工程',
    en: 'Structural Biology & Protein Engineering',
  },
  {
    id: 'omics-bioinformatics',
    zh: '组学与生物信息学',
    en: 'Omics & Bioinformatics',
  },
  {
    id: 'dmpk-nonclinical',
    zh: 'DMPK、非临床与安全性',
    en: 'DMPK, Nonclinical & Safety',
  },
  {
    id: 'cmc-manufacturing',
    zh: '药剂、CMC与生产工艺',
    en: 'Formulation, CMC & Manufacturing',
  },
  {
    id: 'clinical-regulatory',
    zh: '临床开发、注册与上市后',
    en: 'Clinical, Regulatory & Post-Market',
  },
  {
    id: 'research-workflows',
    zh: '科研知识与实验数据工作流',
    en: 'Research Knowledge & Experimental Data Workflows',
  },
  {
    id: 'compute-platform',
    zh: '计算基础设施与平台能力',
    en: 'Computing Infrastructure & Platform Capabilities',
  },
];

export type SynonBiomedSkillCategoryOption = {
  id: SynonBiomedSkillCategorySelection;
  label: string;
  count: number;
};

export type CategorizedSkill = { name: string; category?: string | null };

export function resolveSynonBiomedSkillCategory(skill: CategorizedSkill): SynonBiomedSkillCategoryId | null {
  const declared = skill.category?.trim().toLowerCase();
  return CATEGORY_DEFINITIONS.find((category) => category.id === declared)?.id ?? null;
}

export function getSynonBiomedSkillCategoryLabel(category: string | null | undefined, language?: string): string {
  const definition = CATEGORY_DEFINITIONS.find((item) => item.id === category?.trim().toLowerCase());
  if (!definition) return category?.trim() ?? '';
  return language?.toLowerCase().startsWith('zh') ? definition.zh : definition.en;
}

export function getSynonBiomedSkillCategoryOptions(
  skills: CategorizedSkill[],
  language?: string
): SynonBiomedSkillCategoryOption[] {
  const counts = new Map<SynonBiomedSkillCategoryId, number>();
  let uncategorized = 0;
  for (const skill of skills) {
    const category = resolveSynonBiomedSkillCategory(skill);
    if (category) counts.set(category, (counts.get(category) ?? 0) + 1);
    else uncategorized += 1;
  }

  const isChinese = language?.toLowerCase().startsWith('zh');
  const options: SynonBiomedSkillCategoryOption[] = [
    { id: 'all', label: isChinese ? '全部领域' : 'All fields', count: skills.length },
  ];
  for (const definition of CATEGORY_DEFINITIONS) {
    const count = counts.get(definition.id) ?? 0;
    if (count === 0) continue;
    options.push({ id: definition.id, label: isChinese ? definition.zh : definition.en, count });
  }
  if (uncategorized > 0) {
    options.push({
      id: 'uncategorized',
      label: isChinese ? '未分类' : 'Uncategorized',
      count: uncategorized,
    });
  }
  return options;
}
