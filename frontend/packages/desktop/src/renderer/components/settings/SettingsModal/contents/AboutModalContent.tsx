import { Divider, Typography } from '@arco-design/web-react';
import React from 'react';
import { useTranslation } from 'react-i18next';
import classNames from 'classnames';
import { useSettingsViewMode } from '../settingsViewContext';

declare const __APP_VERSION__: string;

const AboutModalContent: React.FC = () => {
  const { t } = useTranslation();
  const isPageMode = useSettingsViewMode() === 'page';

  return (
    <div className='flex flex-col h-full w-full'>
      <div
        className={classNames(
          'flex-1 min-h-0 overflow-y-auto overflow-x-hidden px-24px',
          isPageMode && 'px-0 overflow-visible'
        )}
      >
        <div className='flex flex-col max-w-500px mx-auto'>
          <div className='flex flex-col items-center pb-24px'>
            <Typography.Title heading={3} className='text-24px font-bold text-t-primary mb-8px'>
              Synon Biomed
            </Typography.Title>
            <Typography.Text className='text-14px text-t-secondary mb-12px text-center'>
              {t('settings.appDescription')}
            </Typography.Text>
            <span className='px-10px py-4px rd-6px text-13px bg-fill-2 text-t-primary font-500'>
              v{__APP_VERSION__}
            </span>
          </div>
          <Divider className='my-16px' />
          <Typography.Text className='text-13px text-t-secondary text-center'>
            {t('settings.synonBiomedWorkbenchDescription')}
          </Typography.Text>
        </div>
      </div>
    </div>
  );
};

export default AboutModalContent;
