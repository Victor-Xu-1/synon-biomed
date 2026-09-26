import type { ScientificRuntimeOption } from '@/renderer/services/scientificRuntimeSettings';
import type { SettingsGeneratedIconId } from '../components/SettingsGeneratedAsset';

export const environmentCategories = ['all', 'core', 'structure', 'medicine', 'omics', 'imaging', 'other'] as const;
export type EnvironmentCategory = (typeof environmentCategories)[number];
export type EnvironmentFilter = 'all' | 'ready' | 'available' | 'active' | 'failed';
export const activeEnvironmentStates = new Set(['scheduled', 'preparing', 'retrying', 'uninstalling']);

const categories: Record<string, EnvironmentCategory> = {
  'synon-biomed-python': 'core',
  'synon-biomed-r': 'core',
  'common-structure-toolkit': 'structure',
  'structure-interaction': 'structure',
  'biomolecular-electrostatics': 'structure',
  'autodock-vina': 'structure',
  'molecular-conversion-quantum': 'structure',
  'molecular-simulation': 'structure',
  'drug-chemistry-process': 'medicine',
  'qsar-admet-classical': 'medicine',
  'clinical-pharmacometrics': 'medicine',
  'single-cell-omics': 'omics',
  'genomics-command-line': 'omics',
  'medical-imaging': 'imaging',
  'instrument-data-analytics': 'imaging',
};
export const environmentCategory = (id: string): EnvironmentCategory => categories[id] ?? 'other';

const categoryIcons: Record<EnvironmentCategory, SettingsGeneratedIconId> = {
  all: 'connector-molecule',
  core: 'connector-molecule',
  structure: 'connector-molecule',
  medicine: 'connector-clipboard',
  omics: 'connector-dna',
  imaging: 'connector-database',
  other: 'connector-book',
};

export const environmentIcon = (id: string): SettingsGeneratedIconId => categoryIcons[environmentCategory(id)];

export function matchesEnvironmentFilter(item: ScientificRuntimeOption, filter: EnvironmentFilter): boolean {
  if (filter === 'ready') return item.status === 'ready';
  if (filter === 'active') return activeEnvironmentStates.has(item.status);
  if (filter === 'failed') return item.status === 'failed' || item.status === 'stopped';
  if (filter === 'available')
    return item.available && item.status !== 'ready' && !activeEnvironmentStates.has(item.status);
  return true;
}
export const environmentStateKeys: Record<string, string> = {
  waiting_for_selection: 'softwareWaiting',
  scheduled: 'softwareQueued',
  preparing: 'softwarePreparing',
  retrying: 'softwareRetrying',
  uninstalling: 'softwareUninstalling',
  ready: 'softwareReady',
  failed: 'softwareFailed',
  stopped: 'softwareStopped',
  disabled: 'softwareUnavailable',
};
