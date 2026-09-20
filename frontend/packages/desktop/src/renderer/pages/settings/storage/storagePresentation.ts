import type {
  SynonBiomedDataDirectory,
  SynonBiomedStorageRuleValues,
} from '@/renderer/services/synonBiomedWorkspaceSettings';

export function formatStorageBytes(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value) || value < 0) return '—';
  if (value === 0) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  const index = Math.max(0, Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1));
  return `${(value / 1024 ** index).toFixed(index > 1 ? 1 : 0)} ${units[index]}`;
}

export function directorySourceKey(source: string | undefined): string {
  const sources: Record<string, string> = {
    flag: 'sourceFlag',
    env: 'sourceEnv',
    config: 'sourceConfig',
    pointer: 'sourcePointer',
  };
  return `settings.storageSettings.${sources[source ?? ''] ?? 'sourceDefault'}`;
}

export function directoryChangeAllowed(directory: SynonBiomedDataDirectory | null): boolean {
  return directory !== null && directory.activeFrames === 0 && !directory.pendingMove;
}

export const storageRuleRows: Array<{ key: keyof SynonBiomedStorageRuleValues; label: string; description: string }> = [
  {
    key: 'taskArtifacts',
    label: 'settings.storageSettings.taskArtifacts',
    description: 'settings.storageSettings.taskArtifactsDescription',
  },
  {
    key: 'logs',
    label: 'settings.storageSettings.taskLogs',
    description: 'settings.storageSettings.taskLogsDescription',
  },
  {
    key: 'toolResults',
    label: 'settings.storageSettings.toolResultsDirectory',
    description: 'settings.storageSettings.toolResultsDirectoryDescription',
  },
  {
    key: 'temp',
    label: 'settings.storageSettings.tempDirectory',
    description: 'settings.storageSettings.tempDirectoryDescription',
  },
];
