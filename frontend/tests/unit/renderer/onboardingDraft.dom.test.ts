import { beforeEach, describe, expect, it } from 'vitest';
import { clearOnboardingDraft, loadOnboardingDraft, saveOnboardingDraft } from '@/renderer/services/onboardingDraft';

const draft = {
  step: 4 as const,
  networkEnabled: { required: true, external: false },
  connectorEnabled: { pubmed: true },
  skillEnabled: { literature: true },
  scientificRuntimeEnabled: { 'common-structure-toolkit': true, 'autodock-vina': false },
  profileSummary: 'Translational genomics',
  attachmentNames: ['cohort.csv'],
  selectedTask: '',
  customTask: 'Reproduce the survival analysis',
};

describe('onboarding draft', () => {
  beforeEach(() => window.localStorage.clear());

  it('round-trips the complete versioned draft atomically for one owner', () => {
    saveOnboardingDraft('owner-a', draft);
    expect(loadOnboardingDraft('owner-a')).toEqual({ version: 1, ...draft });
    expect(loadOnboardingDraft('owner-b')).toBeNull();
  });

  it('fails closed and removes malformed drafts', () => {
    window.localStorage.setItem('synonbiomed.onboarding.draft.v1:owner-a', '{');
    expect(loadOnboardingDraft('owner-a')).toBeNull();
    expect(window.localStorage.getItem('synonbiomed.onboarding.draft.v1:owner-a')).toBeNull();
  });

  it('clears only the completed owner draft', () => {
    saveOnboardingDraft('owner-a', draft);
    saveOnboardingDraft('owner-b', draft);
    clearOnboardingDraft('owner-a');
    expect(loadOnboardingDraft('owner-a')).toBeNull();
    expect(loadOnboardingDraft('owner-b')).not.toBeNull();
  });

  it('does not invent a frontend file-count gate absent from the upload contract', () => {
    const attachmentNames = Array.from({ length: 64 }, (_, index) => `evidence-${index}.csv`);
    saveOnboardingDraft('owner-a', { ...draft, attachmentNames });
    expect(loadOnboardingDraft('owner-a')?.attachmentNames).toEqual(attachmentNames);
  });
});
