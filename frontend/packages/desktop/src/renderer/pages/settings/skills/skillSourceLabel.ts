import type { TFunction } from 'i18next';

export function skillSourceLabel(source?: string, t?: TFunction): string {
  if (!source) return '';
  const normalized = source.trim().toLocaleLowerCase();
  const labels: Record<string, string> = {
    bundled: 'builtin',
    synon_llm: 'builtin',
    marketplace: 'imported',
    github: 'github',
    personal: 'personal',
    'personal-draft': 'personal',
    custom: 'custom',
    user: 'personal',
  };
  const labelKey = labels[normalized];
  return labelKey && t ? t(`settings.skillsSettings.sources.${labelKey}`) : source;
}
