import { Refresh } from '@icon-park/react';
import classNames from 'classnames';
import React from 'react';
import { useTranslation } from 'react-i18next';
import { SettingsGeneratedIcon, type SettingsGeneratedIconId } from './SettingsGeneratedAsset';

export const SettingsSection: React.FC<{
  title: string;
  description?: string;
  actions?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
  bodyClassName?: string;
  icon?: SettingsGeneratedIconId;
}> = ({ title, description, actions, children, className, bodyClassName, icon }) => (
  <section className={classNames('settings-section', className)}>
    <div className='settings-section__header'>
      <div className='settings-section__heading min-w-0'>
        {icon ? <SettingsGeneratedIcon id={icon} className='settings-section__icon' /> : null}
        <div className='min-w-0'>
          <h2 className='settings-section__title'>{title}</h2>
          {description ? <p className='settings-section__description'>{description}</p> : null}
        </div>
      </div>
      {actions ? <div className='settings-section__actions'>{actions}</div> : null}
    </div>
    <div className={classNames('settings-section__body', bodyClassName)}>{children}</div>
  </section>
);

export const RefreshButton: React.FC<{ loading: boolean; onClick: () => void | Promise<void> }> = ({
  loading,
  onClick,
}) => {
  const { t } = useTranslation();
  return (
    <button type='button' className='settings-action-button' onClick={() => void onClick()} disabled={loading}>
      <Refresh size='14' className={loading ? 'animate-spin' : undefined} />
      {t('common.refresh')}
    </button>
  );
};

export const EmptyText: React.FC<React.PropsWithChildren> = ({ children }) => (
  <div className='settings-empty-state'>{children}</div>
);

export const SettingsToolbar: React.FC<React.PropsWithChildren<{ className?: string }>> = ({ children, className }) => (
  <div className={classNames('settings-toolbar', className)}>{children}</div>
);
