export type SkillDescriptionLocale = 'en-US' | 'zh-CN';

// Localized descriptions are supplied by the catalog API from the same
// reviewed metadata that ships alongside the Skill contracts.
export function normalizeSkillDescriptionLocale(language?: string): SkillDescriptionLocale {
  return language?.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en-US';
}

export function resolveSkillDescription(
  name: string,
  description: string,
  language: string | undefined,
  descriptionI18n?: Record<string, string>
): string {
  const locale = normalizeSkillDescriptionLocale(language);
  const localized = descriptionI18n?.[locale] || descriptionI18n?.[locale.split('-')[0]];
  if (localized?.trim()) return localized.trim();
  return description.trim() || name;
}
