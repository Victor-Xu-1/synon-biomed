import React, { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  aggregateAccountActivity,
  type AccountActivityBucket,
  type AccountActivityMode,
  type SynonBiomedAccountInsights,
} from '@/renderer/services/account/accountInsightsModel';
import './AccountActivityChart.css';

type AccountActivityHeatmapProps = {
  insights: SynonBiomedAccountInsights;
};

const MODES: AccountActivityMode[] = ['daily', 'weekly', 'cumulative'];
const CHART_WIDTH = 960;
const CHART_HEIGHT = 196;
const CHART_TOP = 22;
const CHART_BOTTOM = 166;
const CHART_LEFT = 64;
const CHART_RIGHT = 948;

const AccountActivityHeatmap: React.FC<AccountActivityHeatmapProps> = ({ insights }) => {
  const { t, i18n } = useTranslation();
  const [mode, setMode] = useState<AccountActivityMode>('daily');
  const [activePointKey, setActivePointKey] = useState<string | null>(null);
  const buckets = useMemo(() => {
    const allBuckets = aggregateAccountActivity(insights.activityDays, mode, i18n.language).filter(
      (bucket) => !bucket.isFuture
    );
    const limit = mode === 'daily' ? 30 : mode === 'weekly' ? 26 : 12;
    return allBuckets.slice(-limit);
  }, [i18n.language, insights.activityDays, mode]);
  const chart = useMemo(() => buildActivityChart(buckets, i18n.language), [buckets, i18n.language]);
  const activePoint = chart.points.find((point) => point.bucket.key === activePointKey) ?? null;

  return (
    <section className='account-activity' aria-labelledby='account-activity-title'>
      <div className='account-section-heading'>
        <div>
          <h2 id='account-activity-title'>{t('settings.accountSettings.activityTitle')}</h2>
          <p>{t('settings.accountSettings.activityDescription')}</p>
        </div>
        <div className='account-activity-tabs' role='tablist' aria-label={t('settings.accountSettings.activityRange')}>
          {MODES.map((candidate) => (
            <button
              key={candidate}
              type='button'
              role='tab'
              aria-selected={mode === candidate}
              onClick={() => {
                setMode(candidate);
                setActivePointKey(null);
              }}
            >
              {t(`settings.accountSettings.activityModes.${candidate}`)}
            </button>
          ))}
        </div>
      </div>

      <figure className='account-activity-chart' data-mode={mode}>
        {!insights.availability.tokenUsage ? (
          <div className='account-activity-chart__unavailable' role='status'>
            {t('settings.accountSettings.tokenUsageUnavailable')}
          </div>
        ) : (
          <div className='account-activity-chart__plot'>
            <div className='account-activity-chart__y-axis' aria-hidden='true'>
              <small>{t('settings.accountSettings.tokenUnit')}</small>
              {chart.yTicks.map((tick) => (
                <span key={`${tick.value}-${tick.y}`} style={{ top: `${(tick.y / CHART_HEIGHT) * 100}%` }}>
                  {tick.label}
                </span>
              ))}
            </div>
            <svg
              viewBox={`0 0 ${CHART_WIDTH} ${CHART_HEIGHT}`}
              role='img'
              aria-label={t('settings.accountSettings.activityChartLabel', { tokens: chart.formattedTotalTokens })}
              preserveAspectRatio='none'
            >
              {chart.yTicks.slice(0, -1).map((tick) => (
                <line
                  key={`grid-${tick.y}`}
                  className='account-activity-chart__grid'
                  x1={CHART_LEFT}
                  y1={tick.y}
                  x2={CHART_RIGHT}
                  y2={tick.y}
                />
              ))}
              <line
                className='account-activity-chart__baseline'
                x1={CHART_LEFT}
                y1={CHART_BOTTOM}
                x2={CHART_RIGHT}
                y2={CHART_BOTTOM}
              />
              <path className='account-activity-chart__line' d={chart.curvePath} />
              {chart.points.map((point) => {
                const label = t('settings.accountSettings.activityCell', {
                  label: point.bucket.label,
                  tokens: formatTokenCount(point.bucket.tokenCount, i18n.language),
                });
                const active = point.bucket.key === activePointKey;
                return (
                  <g key={point.bucket.key}>
                    {active ? (
                      <line
                        className='account-activity-chart__hover-guide'
                        x1={point.x}
                        y1={CHART_TOP}
                        x2={point.x}
                        y2={CHART_BOTTOM}
                      />
                    ) : null}
                    <rect
                      className='account-activity-chart__point'
                      data-activity-key={point.bucket.key}
                      x={point.x - chart.hitWidth / 2}
                      y={CHART_TOP}
                      width={chart.hitWidth}
                      height={CHART_BOTTOM - CHART_TOP}
                      tabIndex={0}
                      role='button'
                      aria-label={label}
                      onMouseEnter={() => setActivePointKey(point.bucket.key)}
                      onMouseLeave={() =>
                        setActivePointKey((current) => (current === point.bucket.key ? null : current))
                      }
                      onFocus={() => setActivePointKey(point.bucket.key)}
                      onBlur={() => setActivePointKey((current) => (current === point.bucket.key ? null : current))}
                      onClick={() => setActivePointKey(point.bucket.key)}
                    >
                      <title>{label}</title>
                    </rect>
                  </g>
                );
              })}
            </svg>
            {chart.points.map((point) => {
              const active = point.bucket.key === activePointKey;
              return point.bucket.tokenCount > 0 || active ? (
                <span
                  key={`marker-${point.bucket.key}`}
                  className={`account-activity-chart__marker${active ? ' is-active' : ''}`}
                  aria-hidden='true'
                  style={{
                    left: `${(point.x / CHART_WIDTH) * 100}%`,
                    top: `${(point.y / CHART_HEIGHT) * 100}%`,
                  }}
                />
              ) : null;
            })}
            {activePoint ? (
              <div
                className={`account-activity-chart__tooltip${activePoint.y < 70 ? ' is-below' : ''}`}
                role='tooltip'
                style={{
                  left: `${Math.min(92, Math.max(8, (activePoint.x / CHART_WIDTH) * 100))}%`,
                  top: `${(activePoint.y / CHART_HEIGHT) * 100}%`,
                }}
              >
                <strong>{activePoint.bucket.label}</strong>
                <span>
                  {formatTokenCount(activePoint.bucket.tokenCount, i18n.language)}{' '}
                  {t('settings.accountSettings.tokenUnit')}
                </span>
              </div>
            ) : null}
          </div>
        )}
        {insights.availability.tokenUsage ? (
          <figcaption className='account-activity-chart__labels' aria-hidden='true'>
            {chart.labels.map((label) => (
              <span key={label.key} style={{ left: `${label.position}%` }}>
                {label.text}
              </span>
            ))}
          </figcaption>
        ) : null}
      </figure>
    </section>
  );
};

function buildActivityChart(buckets: AccountActivityBucket[], locale: string) {
  const max = Math.max(0, ...buckets.map((bucket) => bucket.tokenCount));
  const scaleMax = Math.max(1, max);
  const plotWidth = CHART_RIGHT - CHART_LEFT;
  const spacing = buckets.length > 1 ? plotWidth / (buckets.length - 1) : plotWidth;
  const points = buckets.map((bucket, index) => ({
    bucket,
    x: buckets.length === 1 ? (CHART_LEFT + CHART_RIGHT) / 2 : CHART_LEFT + index * spacing,
    y: CHART_BOTTOM - (bucket.tokenCount / scaleMax) * (CHART_BOTTOM - CHART_TOP),
  }));
  const labelStep = Math.max(1, Math.ceil(buckets.length / 7));
  const yTicks = max > 0 ? [max, Math.round(max / 2), 0] : [0];
  return {
    formattedTotalTokens: formatTokenCount(
      buckets.reduce((sum, bucket) => sum + bucket.tokenCount, 0),
      locale
    ),
    points,
    curvePath: buildSmoothCurvePath(points),
    hitWidth: Math.max(18, Math.min(32, spacing * 0.86)),
    yTicks: yTicks.map((value) => ({
      value,
      label: formatTokenCount(value, locale),
      y: CHART_BOTTOM - (value / scaleMax) * (CHART_BOTTOM - CHART_TOP),
    })),
    labels: buckets
      .map((bucket, index) => ({ bucket, index }))
      .filter(({ index }) => index % labelStep === 0 || index === buckets.length - 1)
      .map(({ bucket, index }) => ({
        key: bucket.key,
        text: bucket.axisLabel,
        position: buckets.length <= 1 ? 50 : ((CHART_LEFT + index * spacing) / CHART_WIDTH) * 100,
      })),
  };
}

function buildSmoothCurvePath(points: Array<{ x: number; y: number }>): string {
  if (points.length === 0) return '';
  if (points.length === 1) return `M ${points[0].x},${points[0].y}`;

  const intervals = points.slice(1).map((point, index) => point.x - points[index].x);
  const slopes = points.slice(1).map((point, index) => (point.y - points[index].y) / intervals[index]);
  const tangents = points.map((_, index) => {
    if (index === 0) return slopes[0];
    if (index === points.length - 1) return slopes[slopes.length - 1];

    const previousSlope = slopes[index - 1];
    const nextSlope = slopes[index];
    if (previousSlope === 0 || nextSlope === 0 || Math.sign(previousSlope) !== Math.sign(nextSlope)) return 0;

    const previousInterval = intervals[index - 1];
    const nextInterval = intervals[index];
    const previousWeight = 2 * nextInterval + previousInterval;
    const nextWeight = nextInterval + 2 * previousInterval;
    return (previousWeight + nextWeight) / (previousWeight / previousSlope + nextWeight / nextSlope);
  });

  return points.slice(1).reduce((path, point, index) => {
    const previous = points[index];
    const interval = intervals[index];
    const controlOffset = interval / 3;
    const firstControlX = previous.x + controlOffset;
    const firstControlY = previous.y + tangents[index] * controlOffset;
    const secondControlX = point.x - controlOffset;
    const secondControlY = point.y - tangents[index + 1] * controlOffset;
    return `${path} C ${firstControlX},${firstControlY} ${secondControlX},${secondControlY} ${point.x},${point.y}`;
  }, `M ${points[0].x},${points[0].y}`);
}

function formatTokenCount(value: number, locale: string): string {
  return new Intl.NumberFormat(locale, {
    notation: 'compact',
    maximumFractionDigits: 1,
  }).format(value);
}

export default AccountActivityHeatmap;
