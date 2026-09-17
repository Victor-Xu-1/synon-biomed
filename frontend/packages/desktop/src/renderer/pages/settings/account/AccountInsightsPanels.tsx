import { Refresh } from '@icon-park/react';
import React from 'react';
import { useTranslation } from 'react-i18next';
import type { SynonBiomedAccountInsights } from '@/renderer/services/account/accountInsightsModel';
import { SettingsGeneratedIcon } from '../components/SettingsGeneratedAsset';
import './AccountInsightsPanels.css';

type AccountInsightsPanelsProps = {
  insights: SynonBiomedAccountInsights;
  onRetry: () => void;
};

const AccountInsightsPanels: React.FC<AccountInsightsPanelsProps> = ({ insights, onRetry }) => {
  const { t, i18n } = useTranslation();
  const completionRate = insights.metrics.completionRate ?? 0;
  const percent = (value: number | null) =>
    value === null
      ? '—'
      : new Intl.NumberFormat(i18n.language, { style: 'percent', maximumFractionDigits: 0 }).format(value);
  const formatNumber = (value: number) => new Intl.NumberFormat(i18n.language).format(value);
  const insightRows = [
    [t('settings.accountSettings.recentTasks'), formatNumber(insights.metrics.recentTaskCount)],
    [
      t('settings.accountSettings.longestStreak'),
      t('settings.accountSettings.daysValue', { count: insights.metrics.longestStreak }),
    ],
    [t('settings.accountSettings.activeDays'), formatNumber(insights.metrics.activeDayCount)],
  ];
  const hasPartialData = !insights.availability.projects || !insights.availability.skills;

  return (
    <>
      {hasPartialData ? (
        <div className='account-partial-notice' role='status'>
          <span>{t('settings.accountSettings.partialData')}</span>
          <button type='button' onClick={onRetry}>
            <Refresh theme='outline' size={13} />
            {t('common.retry')}
          </button>
        </div>
      ) : null}
      <div className='account-insight-grid'>
        <section className='account-insight-panel' aria-labelledby='account-insights-title'>
          <div className='account-insight-panel__title'>
            <span className='account-insight-panel__icon'>
              <SettingsGeneratedIcon id='connector-bars' className='account-insight-panel__icon-image' />
            </span>
            <div>
              <h2 id='account-insights-title'>{t('settings.accountSettings.insightsTitle')}</h2>
              <p>{t('settings.accountSettings.insightsDescription')}</p>
            </div>
          </div>
          <div className='account-completion'>
            <div>
              <span>{t('settings.accountSettings.completionRate')}</span>
              <strong>{percent(insights.metrics.completionRate)}</strong>
            </div>
            <progress value={completionRate} max={1} aria-label={t('settings.accountSettings.completionRate')} />
          </div>
          <dl className='account-insight-list'>
            {insightRows.map(([label, value]) => (
              <div key={label}>
                <dt>{label}</dt>
                <dd>{value}</dd>
              </div>
            ))}
          </dl>
        </section>

        <section className='account-insight-panel' aria-labelledby='account-skills-title'>
          <div className='account-insight-panel__title'>
            <span className='account-insight-panel__icon account-insight-panel__icon--skills'>
              <SettingsGeneratedIcon id='skills' className='account-insight-panel__icon-image' />
            </span>
            <div>
              <h2 id='account-skills-title'>{t('settings.accountSettings.topSkillsTitle')}</h2>
              <p>{t('settings.accountSettings.topSkillsDescription')}</p>
            </div>
          </div>
          {!insights.availability.skills ? (
            <p className='account-insight-empty'>{t('settings.accountSettings.skillUsageUnavailable')}</p>
          ) : insights.topSkills.length === 0 ? (
            <p className='account-insight-empty'>{t('settings.accountSettings.noSkillUsage')}</p>
          ) : (
            <ol className='account-skill-list'>
              {insights.topSkills.map((skill, index) => (
                <li key={skill.name}>
                  <span className='account-skill-list__rank'>{index + 1}</span>
                  <span className='account-skill-list__name' title={skill.name}>
                    {skill.name}
                  </span>
                  <span className='account-skill-list__count'>
                    {t('settings.accountSettings.skillRuns', { count: skill.invocationCount })}
                  </span>
                </li>
              ))}
            </ol>
          )}
        </section>
      </div>
    </>
  );
};

export default AccountInsightsPanels;
