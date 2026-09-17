export type AccountActivityDay = {
  date: string;
  count: number;
  tokenCount: number;
  isFuture: boolean;
};

export type AccountActivityMode = 'daily' | 'weekly' | 'cumulative';

export type AccountActivityBucket = {
  key: string;
  label: string;
  axisLabel: string;
  count: number;
  tokenCount: number;
  level: 0 | 1 | 2 | 3 | 4;
  isFuture: boolean;
};

export type AccountTopSkill = {
  name: string;
  invocationCount: number;
  lastUsedAt: string | null;
};

export type SynonBiomedAccountInsights = {
  metrics: {
    totalTasks: number;
    completedTasks: number;
    projectCount: number | null;
    artifactCount: number | null;
    currentStreak: number;
    longestStreak: number;
    completionRate: number | null;
    recentTaskCount: number;
    activeDayCount: number;
  };
  activityDays: AccountActivityDay[];
  topSkills: AccountTopSkill[];
  availability: {
    projects: boolean;
    skills: boolean;
    tokenUsage: boolean;
  };
  loadedAt: string;
};

export function aggregateAccountActivity(
  days: AccountActivityDay[],
  mode: AccountActivityMode,
  locale: string
): AccountActivityBucket[] {
  if (mode === 'daily') {
    return withActivityLevels(
      days.map((day) => ({
        key: day.date,
        label: formatBucketDate(day.date, locale, { dateStyle: 'medium' }),
        axisLabel: formatBucketDate(day.date, locale, { month: 'numeric', day: 'numeric' }),
        count: day.count,
        tokenCount: day.tokenCount,
        isFuture: day.isFuture,
      }))
    );
  }

  if (mode === 'weekly') {
    const weeks: Array<Omit<AccountActivityBucket, 'level'>> = [];
    for (let index = 0; index < days.length; index += 7) {
      const week = days.slice(index, index + 7);
      if (week.length === 0) continue;
      const first = week[0];
      const last = week[week.length - 1];
      weeks.push({
        key: `week-${first.date}`,
        label: `${formatBucketDate(first.date, locale, { month: 'short', day: 'numeric' })} – ${formatBucketDate(
          last.date,
          locale,
          { month: 'short', day: 'numeric' }
        )}`,
        axisLabel: formatBucketDate(first.date, locale, { month: 'numeric', day: 'numeric' }),
        count: week.reduce((total, day) => total + day.count, 0),
        tokenCount: week.reduce((total, day) => total + day.tokenCount, 0),
        isFuture: week.every((day) => day.isFuture),
      });
    }
    return withActivityLevels(weeks);
  }

  const months = new Map<string, Omit<AccountActivityBucket, 'level'>>();
  for (const day of days) {
    const monthKey = day.date.slice(0, 7);
    const existing = months.get(monthKey);
    if (existing) {
      existing.count += day.count;
      existing.tokenCount += day.tokenCount;
      existing.isFuture = existing.isFuture && day.isFuture;
      continue;
    }
    months.set(monthKey, {
      key: monthKey,
      label: formatBucketDate(`${monthKey}-01`, locale, { month: 'long', year: 'numeric' }),
      axisLabel: formatBucketDate(`${monthKey}-01`, locale, { month: 'short' }),
      count: day.count,
      tokenCount: day.tokenCount,
      isFuture: day.isFuture,
    });
  }
  return withActivityLevels(Array.from(months.values()).slice(-12));
}

function withActivityLevels(buckets: Array<Omit<AccountActivityBucket, 'level'>>): AccountActivityBucket[] {
  const max = Math.max(0, ...buckets.filter((bucket) => !bucket.isFuture).map((bucket) => bucket.tokenCount));
  return buckets.map((bucket) => ({
    ...bucket,
    level: activityLevel(bucket.tokenCount, max, bucket.isFuture),
  }));
}

function activityLevel(count: number, max: number, isFuture: boolean): 0 | 1 | 2 | 3 | 4 {
  if (isFuture || count <= 0 || max <= 0) return 0;
  const ratio = count / max;
  if (ratio <= 0.25) return 1;
  if (ratio <= 0.5) return 2;
  if (ratio <= 0.75) return 3;
  return 4;
}

function localDateFromKey(value: string): Date | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) return null;
  const date = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]));
  return Number.isFinite(date.getTime()) ? date : null;
}

function formatBucketDate(date: string, locale: string, options: Intl.DateTimeFormatOptions): string {
  const parsed = localDateFromKey(date);
  return parsed ? new Intl.DateTimeFormat(locale, options).format(parsed) : date;
}
