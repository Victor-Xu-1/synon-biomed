import type { TFunction } from 'i18next';
import type { SynonBiomedExpertProfile } from './synonBiomedExpertProfiles';
import enSettings from '../i18n/locales/en-US/settings.json';
import zhSettings from '../i18n/locales/zh-CN/settings.json';

const BUILTIN_EXPERT_TRANSLATION_KEYS = {
  OPERON: 'operon',
  REVIEWER: 'reviewer',
  BOOKMARKER: 'bookmarker',
  ONBOARDING: 'onboarding',
  AIDD_EXPERT: 'aidd',
  MEDCHEM_EXPERT: 'medchem',
  ONCOLOGY_EXPERT: 'oncology',
  IMMUNOLOGY_EXPERT: 'immunology',
  STRUCTURAL_BIOLOGY_EXPERT: 'structuralBiology',
  COMPUTATIONAL_CHEM_EXPERT: 'computationalChemistry',
  DMPK_EXPERT: 'dmpk',
  GENOMICS_BIOINFO_EXPERT: 'genomicsBioinformatics',
  NEUROSCIENCE_EXPERT: 'neuroscience',
  CLINICAL_DEV_EXPERT: 'clinicalDevelopment',
} as const;

type BuiltinExpertName = keyof typeof BUILTIN_EXPERT_TRANSLATION_KEYS;

export type SynonBiomedBuiltinExpertText = {
  'en-US': { displayName: string; description: string };
  'zh-CN': { displayName: string; description: string };
};

export function resolveSynonBiomedBuiltinExpertText(name: string): SynonBiomedBuiltinExpertText | null {
  const expertName = name.trim().toUpperCase();
  if (!isBuiltinExpertName(expertName)) return null;

  const resource = BUILTIN_EXPERT_TRANSLATION_KEYS[expertName];
  return {
    'en-US': enSettings.expertsSettings.builtinProfiles[resource],
    'zh-CN': zhSettings.expertsSettings.builtinProfiles[resource],
  };
}

export function localizeSynonBiomedExpertProfile(
  profile: SynonBiomedExpertProfile,
  translate: TFunction
): SynonBiomedExpertProfile {
  if (profile.source === 'user') return profile;

  const expertName = profile.name.trim().toUpperCase();
  if (!isBuiltinExpertName(expertName)) return profile;

  const resource = BUILTIN_EXPERT_TRANSLATION_KEYS[expertName];
  const resourceRoot = `settings.expertsSettings.builtinProfiles.${resource}`;
  return {
    ...profile,
    displayName: translate(`${resourceRoot}.displayName`),
    description: translate(`${resourceRoot}.description`),
  };
}

function isBuiltinExpertName(value: string): value is BuiltinExpertName {
  return Object.hasOwn(BUILTIN_EXPERT_TRANSLATION_KEYS, value);
}
