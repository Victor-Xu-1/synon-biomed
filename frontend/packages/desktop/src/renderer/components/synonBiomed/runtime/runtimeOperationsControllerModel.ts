import type { TConversationRuntimeSummary } from '@/common/config/storage';
import type { SynonBiomedRuntimeInvalidation } from '@/renderer/services/synonBiomedRuntimeOperations';
import type { SynonBiomedRuntimeSnapshot } from './runtimeOperationsModel';

export const TERMINAL_RUNTIME_STATUSES = new Set<NonNullable<SynonBiomedRuntimeInvalidation['terminal_status']>>([
  'completed',
  'failed',
  'cancelled',
]);

export const OFFLINE_RECOVERY_INTERVAL_MS = 2500;
export const MAX_OFFLINE_RECOVERY_INTERVAL_MS = 30_000;
export const ACTIVE_RUNTIME_SNAPSHOT_INTERVAL_MS = 5000;
export const ACTIVE_RUNTIME_STATES = new Set([
  'running',
  'starting',
  'waiting_approval',
  'waiting_confirmation',
  'waiting_input',
  'cancelling',
]);

const PAUSE_TRANSITION_SETTLED_STATUSES = new Set([
  'paused',
  'cancelled',
  'canceled',
  'failed',
  'completed',
  'finished',
  'succeeded',
]);

/**
 * A successful stop request is only locally complete once the authoritative
 * task snapshot reaches an explicit settled state. Until then the previous
 * processing snapshot is stale and must not be projected as running again.
 */
export const isRuntimePauseTransitionSettled = (snapshot: SynonBiomedRuntimeSnapshot | null): boolean =>
  Boolean(snapshot && PAUSE_TRANSITION_SETTLED_STATUSES.has(snapshot.status.trim().toLowerCase()));

export const projectAcceptedRuntimeStart = (
  snapshot: SynonBiomedRuntimeSnapshot | null,
  acceptedStartPending: boolean
): SynonBiomedRuntimeSnapshot | null => {
  if (!snapshot || snapshot.status !== 'completed' || snapshot.runtimeAttempt !== null || !acceptedStartPending) {
    return snapshot;
  }
  return {
    ...snapshot,
    status: 'starting',
    statusDescription: null,
    error: null,
    failureKind: null,
    completedAt: null,
    runtimeActive: true,
    runtimeTaskFinishedAt: null,
    runtimeTaskActive: true,
    canCancel: false,
    canResume: false,
  };
};

export const offlineRecoveryDelay = (consecutiveFailures: number): number =>
  Math.min(OFFLINE_RECOVERY_INTERVAL_MS * 2 ** consecutiveFailures, MAX_OFFLINE_RECOVERY_INTERVAL_MS);

export const runtimeSummaryChanged = (
  previous: TConversationRuntimeSummary | null,
  next: TConversationRuntimeSummary
): boolean => {
  if (!previous) return true;
  return (
    previous.state !== next.state ||
    previous.can_send_message !== next.can_send_message ||
    previous.has_task !== next.has_task ||
    previous.task_status !== next.task_status ||
    previous.is_processing !== next.is_processing ||
    previous.pending_confirmations !== next.pending_confirmations ||
    previous.turn_id !== next.turn_id
  );
};
