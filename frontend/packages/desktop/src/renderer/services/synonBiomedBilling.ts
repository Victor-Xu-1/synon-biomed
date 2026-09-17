export type SynonBiomedBillingRecord = {
  id: string;
  issuedAt: string;
  amount: number;
  currency: string;
};

export type SynonBiomedBillingOverview = {
  plan: 'local-free';
  metering: 'credit-draft';
  credits: {
    initial: number;
    used: number;
    remaining: number;
  };
  modelRates: readonly SynonBiomedModelRate[];
  bills: readonly SynonBiomedBillingRecord[];
  invoices: readonly SynonBiomedBillingRecord[];
};

export type SynonBiomedModelRate = {
  id: 'light' | 'standard' | 'reasoning';
  multiplier: number;
  tokensPerCredit: number;
  capacityTokens: number;
};

export const SYNON_BIOMED_INITIAL_CREDITS = 10_000;
export const SYNON_BIOMED_BASE_TOKENS_PER_CREDIT = 1_000;

const MODEL_RATE_CONFIG = [
  { id: 'light', multiplier: 0.5 },
  { id: 'standard', multiplier: 1 },
  { id: 'reasoning', multiplier: 2 },
] as const;

function buildModelRate(id: SynonBiomedModelRate['id'], multiplier: number): SynonBiomedModelRate {
  return Object.freeze({
    id,
    multiplier,
    tokensPerCredit: SYNON_BIOMED_BASE_TOKENS_PER_CREDIT / multiplier,
    capacityTokens: Math.floor((SYNON_BIOMED_INITIAL_CREDITS * SYNON_BIOMED_BASE_TOKENS_PER_CREDIT) / multiplier),
  });
}

const MODEL_RATES = Object.freeze(MODEL_RATE_CONFIG.map(({ id, multiplier }) => buildModelRate(id, multiplier)));

export function calculateSynonBiomedCredits(totalTokens: number, multiplier: number): number {
  if (!Number.isFinite(totalTokens) || !Number.isFinite(multiplier) || totalTokens <= 0 || multiplier <= 0) return 0;
  return Math.ceil((Math.floor(totalTokens) / SYNON_BIOMED_BASE_TOKENS_PER_CREDIT) * multiplier);
}

const LOCAL_BILLING_OVERVIEW: SynonBiomedBillingOverview = Object.freeze({
  plan: 'local-free',
  metering: 'credit-draft',
  credits: Object.freeze({
    initial: SYNON_BIOMED_INITIAL_CREDITS,
    used: 0,
    remaining: SYNON_BIOMED_INITIAL_CREDITS,
  }),
  modelRates: MODEL_RATES,
  bills: Object.freeze([]),
  invoices: Object.freeze([]),
});

export function getSynonBiomedBillingOverview(): SynonBiomedBillingOverview {
  return LOCAL_BILLING_OVERVIEW;
}
