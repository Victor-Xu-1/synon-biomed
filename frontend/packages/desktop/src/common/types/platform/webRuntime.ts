export type BackendStartupFailureReason = 'synon_biomed_backend_unavailable';

export interface BackendStartupFailureInfo {
  reason: BackendStartupFailureReason;
  message?: string;
}

declare global {
  interface Window {
    __initialLanguage?: string | null;
    __synonAiE2ETest?: boolean;
    __backendStartupFailed?: boolean;
    __backendStartupFailure?: BackendStartupFailureInfo | null;
    __installationIntegrityReportCount?: number;
    __lastInstallationIntegrityReportMessage?: string;
  }
}
