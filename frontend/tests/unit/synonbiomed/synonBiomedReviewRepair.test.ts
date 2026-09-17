import { describe, expect, it } from 'vitest';
import { buildSynonBiomedReviewRepairPrompt } from '@/renderer/components/synonBiomed/runtime/synonBiomedReviewRepair';
import type { SynonBiomedVerificationCheck } from '@/renderer/services/synonBiomedAnnotations';

const check = (overrides: Partial<SynonBiomedVerificationCheck> = {}): SynonBiomedVerificationCheck => ({
  id: 'check-1',
  rootFrameId: 'root-frame',
  artifactVersionId: null,
  claimId: null,
  claim: 'The claim needs a source.',
  verdict: 'fail',
  severity: 'high',
  evidence: 'The cited paper does not support it.',
  rebuttal: 'Replace the claim with a supported statement.',
  reviewerIndex: 0,
  reviewerModel: 'reviewer-model',
  reviewerFrameId: 'review-frame',
  sourceRef: null,
  status: 'unaddressed',
  reflagCount: null,
  createdAt: '2026-08-10T08:02:03Z',
  ...overrides,
});

describe('buildSynonBiomedReviewRepairPrompt', () => {
  it('includes actionable review context in the Chinese repair prompt', () => {
    const prompt = buildSynonBiomedReviewRepairPrompt([check()], 'zh-CN');

    expect(prompt).toContain('修复上一轮任务的结果并重新生成完整最终答案');
    expect(prompt).toContain('The claim needs a source.');
    expect(prompt).toContain('依据: The cited paper does not support it.');
    expect(prompt).toContain('补充说明: Replace the claim with a supported statement.');
  });

  it('omits passing checks from the repair prompt', () => {
    const prompt = buildSynonBiomedReviewRepairPrompt(
      [check(), check({ id: 'check-2', verdict: 'pass', claim: 'Already verified.' })],
      'en-US'
    );

    expect(prompt).toContain('The claim needs a source.');
    expect(prompt).not.toContain('Already verified.');
  });
});
