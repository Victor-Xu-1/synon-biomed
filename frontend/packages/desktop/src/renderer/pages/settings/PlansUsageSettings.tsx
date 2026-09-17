import { Tag } from '@arco-design/web-react';
import React from 'react';
import { useTranslation } from 'react-i18next';
import { getSynonBiomedBillingOverview } from '@/renderer/services/synonBiomedBilling';
import SettingsPageHeader from './components/SettingsPageHeader';
import SettingsPageWrapper from './components/SettingsPageWrapper';
import { SettingsGeneratedIcon } from './components/SettingsGeneratedAsset';
import { SettingsSection } from './components/SettingsPrimitives';

const formatNumber = (value: number) => new Intl.NumberFormat().format(value);

function resolveRateIcon(rateId: string): 'connector-dna' | 'connector-clipboard' | 'connector-cube' {
  if (rateId === 'light') return 'connector-dna';
  if (rateId === 'standard') return 'connector-clipboard';
  return 'connector-cube';
}

const PlansUsageSettings: React.FC = () => {
  const { t } = useTranslation();
  const overview = getSynonBiomedBillingOverview();
  const usagePercent = overview.credits.initial === 0 ? 0 : (overview.credits.used / overview.credits.initial) * 100;

  return (
    <SettingsPageWrapper>
      <div className='settings-plans-page flex flex-col gap-24px pb-24px' data-testid='synon-plans-usage-settings'>
        <SettingsPageHeader
          title={t('settings.plansUsageSettings.title')}
          description={t('settings.plansUsageSettings.description')}
        />

        <SettingsSection
          className='plans-usage-section plans-usage-section--balance'
          title={t('settings.plansUsageSettings.usageTitle')}
          description={t('settings.plansUsageSettings.usageDescription')}
          icon='plans'
        >
          <div className='plans-usage-balance-content py-14px flex flex-col gap-16px'>
            <div className='plans-usage-plan-identity flex items-center justify-between gap-16px flex-wrap'>
              <div className='flex items-center gap-11px min-w-0'>
                <span className='plans-usage-plan-identity__icon size-40px flex items-center justify-center shrink-0 rd-10px overflow-hidden'>
                  <SettingsGeneratedIcon id='plans' className='settings-list-generated-icon' />
                </span>
                <div className='min-w-0'>
                  <div className='text-15px font-600 text-t-primary'>
                    {t(`settings.plansUsageSettings.plans.${overview.plan}`)}
                  </div>
                  <div className='mt-3px text-11px text-t-tertiary'>
                    {t('settings.plansUsageSettings.localPlanDescription')}
                  </div>
                </div>
              </div>
              <Tag color='arcoblue'>{t('settings.plansUsageSettings.draftTag')}</Tag>
            </div>

            <div className='plans-usage-stat-grid grid grid-cols-1 md:grid-cols-3 gap-10px'>
              {[
                ['remaining', t('settings.plansUsageSettings.remainingCredits')],
                ['used', t('settings.plansUsageSettings.usedCredits')],
                ['initial', t('settings.plansUsageSettings.initialCredits')],
              ].map(([key, label]) => (
                <div key={key} className='rd-10px bg-fill-1 px-14px py-12px'>
                  <div className='text-11px text-t-tertiary'>{label}</div>
                  <div className='mt-5px text-20px font-600 text-t-primary'>
                    {formatNumber(overview.credits[key as keyof typeof overview.credits])}
                  </div>
                  <div className='mt-3px text-11px text-t-tertiary'>{t('settings.plansUsageSettings.creditUnit')}</div>
                </div>
              ))}
            </div>

            <div className='plans-usage-progress rd-10px bg-fill-1 px-14px py-13px'>
              <div className='flex items-center justify-between gap-12px text-12px'>
                <span className='text-t-secondary'>{t('settings.plansUsageSettings.creditUsage')}</span>
                <span className='font-600 text-t-primary'>
                  {formatNumber(overview.credits.used)} / {formatNumber(overview.credits.initial)}
                </span>
              </div>
              <div
                className='mt-10px h-6px rd-full bg-fill-3 overflow-hidden'
                aria-label={t('settings.plansUsageSettings.creditUsage')}
                role='progressbar'
                aria-valuemin={0}
                aria-valuemax={overview.credits.initial}
                aria-valuenow={overview.credits.used}
              >
                <div
                  className='h-full rd-full bg-primary'
                  style={{
                    width: Math.min(100, Math.max(0, usagePercent)) + '%',
                  }}
                />
              </div>
              <p className='mt-10px mb-0 text-11px leading-5 text-t-tertiary'>
                {t('settings.plansUsageSettings.draftExplanation')}
              </p>
            </div>
          </div>
        </SettingsSection>

        <SettingsSection
          className='plans-usage-section plans-usage-section--rates'
          title={t('settings.plansUsageSettings.modelRatesTitle')}
          description={t('settings.plansUsageSettings.modelRatesDescription')}
          icon='models'
        >
          <div className='plans-usage-rates-grid py-14px grid grid-cols-1 md:grid-cols-3 gap-10px'>
            {overview.modelRates.map((rate) => (
              <article
                key={rate.id}
                className='plans-usage-rate-card rd-10px bg-fill-1 px-14px py-13px'
                data-testid={'synon-model-rate-' + rate.id}
              >
                <div className='plans-usage-rate-card__heading flex items-center justify-between gap-8px'>
                  <div className='flex min-w-0 items-center gap-12px'>
                    <SettingsGeneratedIcon id={resolveRateIcon(rate.id)} className='plans-usage-rate-card__icon' />
                    <h3 className='m-0 text-13px font-600 text-t-primary'>
                      {t('settings.plansUsageSettings.modelRates.' + rate.id + '.label')}
                    </h3>
                  </div>
                  <Tag>{rate.multiplier.toFixed(1)}×</Tag>
                </div>
                <div className='mt-12px flex flex-col gap-7px text-11px'>
                  <div className='flex items-center justify-between gap-8px'>
                    <span className='text-t-tertiary'>{t('settings.plansUsageSettings.tokensPerCredit')}</span>
                    <span className='font-600 text-t-secondary'>{formatNumber(rate.tokensPerCredit)}</span>
                  </div>
                  <div className='flex items-center justify-between gap-8px'>
                    <span className='text-t-tertiary'>{t('settings.plansUsageSettings.initialCapacity')}</span>
                    <span className='font-600 text-t-secondary'>{formatNumber(rate.capacityTokens)}</span>
                  </div>
                </div>
                <p className='mt-10px mb-0 text-11px leading-5 text-t-tertiary'>
                  {t('settings.plansUsageSettings.modelRates.' + rate.id + '.description')}
                </p>
              </article>
            ))}
          </div>
          <div className='plans-usage-formula mx-14px mb-14px rd-8px bg-fill-1 px-12px py-10px text-11px leading-5 text-t-secondary'>
            <div className='font-600 text-t-primary'>{t('settings.plansUsageSettings.formulaTitle')}</div>
            <div className='mt-3px'>{t('settings.plansUsageSettings.formula')}</div>
          </div>
        </SettingsSection>

        <SettingsSection
          className='plans-usage-section plans-usage-section--billing'
          title={t('settings.plansUsageSettings.billingTitle')}
          description={t('settings.plansUsageSettings.billingDescription')}
          icon='plans'
        >
          <div className='plans-usage-billing-grid py-14px grid grid-cols-1 md:grid-cols-2 gap-12px'>
            <article className='min-h-150px rd-10px bg-fill-1 px-14px py-13px' aria-labelledby='billing-bills-title'>
              <h3 id='billing-bills-title' className='m-0 text-13px font-600 text-t-primary'>
                {t('settings.plansUsageSettings.billsTitle')}
              </h3>
              {overview.bills.length === 0 ? (
                <div className='min-h-112px flex flex-col items-center justify-center text-center'>
                  <SettingsGeneratedIcon id='connector-book' className='settings-empty-artwork-icon' />
                  <div className='mt-8px text-12px font-500 text-t-secondary'>
                    {t('settings.plansUsageSettings.billsEmpty')}
                  </div>
                  <div className='mt-3px text-11px text-t-tertiary'>
                    {t('settings.plansUsageSettings.billsEmptyDescription')}
                  </div>
                </div>
              ) : null}
            </article>

            <article className='min-h-150px rd-10px bg-fill-1 px-14px py-13px' aria-labelledby='billing-invoices-title'>
              <h3 id='billing-invoices-title' className='m-0 text-13px font-600 text-t-primary'>
                {t('settings.plansUsageSettings.invoicesTitle')}
              </h3>
              {overview.invoices.length === 0 ? (
                <div className='min-h-112px flex flex-col items-center justify-center text-center'>
                  <SettingsGeneratedIcon id='connector-book' className='settings-empty-artwork-icon' />
                  <div className='mt-8px text-12px font-500 text-t-secondary'>
                    {t('settings.plansUsageSettings.invoicesEmpty')}
                  </div>
                  <div className='mt-3px text-11px text-t-tertiary'>
                    {t('settings.plansUsageSettings.invoicesEmptyDescription')}
                  </div>
                </div>
              ) : null}
            </article>
          </div>
        </SettingsSection>
      </div>
    </SettingsPageWrapper>
  );
};

export default PlansUsageSettings;
