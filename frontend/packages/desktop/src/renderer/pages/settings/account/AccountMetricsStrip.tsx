import React from 'react';
import { useTranslation } from 'react-i18next';
import type { SynonBiomedAccountInsights } from '@/renderer/services/account/accountInsightsModel';
import { SettingsGeneratedIcon, type SettingsGeneratedIconId } from '../components/SettingsGeneratedAsset';
import './AccountMetricsStrip.css';

type AccountMetricsStripProps = {
  insights: SynonBiomedAccountInsights | null;
  loading: boolean;
};

const AccountMetricsStrip: React.FC<AccountMetricsStripProps> = ({ insights, loading }) => {
  const { t, i18n } = useTranslation();
  const formatNumber = (value: number | null | undefined) =>
    value === null || value === undefined
      ? '—'
      : new Intl.NumberFormat(i18n.language, {
          notation: value >= 10_000 ? 'compact' : 'standard',
        }).format(value);
  const metrics = [
    {
      value: insights?.metrics.totalTasks,
      label: t('settings.accountSettings.totalTasks'),
      iconId: 'connector-bars' as SettingsGeneratedIconId,
      tone: 'cyan',
    },
    {
      value: insights?.metrics.completedTasks,
      label: t('settings.accountSettings.completedTasks'),
      iconId: 'connector-clipboard' as SettingsGeneratedIconId,
      tone: 'blue',
    },
    {
      value: insights?.metrics.projectCount,
      label: t('settings.accountSettings.totalProjects'),
      iconId: 'experts' as SettingsGeneratedIconId,
      tone: 'violet',
    },
    {
      value: insights?.metrics.artifactCount,
      label: t('settings.accountSettings.totalArtifacts'),
      iconId: 'storage' as SettingsGeneratedIconId,
      tone: 'teal',
    },
    {
      value: insights?.metrics.currentStreak,
      label: t('settings.accountSettings.currentStreak'),
      suffix: insights && insights.metrics.currentStreak > 0 ? t('settings.accountSettings.dayUnit') : undefined,
      iconId: 'skills' as SettingsGeneratedIconId,
      tone: 'amber',
    },
  ];

  return (
    <section className='account-footprint' aria-labelledby='account-footprint-title'>
      <h2 id='account-footprint-title'>{t('settings.accountSettings.researchFootprint')}</h2>
      <div
        className='account-metrics'
        aria-label={t('settings.accountSettings.metricsTitle')}
        aria-busy={loading}
        data-testid='account-metrics-strip'
      >
        {metrics.map((metric) => (
          <div key={metric.label} className='account-metric'>
            <span className={`account-metric__icon account-metric__icon--${metric.tone}`}>
              <SettingsGeneratedIcon id={metric.iconId} className='account-metric__icon-image' />
            </span>
            <div className='account-metric__content'>
              <div
                className={loading ? 'account-metric__value account-metric__value--loading' : 'account-metric__value'}
              >
                {loading ? '—' : formatNumber(metric.value)}
                {!loading && metric.suffix ? <span>{metric.suffix}</span> : null}
              </div>
              <div className='account-metric__label'>{metric.label}</div>
            </div>
          </div>
        ))}
      </div>
    </section>
  );
};

export default AccountMetricsStrip;
