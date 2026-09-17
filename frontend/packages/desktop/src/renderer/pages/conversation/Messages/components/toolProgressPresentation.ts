import type { NormalizedToolProgress } from '@/common/chat/normalizeToolCall';
import { toolPublicDetailText, type ToolPublicDetailTextKey } from '@/renderer/services/i18n/toolPublicDetailLocale';

export type ToolProgressPublicPresentation = {
  phaseLabel: string;
  overallPercent: number | null;
  phasePercent: number | null;
  elapsedLabel: string | null;
  compactDetail: string;
  compactResult: string | null;
  rows: Array<{ label: string; value: string }>;
};

const PHASE_LABEL_KEYS: Record<string, ToolPublicDetailTextKey> = {
  queued: 'progressQueued',
  operation_completed: 'progressCompleted',
  operation_failed: 'progressFailed',
  operation_cancelled: 'progressCancelled',
  operation_interrupted: 'progressInterrupted',
  waiting_for_installer: 'progressWaitingForInstaller',
  preparing_environment: 'progressPreparingEnvironment',
  creating_environment: 'progressCreatingEnvironment',
  configuring_environment: 'progressConfiguringEnvironment',
  installing_dependencies: 'progressInstallingDependencies',
  resolving_dependencies: 'progressResolvingDependencies',
  resolving_python_packages: 'progressResolvingPythonPackages',
  preparing_transaction: 'progressPreparingTransaction',
  downloading_packages: 'progressDownloadingPackages',
  extracting_packages: 'progressExtractingPackages',
  building_packages: 'progressBuildingPackages',
  installing_packages: 'progressInstallingPackages',
  installer_process_completed: 'progressInstallerProcessCompleted',
  dependencies_installed: 'progressDependenciesInstalled',
  inspecting_environment: 'progressInspectingEnvironment',
  dependency_inventory_verified: 'progressDependencyInventoryVerified',
  validating_environment: 'progressValidatingEnvironment',
  verifying_environment: 'progressValidatingEnvironment',
  staging_environment_validated: 'progressStagingEnvironmentValidated',
  materializing_generation: 'progressMaterializingGeneration',
  generation_materialized: 'progressGenerationMaterialized',
  validating_generation: 'progressValidatingGeneration',
  generation_validated: 'progressGenerationValidated',
  publishing_environment: 'progressPublishingEnvironment',
  environment_published: 'progressEnvironmentPublished',
  activating_environment: 'progressActivatingEnvironment',
  environment_ready: 'progressEnvironmentReady',
  downloading_file: 'progressDownloadingFile',
  verifying_download: 'progressVerifyingDownload',
  download_verified: 'progressDownloadVerified',
  publishing_download: 'progressPublishingDownload',
  download_ready: 'progressDownloadReady',
};

const roundedPercent = (value: number | undefined): number | null =>
  typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= 100 ? Math.round(value) : null;

const elapsedLabel = (elapsedMs: number | undefined): string | null => {
  if (typeof elapsedMs !== 'number' || !Number.isFinite(elapsedMs) || elapsedMs < 0) return null;
  const totalSeconds = Math.floor(elapsedMs / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  return hours > 0
    ? `${hours}:${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`
    : `${minutes}:${String(seconds).padStart(2, '0')}`;
};

const transferRateLabel = (bytesPerSecond: number | undefined): string | null => {
  if (typeof bytesPerSecond !== 'number' || !Number.isFinite(bytesPerSecond) || bytesPerSecond < 0) return null;
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s'] as const;
  let value = bytesPerSecond;
  let unitIndex = 0;
  while (value >= 1000 && unitIndex < units.length - 1) {
    value /= 1000;
    unitIndex += 1;
  }
  const digits = value >= 100 || unitIndex === 0 ? 0 : value >= 10 ? 1 : 2;
  return `${Number(value.toFixed(digits))} ${units[unitIndex]}`;
};

const byteCountLabel = (bytes: number): string => {
  const units = ['B', 'KB', 'MB', 'GB'] as const;
  let value = bytes;
  let unitIndex = 0;
  while (value >= 1000 && unitIndex < units.length - 1) {
    value /= 1000;
    unitIndex += 1;
  }
  const digits = value >= 100 || unitIndex === 0 ? 0 : value >= 10 ? 1 : 2;
  return `${Number(value.toFixed(digits))} ${units[unitIndex]}`;
};

export function buildToolProgressPublicPresentation(
  progress: NormalizedToolProgress,
  language: string
): ToolProgressPublicPresentation {
  const chinese = language.toLowerCase().startsWith('zh');
  const phaseKey = Object.hasOwn(PHASE_LABEL_KEYS, progress.phase)
    ? PHASE_LABEL_KEYS[progress.phase]
    : 'progressProcessing';
  const phaseLabel = toolPublicDetailText(chinese, phaseKey);
  const phasePercent = roundedPercent(progress.phasePercent);
  const milestonePercent =
    typeof progress.completedItems === 'number' &&
    typeof progress.totalItems === 'number' &&
    progress.totalItems > 0 &&
    progress.completedItems <= progress.totalItems
      ? Math.round((progress.completedItems / progress.totalItems) * 100)
      : null;
  const hasByteProgress =
    typeof progress.bytesCompleted === 'number' &&
    typeof progress.bytesTotal === 'number' &&
    progress.bytesTotal > 0 &&
    progress.bytesCompleted <= progress.bytesTotal;
  const bytePercent = hasByteProgress ? Math.round((progress.bytesCompleted! / progress.bytesTotal!) * 100) : null;
  const overallPercent = milestonePercent;
  const elapsed = elapsedLabel(progress.elapsedMs);
  const transferRate = transferRateLabel(progress.bytesPerSecond);
  const transferred = hasByteProgress
    ? `${byteCountLabel(progress.bytesCompleted!)} / ${byteCountLabel(progress.bytesTotal!)}`
    : typeof progress.bytesCompleted === 'number' &&
        Number.isFinite(progress.bytesCompleted) &&
        progress.bytesCompleted >= 0
      ? byteCountLabel(progress.bytesCompleted)
      : null;
  const remaining = hasByteProgress ? byteCountLabel(progress.bytesTotal! - progress.bytesCompleted!) : null;
  const detailParts = [phaseLabel];
  if (phasePercent !== null)
    detailParts.push(
      toolPublicDetailText(
        chinese,
        hasByteProgress ? 'progressDownloadPercentCompact' : 'progressPhasePercentCompact',
        {
          percent: phasePercent,
        }
      )
    );
  if (transferred) detailParts.push(transferred);
  if (transferRate) detailParts.push(transferRate);
  if (remaining) detailParts.push(toolPublicDetailText(chinese, 'progressRemainingCompact', { remaining }));
  if (elapsed) detailParts.push(toolPublicDetailText(chinese, 'progressElapsedCompact', { elapsed }));

  const rows: Array<{ label: string; value: string }> = [
    {
      label: toolPublicDetailText(chinese, 'progressCurrentPhase'),
      value: phaseLabel,
    },
  ];
  if (overallPercent !== null) {
    rows.push({
      label: toolPublicDetailText(chinese, 'progressOverall'),
      value: `${overallPercent}%`,
    });
  }
  const detailedPhasePercent = bytePercent ?? phasePercent;
  if (detailedPhasePercent !== null) {
    rows.push({
      label: toolPublicDetailText(chinese, hasByteProgress ? 'progressDownload' : 'progressPhase'),
      value: `${detailedPhasePercent}%`,
    });
  }
  if (transferred) {
    rows.push({
      label: toolPublicDetailText(chinese, 'progressTransferred'),
      value: transferred,
    });
  }
  if (transferRate) {
    rows.push({ label: toolPublicDetailText(chinese, 'progressTransferRate'), value: transferRate });
  }
  if (remaining) {
    rows.push({ label: toolPublicDetailText(chinese, 'progressRemaining'), value: remaining });
  }
  if (
    typeof progress.completedItems === 'number' &&
    typeof progress.totalItems === 'number' &&
    progress.totalItems > 0
  ) {
    rows.push({
      label: toolPublicDetailText(chinese, 'progressMilestones'),
      value: `${Math.trunc(progress.completedItems)} / ${Math.trunc(progress.totalItems)}`,
    });
  }
  if (elapsed)
    rows.push({
      label: toolPublicDetailText(chinese, 'progressElapsed'),
      value: elapsed,
    });

  const compactPercent = bytePercent ?? phasePercent;
  return {
    phaseLabel,
    overallPercent,
    phasePercent,
    elapsedLabel: elapsed,
    compactDetail: detailParts.join(' · '),
    compactResult:
      milestonePercent !== null
        ? toolPublicDetailText(chinese, 'progressStepsCompact', {
            completed: Math.trunc(progress.completedItems!),
            total: Math.trunc(progress.totalItems!),
          })
        : compactPercent === null
          ? null
          : toolPublicDetailText(chinese, bytePercent !== null ? 'progressDownloadResult' : 'progressPhaseResult', {
              percent: compactPercent,
            }),
    rows,
  };
}
