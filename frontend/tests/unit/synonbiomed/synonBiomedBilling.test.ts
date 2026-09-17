import { describe, expect, it } from 'vitest';
import {
  calculateSynonBiomedCredits,
  getSynonBiomedBillingOverview,
  SYNON_BIOMED_BASE_TOKENS_PER_CREDIT,
  SYNON_BIOMED_INITIAL_CREDITS,
} from '@/renderer/services/synonBiomedBilling';

describe('Synon Biomed credit draft', () => {
  it('uses 10,000 credits and 1,000 tokens per credit as the standard baseline', () => {
    const overview = getSynonBiomedBillingOverview();

    expect(overview.credits).toEqual({
      initial: 10_000,
      used: 0,
      remaining: 10_000,
    });
    expect(SYNON_BIOMED_INITIAL_CREDITS * SYNON_BIOMED_BASE_TOKENS_PER_CREDIT).toBe(10_000_000);
    expect(overview.modelRates.map((rate) => rate.tokensPerCredit)).toEqual([2_000, 1_000, 500]);
  });

  it('rounds each request up and rejects invalid model inputs', () => {
    expect(calculateSynonBiomedCredits(1_000, 1)).toBe(1);
    expect(calculateSynonBiomedCredits(1_001, 1)).toBe(2);
    expect(calculateSynonBiomedCredits(1_000, 0.5)).toBe(1);
    expect(calculateSynonBiomedCredits(1_000, 2)).toBe(2);
    expect(calculateSynonBiomedCredits(-1, 1)).toBe(0);
    expect(calculateSynonBiomedCredits(Number.NaN, 1)).toBe(0);
    expect(calculateSynonBiomedCredits(1_000, 0)).toBe(0);
  });
});
