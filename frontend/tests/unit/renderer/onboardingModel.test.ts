import {
  disabledNetworkGroupIds,
  initialEnabledState,
  initialNetworkState,
  nextOnboardingStep,
  previousOnboardingStep,
  toggleGroup,
} from '@/renderer/pages/onboarding/onboardingModel';
import type { OnboardingSnapshot } from '@/renderer/services/onboardingService';
import { describe, expect, it } from 'vitest';

const snapshot = {
  assistantId: 'synonbiomed:operon',
  complete: false,
  networkGroups: [
    { id: 'required', label: 'Required', description: '', locked: true, domains: [] },
    { id: 'literature', label: 'Literature', description: '', locked: false, domains: [] },
    { id: 'data', label: 'Data', description: '', locked: false, domains: [] },
  ],
  disabledNetworkGroupIds: ['literature', 'required'],
  connectors: [],
  skills: [],
} satisfies OnboardingSnapshot;

describe('onboarding state model', () => {
  it('keeps locked network groups enabled and returns only disabled mutable groups', () => {
    const state = initialNetworkState(snapshot);
    expect(state).toEqual({ required: true, literature: false, data: true });
    expect(disabledNetworkGroupIds(snapshot, state)).toEqual(['literature']);
  });

  it('builds and updates enabled maps without mutating the previous value', () => {
    const initial = initialEnabledState(
      [
        { id: 'a', enabled: true },
        { id: 'b', enabled: false },
      ],
      (item) => item.id
    );
    const updated = toggleGroup(initial, ['a', 'b'], false);
    expect(initial).toEqual({ a: true, b: false });
    expect(updated).toEqual({ a: false, b: false });
  });

  it('bounds forward and backward navigation', () => {
    expect(nextOnboardingStep(5)).toBe(5);
    expect(nextOnboardingStep(4)).toBe(5);
    expect(nextOnboardingStep(2)).toBe(3);
    expect(previousOnboardingStep(0)).toBe(0);
    expect(previousOnboardingStep(2)).toBe(1);
    expect(previousOnboardingStep(5)).toBe(4);
  });
});
