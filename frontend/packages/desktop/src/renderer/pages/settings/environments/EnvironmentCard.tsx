import React from 'react';
import { useTranslation } from 'react-i18next';
import type { ScientificRuntimeOption } from '@/renderer/services/scientificRuntimeSettings';
import { scientificRuntimePresentation } from '@/renderer/utils/scientificRuntimePresentation';
import { SettingsGeneratedIcon } from '../components/SettingsGeneratedAsset';
import {
  activeEnvironmentStates,
  environmentCategory,
  environmentIcon,
  environmentStateKeys,
} from './environmentCatalog';

export function EnvironmentCard({
  item,
  disabled,
  onPrepare,
  onPause,
  onUninstall,
}: {
  item: ScientificRuntimeOption;
  disabled: boolean;
  onPrepare: (item: ScientificRuntimeOption) => void;
  onPause: (item: ScientificRuntimeOption) => void;
  onUninstall: (item: ScientificRuntimeOption) => void;
}) {
  const { t } = useTranslation();
  const text = scientificRuntimePresentation(item.id, t);
  const active = activeEnvironmentStates.has(item.status);
  const uninstalling = item.status === 'uninstalling';
  return (
    <article className='settings-entity-card environment-card' role='listitem' data-environment-id={item.id}>
      <div className='environment-card-body'>
        <div className='environment-card-heading'>
          <span className='environment-card-icon' aria-hidden='true'>
            <SettingsGeneratedIcon id={environmentIcon(item.id)} className='environment-card-icon-image' />
          </span>
          <div className='environment-card-title-group'>
            <h2>{text.title}</h2>
            <span className='environment-category'>
              {t('settings.environments.categories.' + environmentCategory(item.id))}
            </span>
            {item.required && <span className='environment-required'>{t('settings.environments.required')}</span>}
          </div>
        </div>
        <p className='environment-description'>{text.description}</p>
        <details className='environment-packages'>
          <summary>{t('settings.environments.packages', { count: item.packages?.length ?? 0 })}</summary>
          {item.packages?.length ? (
            <ul>
              {item.packages.map((pkg) => (
                <li key={pkg.manager + ':' + pkg.spec}>
                  <code>{pkg.spec}</code>
                  <span>{pkg.manager}</span>
                </li>
              ))}
            </ul>
          ) : (
            <p>{t('settings.environments.packagesUnavailable')}</p>
          )}
          {item.environment && (
            <p className='environment-identity'>
              {t('settings.environments.environmentId')}: <code>{item.environment}</code>
            </p>
          )}
        </details>
      </div>
      {item.status === 'preparing' && item.phasePercent != null && (
        <progress
          value={item.phasePercent}
          max={100}
          aria-label={t('settings.storageSettings.softwarePhaseProgress')}
        />
      )}
      {item.errorCode && (item.status === 'failed' || item.status === 'stopped') && (
        <p className='environment-error-code'>{t('settings.environments.failureHint')}</p>
      )}
      <div className='environment-card-footer'>
        <div className='environment-card-status'>
          <span className='environment-status' data-status={item.status} role='status'>
            {t('settings.storageSettings.' + (environmentStateKeys[item.status] ?? 'softwareUnknown'))}
          </span>
          <span className='environment-size'>
            {item.required
              ? t('settings.environments.included')
              : t('settings.storageSettings.estimatedSoftwareSize', { value: item.estimatedInstallMB })}
          </span>
        </div>
        <div className='environment-card-actions'>
          {!item.required && active && !uninstalling ? (
            <button
              type='button'
              className='settings-action-button environment-action environment-action--pause'
              disabled={disabled || !item.available}
              onClick={() => onPause(item)}
            >
              {t('settings.environments.pause')}
            </button>
          ) : null}
          {item.required ? (
            item.status === 'failed' || item.status === 'stopped' ? (
              <button
                type='button'
                className='settings-action-button environment-action'
                disabled={disabled || !item.available}
                onClick={() => onPrepare(item)}
              >
                {t('common.retry')}
              </button>
            ) : null
          ) : (
            <button
              type='button'
              className='settings-action-button environment-action'
              disabled={disabled || !item.available || active}
              onClick={() => {
                if (item.status === 'ready') onUninstall(item);
                else onPrepare(item);
              }}
            >
              {item.status === 'ready'
                ? t('settings.environments.uninstall')
                : uninstalling
                  ? t('settings.storageSettings.softwareUninstalling')
                  : active
                    ? t('settings.environments.preparing')
                    : t(
                        item.status === 'failed' || item.status === 'stopped'
                          ? 'common.retry'
                          : 'settings.environments.download'
                      )}
            </button>
          )}
        </div>
      </div>
    </article>
  );
}
