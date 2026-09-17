import { describe, expect, it } from 'vitest';
import { aggregateAccountActivity, type AccountActivityDay } from '@/renderer/services/account/accountInsightsModel';

function activityDays(): AccountActivityDay[] {
  const start = new Date(Date.UTC(2025, 7, 10));
  return Array.from({ length: 53 * 7 }, (_, index) => {
    const date = new Date(start);
    date.setUTCDate(start.getUTCDate() + index);
    return {
      date: date.toISOString().slice(0, 10),
      count: index === 368 ? 2 : index === 369 ? 3 : 0,
      tokenCount: index === 368 ? 12_400 : index === 369 ? 7_600 : 0,
      isFuture: index === 370,
    };
  });
}

describe('accountInsightsModel', () => {
  it('aggregates one server calendar into daily, weekly, and monthly visual buckets', () => {
    const days = activityDays();
    const daily = aggregateAccountActivity(days, 'daily', 'en-US');
    const weekly = aggregateAccountActivity(days, 'weekly', 'en-US');
    const cumulative = aggregateAccountActivity(days, 'cumulative', 'en-US');

    expect(daily).toHaveLength(371);
    expect(weekly).toHaveLength(53);
    expect(cumulative).toHaveLength(12);
    expect(daily.reduce((sum, bucket) => sum + bucket.count, 0)).toBe(5);
    expect(weekly.reduce((sum, bucket) => sum + bucket.count, 0)).toBe(5);
    expect(cumulative.reduce((sum, bucket) => sum + bucket.count, 0)).toBe(5);
    expect(daily.reduce((sum, bucket) => sum + bucket.tokenCount, 0)).toBe(20_000);
    expect(weekly.reduce((sum, bucket) => sum + bucket.tokenCount, 0)).toBe(20_000);
    expect(cumulative.reduce((sum, bucket) => sum + bucket.tokenCount, 0)).toBe(20_000);
    expect(daily.at(-3)?.axisLabel).toBe('8/13');
  });

  it('keeps future and zero-activity buckets visually inactive', () => {
    const buckets = aggregateAccountActivity(activityDays(), 'daily', 'zh-CN');
    expect(buckets.at(-1)).toMatchObject({ isFuture: true, level: 0 });
    expect(buckets[0]).toMatchObject({ count: 0, level: 0 });
    expect(buckets.at(-2)?.level).toBeGreaterThan(0);
  });
});
