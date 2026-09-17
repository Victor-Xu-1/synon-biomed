import { Button } from '@arco-design/web-react';
import React from 'react';
import type { useTranslation } from 'react-i18next';
import {
  SettingsGeneratedEmptyArtwork,
  SettingsGeneratedIcon,
  type SettingsGeneratedIconId,
} from '../SettingsGeneratedAsset';

export const ComputeSection: React.FC<{
  title: string;
  description?: string;
  action?: React.ReactNode;
  children: React.ReactNode;
  icon?: SettingsGeneratedIconId;
}> = ({ title, description, action, children, icon }) => (
  <section className='settings-section compute-section'>
    <div className='settings-section__header compute-section__header'>
      <div className='settings-section__heading min-w-0'>
        {icon ? <SettingsGeneratedIcon id={icon} className='settings-section__icon' /> : null}
        <div className='min-w-0'>
          <h2 className='settings-section__title'>{title}</h2>
          {description ? <p className='settings-section__description'>{description}</p> : null}
        </div>
      </div>
      {action ? <div className='settings-section__actions'>{action}</div> : null}
    </div>
    <div className='settings-section__body compute-section__body'>{children}</div>
  </section>
);

export const WorkbenchProductRow: React.FC<{
  title: string;
  description: string;
  actionLabel: string;
  onAction: () => void;
}> = ({ title, description, actionLabel, onAction }) => (
  <div className='compute-product-row'>
    <div className='min-w-0'>
      <h3 className='m-0 text-14px font-600 text-t-primary'>{title}</h3>
      <p className='m-0 mt-3px text-12px leading-5 text-t-tertiary'>{description}</p>
    </div>
    <Button type='secondary' size='small' className='shrink-0' onClick={onAction}>
      {actionLabel}
    </Button>
  </div>
);

export const ComputeEmptyRow: React.FC<{ text: string }> = ({ text }) => (
  <div className='compute-empty-row'>
    <SettingsGeneratedEmptyArtwork id='compute' className='compute-empty-row__artwork' />
    <span>{text}</span>
  </div>
);

export const ComputeDetail: React.FC<{ label: string; value: string }> = ({ label, value }) => (
  <div className='min-w-0'>
    <div className='text-t-tertiary mb-2px'>{label}</div>
    <div className='text-t-secondary truncate' title={value}>
      {value}
    </div>
  </div>
);

export function computeStateLabel(state: string, t: ReturnType<typeof useTranslation>['t']): string {
  const labels: Record<string, string> = {
    running: t('settings.computeWorkspace.states.running'),
    starting: t('settings.computeWorkspace.states.starting'),
    queued: t('settings.computeWorkspace.states.queued'),
    pending: t('settings.computeWorkspace.states.pending'),
    live: t('settings.computeWorkspace.states.live'),
    ready: t('settings.computeWorkspace.states.ready'),
    stopped: t('settings.computeWorkspace.states.stopped'),
    completed: t('settings.computeWorkspace.states.completed'),
    succeeded: t('settings.computeWorkspace.states.completed'),
    failed: t('settings.computeWorkspace.states.failed'),
    cancelled: t('settings.computeWorkspace.states.cancelled'),
    canceled: t('settings.computeWorkspace.states.cancelled'),
  };
  return labels[state.toLowerCase()] ?? (state || t('settings.computeWorkspace.states.unknown'));
}

export const normalizeComputeTestId = (value: string): string => value.replace(/[:/\s<>"'|?*]/g, '-');
