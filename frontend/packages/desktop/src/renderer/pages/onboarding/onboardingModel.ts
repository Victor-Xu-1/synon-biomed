import type { OnboardingSnapshot } from '@/renderer/services/onboardingService';

export const ONBOARDING_STEP_COUNT = 5;

export type OnboardingStep = 0 | 1 | 2 | 3 | 4;

export function nextOnboardingStep(step: OnboardingStep): OnboardingStep {
  return Math.min(step + 1, ONBOARDING_STEP_COUNT - 1) as OnboardingStep;
}

export function previousOnboardingStep(step: OnboardingStep): OnboardingStep {
  return Math.max(step - 1, 0) as OnboardingStep;
}

export function initialEnabledState<T extends { enabled: boolean }>(
  items: T[],
  key: (item: T) => string,
  enabled: (item: T) => boolean = (item) => item.enabled
): Record<string, boolean> {
  return Object.fromEntries(items.map((item) => [key(item), enabled(item)]));
}

export function initialNetworkState(snapshot: OnboardingSnapshot): Record<string, boolean> {
  const disabled = new Set(snapshot.disabledNetworkGroupIds);
  return Object.fromEntries(snapshot.networkGroups.map((group) => [group.id, group.locked || !disabled.has(group.id)]));
}

export function disabledNetworkGroupIds(
  snapshot: Pick<OnboardingSnapshot, 'networkGroups'>,
  enabled: Record<string, boolean>
): string[] {
  return snapshot.networkGroups
    .filter((group) => !group.locked && enabled[group.id] === false)
    .map((group) => group.id)
    .toSorted();
}

export function toggleGroup(
  values: Record<string, boolean>,
  keys: string[],
  enabled: boolean
): Record<string, boolean> {
  const next = { ...values };
  for (const key of keys) next[key] = enabled;
  return next;
}
