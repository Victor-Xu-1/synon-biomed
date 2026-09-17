import FlexFullContainer from '@/renderer/components/layout/FlexFullContainer';
import { Tooltip } from '@arco-design/web-react';
import classNames from 'classnames';
import React, { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { getSiderTooltipProps } from '@/renderer/utils/ui/siderTooltip';
import {
  preloadSettingsRoute,
  preloadSettingsRoutesDuringIdle,
  type SettingsRouteId,
} from '@/renderer/pages/settings/settingsRouteLoaders';
import {
  navigateSettingsRoute,
  readSettingsRoute,
  subscribeToSettingsRoute,
} from '@/renderer/pages/settings/settingsNavigation';
import { SettingsGeneratedNavIcon } from './SettingsGeneratedAsset';

export const BUILTIN_TAB_IDS = [
  'experts',
  'skills',
  'tools',
  'models',
  'compute',
  'governance',
  'network',
  'credentials',
  'storage',
  'general',
] as const;

const GROUP_HEADER_BEFORE: Record<string, string> = {
  experts: 'settings.groupAiCore',
  credentials: 'settings.groupWorkspace',
};

type SiderItem = {
  id: SettingsRouteId;
  label: string;
  icon: React.ReactElement;
  path: string;
};

const preloadRoute = (id: SettingsRouteId) => {
  void preloadSettingsRoute(id).catch((error) => {
    console.error(`Failed to preload settings module "${id}":`, error);
  });
};

const SettingsSider: React.FC<{ collapsed?: boolean; tooltipEnabled?: boolean }> = ({
  collapsed = false,
  tooltipEnabled = false,
}) => {
  const { t } = useTranslation();
  const [activeRoute, setActiveRoute] = React.useState<SettingsRouteId>(() => readSettingsRoute() ?? 'experts');

  React.useEffect(() => preloadSettingsRoutesDuringIdle(activeRoute), [activeRoute]);
  React.useEffect(() => subscribeToSettingsRoute(setActiveRoute), []);

  const { menus, groupHeaderAt } = useMemo(() => {
    const builtinMap: Record<(typeof BUILTIN_TAB_IDS)[number], SiderItem> = {
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

    const result = BUILTIN_TAB_IDS.map((id) => builtinMap[id]);
    const headerAt = new Map<number, string>();
    for (const [builtinId, headerKey] of Object.entries(GROUP_HEADER_BEFORE)) {
      const builtinIdx = result.findIndex((item) => item.id === builtinId);
      if (builtinIdx >= 0) {
        headerAt.set(builtinIdx, headerKey);
      }
    }

    return { menus: result, groupHeaderAt: headerAt };
  }, [t]);

  const siderTooltipProps = getSiderTooltipProps(tooltipEnabled);

  return (
    <div
      className={classNames('h-full settings-sider flex flex-col gap-1px overflow-y-auto overflow-x-hidden', {
        'settings-sider--collapsed': collapsed,
      })}
    >
      {menus.map((item, index) => {
        const isSelected = activeRoute === item.id;
        const groupHeaderKey = groupHeaderAt.get(index);
        const groupHeader =
          groupHeaderKey && !collapsed ? (
            <div className='settings-sider__group-header px-10px mt-10px h-26px flex items-center text-11px font-[600] text-t-tertiary select-none'>
              {t(groupHeaderKey)}
            </div>
          ) : null;

        return (
          <React.Fragment key={item.id}>
            {groupHeader}
            <Tooltip {...siderTooltipProps} content={item.label} position='right'>
              <a
                href={`#/settings/${item.path}`}
                data-settings-id={item.id}
                data-settings-path={item.path}
                aria-current={isSelected ? 'page' : undefined}
                className={classNames(
                  'settings-sider__item h-40px rd-8px flex items-center gap-8px group cursor-pointer relative overflow-hidden shrink-0 conversation-item transition-colors border-0 bg-transparent text-left',
                  collapsed ? 'w-full justify-center px-0' : 'justify-start px-10px',
                  {
                    'hover:bg-fill-3': !isSelected,
                    '!bg-fill-3': isSelected,
                  }
                )}
                onPointerEnter={() => preloadRoute(item.id)}
                onFocus={() => preloadRoute(item.id)}
                onClick={(event) => {
                  event.preventDefault();
                  preloadRoute(item.id);
                  navigateSettingsRoute(item.id);
                }}
              >
                <span className='settings-sider__item-icon size-18px flex items-center justify-center shrink-0 line-height-0'>
                  {item.icon}
                </span>
                <FlexFullContainer className='h-24px collapsed-hidden'>
                  <div className='settings-sider__item-label text-nowrap overflow-hidden inline-block w-full text-14px font-[500] lh-24px whitespace-nowrap text-t-primary'>
                    {item.label}
                  </div>
                </FlexFullContainer>
              </a>
            </Tooltip>
          </React.Fragment>
        );
      })}
    </div>
  );
};

export default SettingsSider;
