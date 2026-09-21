import classNames from 'classnames';
import React from 'react';
import { useLayoutContext } from '@/renderer/hooks/context/LayoutContext';
import {
  SettingsTabNavigateProvider,
  SettingsViewModeProvider,
} from '@/renderer/components/settings/SettingsModal/settingsViewContext';
import { User } from '@icon-park/react';
import { useTranslation } from 'react-i18next';
import { BUILTIN_TAB_IDS } from './SettingsSider';
import { navigateSettingsRoute, readSettingsRoute, subscribeToSettingsRoute } from '../settingsNavigation';
import type { SettingsRouteId } from '../settingsRouteLoaders';
import { getSettingsVisualContract, SETTINGS_VISUAL_SYSTEM_ID } from './settingsVisualContract';
import { SettingsGeneratedNavIcon } from './SettingsGeneratedAsset';

interface SettingsPageWrapperProps {
  children: React.ReactNode;
  className?: string;
  contentClassName?: string;
}

type NavItem = {
  label: string;
  icon: React.ReactElement;
  path: SettingsRouteId;
  id: SettingsRouteId;
};

type TranslateFn = (key: string) => string;

export function getBuiltinSettingsNavItems(_isDesktop: boolean, t: TranslateFn): NavItem[] {
  const builtinMap: Record<(typeof BUILTIN_TAB_IDS)[number], NavItem> = {
    experts: {
      id: 'experts',
      label: t('settings.synonBiomedExperts'),
      icon: <SettingsGeneratedNavIcon id='experts' />,
      path: 'experts',
    },
    skills: {
      id: 'skills',
      label: t('settings.skills'),
      icon: <SettingsGeneratedNavIcon id='skills' />,
      path: 'skills',
    },
    tools: {
      id: 'tools',
      label: t('settings.tools'),
      icon: <SettingsGeneratedNavIcon id='tools' />,
      path: 'tools',
    },
    models: {
      id: 'models',
      label: t('settings.models'),
      icon: <SettingsGeneratedNavIcon id='models' />,
      path: 'models',
    },
    compute: {
      id: 'compute',
      label: t('settings.compute'),
      icon: <SettingsGeneratedNavIcon id='compute' />,
      path: 'compute',
    },
    governance: {
      id: 'governance',
      label: t('settings.governance'),
      icon: <SettingsGeneratedNavIcon id='governance' />,
      path: 'governance',
    },
    network: {
      id: 'network',
      label: t('settings.network'),
      icon: <SettingsGeneratedNavIcon id='network' />,
      path: 'network',
    },
    environments: {
      id: 'environments',
      label: t('settings.environments.title'),
      icon: <SettingsGeneratedNavIcon id='environments' />,
      path: 'environments',
    },
    credentials: {
      id: 'credentials',
      label: t('settings.credentials'),
      icon: <SettingsGeneratedNavIcon id='credentials' />,
      path: 'credentials',
    },
    storage: {
      id: 'storage',
      label: t('settings.storage'),
      icon: <SettingsGeneratedNavIcon id='storage' />,
      path: 'storage',
    },
    general: {
      id: 'general',
      label: t('settings.general'),
      icon: <SettingsGeneratedNavIcon id='general' />,
      path: 'general',
    },
  };

  return BUILTIN_TAB_IDS.map((id) => builtinMap[id]);
}

const SettingsPageWrapper: React.FC<SettingsPageWrapperProps> = ({ children, className, contentClassName }) => {
  const layout = useLayoutContext();
  const isMobile = layout?.isMobile ?? false;
  const { t } = useTranslation();
  const [activeRoute, setActiveRoute] = React.useState<SettingsRouteId>(() => readSettingsRoute() ?? 'experts');
  const [contentRoute] = React.useState<SettingsRouteId>(() => readSettingsRoute() ?? 'experts');
  const menuItems = React.useMemo(() => getBuiltinSettingsNavItems(false, t), [t]);

  React.useEffect(() => subscribeToSettingsRoute(setActiveRoute), []);

  const containerClass = classNames(
    'settings-page-wrapper w-full min-h-full box-border overflow-y-auto',
    {
      'settings-page-wrapper--mobile': isMobile,
      'settings-page-wrapper--transitioning': activeRoute !== contentRoute,
    },
    className
  );

  const contentClass = classNames('settings-page-content w-full', contentClassName);
  const visualContract = getSettingsVisualContract(contentRoute);

  const navigateToTab = React.useCallback((tabId: string) => {
    navigateSettingsRoute(tabId as SettingsRouteId);
  }, []);

  return (
    <SettingsViewModeProvider value='page'>
      <SettingsTabNavigateProvider value={navigateToTab}>
        <div
          className={containerClass}
          data-settings-route={contentRoute}
          data-settings-module={contentRoute}
          data-settings-visual-system={SETTINGS_VISUAL_SYSTEM_ID}
          data-settings-reference-desktop={visualContract.reference.desktop}
          data-settings-reference-mobile={visualContract.reference.mobile}
        >
          {isMobile && (
            <div className='settings-mobile-nav-row'>
              <div className='settings-mobile-top-nav'>
                {menuItems.map((item) => {
                  const active = activeRoute === item.id;
                  return (
                    <a
                      key={item.path}
                      href={`#/settings/${item.path}`}
                      data-settings-id={item.id}
                      className={classNames('settings-mobile-top-nav__item', {
                        'settings-mobile-top-nav__item--active': active,
                      })}
                      onClick={(event) => {
                        event.preventDefault();
                        navigateSettingsRoute(item.id);
                      }}
                    >
                      <span className='settings-mobile-top-nav__icon'>{item.icon}</span>
                      <span className='settings-mobile-top-nav__label'>{item.label}</span>
                    </a>
                  );
                })}
              </div>
              <button
                type='button'
                className='settings-mobile-account-button'
                aria-label={t('settings.synonBiomedOpenAccount')}
                title={t('settings.synonBiomedAccountMenu')}
                onClick={() => layout?.setSiderCollapsed?.(false)}
              >
                <User theme='outline' size='17' />
              </button>
            </div>
          )}
          <div className={contentClass}>
            <div className='settings-page-body'>{children}</div>
          </div>
        </div>
      </SettingsTabNavigateProvider>
    </SettingsViewModeProvider>
  );
};

export default SettingsPageWrapper;
