import SynonModal from '@/renderer/components/base/SynonModal';
import SynonScrollArea from '@/renderer/components/base/SynonScrollArea';
import SynonBiomedAvatar from '@/renderer/components/synonBiomed/SynonBiomedAvatar';
import { iconColors } from '@/renderer/styles/colors';
import { Tabs } from '@arco-design/web-react';
import { Brain, Computer, Info, Lightning, Shield, System, Toolkit } from '@icon-park/react';
import classNames from 'classnames';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import AboutModalContent from './contents/AboutModalContent';
import SystemModalContent from './contents/SystemModalContent';
import ToolsModalContent from './contents/ToolsModalContent';
import ExpertsSettings from '@/renderer/pages/settings/SynonBiomedExpertsSettings';
import { ComputeSettingsContent } from '@/renderer/pages/settings/ComputeSettings';
import { GovernanceSettingsContent } from '@/renderer/pages/settings/GovernanceSettings';
import { SynonBiomedModelsSettingsContent } from '@/renderer/pages/settings/SynonBiomedModelsSettings';
import SynonBiomedSkillsSettings from '@/renderer/pages/settings/SynonBiomedSkillsSettings';
import { SettingsTabNavigateProvider, SettingsViewModeProvider } from './settingsViewContext';

const MOBILE_BREAKPOINT = 768;
const SIDEBAR_WIDTH = 200;
const MODAL_WIDTH = {
  mobile: 560,
  desktop: 880,
} as const;
const MODAL_HEIGHT = {
  mobile: '90vh',
  mobileContent: 'calc(90vh - 80px)',
  desktop: 459,
} as const;
const RESIZE_DEBOUNCE_DELAY = 150;

export type BuiltinSettingTab =
  | 'experts'
  | 'skills'
  | 'tools'
  | 'models'
  | 'compute'
  | 'governance'
  | 'system'
  | 'about';
export type SettingTab = BuiltinSettingTab | (string & {});

interface SettingsModalProps {
  visible: boolean;
  onCancel: () => void;
  defaultTab?: SettingTab;
}

interface SubModalProps {
  visible: boolean;
  onCancel: () => void;
  title?: string;
  children: React.ReactNode;
}

type MenuItem = { key: BuiltinSettingTab; label: string; icon: React.ReactNode };

const normalizeSettingTab = (tab?: SettingTab): BuiltinSettingTab => {
  switch (tab) {
    case 'experts':
    case 'skills':
    case 'tools':
    case 'models':
    case 'compute':
    case 'governance':
    case 'system':
    case 'about':
      return tab as BuiltinSettingTab;
    default:
      return 'experts';
  }
};

export const SubModal: React.FC<SubModalProps> = ({ visible, onCancel, title, children }) => {
  return (
    <SynonModal
      visible={visible}
      onCancel={onCancel}
      footer={null}
      className='settings-sub-modal'
      size='medium'
      title={title}
    >
      <SynonScrollArea className='h-full px-20px pb-16px text-14px text-t-primary'>{children}</SynonScrollArea>
    </SynonModal>
  );
};

const SettingsModal: React.FC<SettingsModalProps> = ({ visible, onCancel, defaultTab = 'experts' }) => {
  const { t } = useTranslation();
  const [activeTab, setActiveTab] = useState<BuiltinSettingTab>(() => normalizeSettingTab(defaultTab));
  const [isMobile, setIsMobile] = useState(false);
  const resizeTimerRef = useRef<number | undefined>(undefined);

  const handleResize = useCallback(() => {
    setIsMobile(window.innerWidth < MOBILE_BREAKPOINT);
  }, []);

  useEffect(() => {
    if (visible) {
      setActiveTab(normalizeSettingTab(defaultTab));
    }
  }, [defaultTab, visible]);

  useEffect(() => {
    handleResize();

    const debouncedResize = () => {
      if (resizeTimerRef.current) {
        window.clearTimeout(resizeTimerRef.current);
      }
      resizeTimerRef.current = window.setTimeout(handleResize, RESIZE_DEBOUNCE_DELAY);
    };

    window.addEventListener('resize', debouncedResize);
    return () => {
      window.removeEventListener('resize', debouncedResize);
      if (resizeTimerRef.current) {
        window.clearTimeout(resizeTimerRef.current);
      }
    };
  }, [handleResize]);

  const menuItems = useMemo<MenuItem[]>(() => {
    const items: MenuItem[] = [
      {
        key: 'experts',
        label: t('settings.synonBiomedExperts'),
        icon: <SynonBiomedAvatar size={20} />,
      },
      {
        key: 'skills',
        label: t('settings.skills'),
        icon: <Lightning theme='outline' size='20' fill={iconColors.secondary} />,
      },
      {
        key: 'tools',
        label: t('settings.tools'),
        icon: <Toolkit theme='outline' size='20' fill={iconColors.secondary} />,
      },
      {
        key: 'models',
        label: t('settings.models'),
        icon: <Brain theme='outline' size='20' fill={iconColors.secondary} />,
      },
      {
        key: 'compute',
        label: t('settings.compute'),
        icon: <System theme='outline' size='20' fill={iconColors.secondary} />,
      },
      {
        key: 'governance',
        label: t('settings.governance'),
        icon: <Shield theme='outline' size='20' fill={iconColors.secondary} />,
      },
    ];

    items.push(
      {
        key: 'system',
        label: t('settings.system'),
        icon: <Computer theme='outline' size='20' fill={iconColors.secondary} />,
      },
      { key: 'about', label: t('settings.about'), icon: <Info theme='outline' size='20' fill={iconColors.secondary} /> }
    );

    return items;
  }, [t]);

  const renderContent = () => {
    switch (activeTab) {
      case 'experts':
        return <ExpertsSettings />;
      case 'skills':
        return <SynonBiomedSkillsSettings withWrapper={false} />;
      case 'tools':
        return <ToolsModalContent />;
      case 'models':
        return <SynonBiomedModelsSettingsContent />;
      case 'compute':
        return <ComputeSettingsContent />;
      case 'governance':
        return <GovernanceSettingsContent />;
      case 'system':
        return <SystemModalContent />;
      case 'about':
        return <AboutModalContent />;
      default:
        return null;
    }
  };

  const handleTabChange = useCallback((tab: string) => {
    setActiveTab(normalizeSettingTab(tab));
  }, []);

  const mobileMenu = (
    <div className='mt-16px mb-20px overflow-x-auto'>
      <Tabs
        activeTab={activeTab}
        onChange={handleTabChange}
        type='line'
        size='default'
        className='settings-mobile-tabs [&_.arco-tabs-nav]:border-b-0'
      >
        {menuItems.map((item) => (
          <Tabs.TabPane key={item.key} title={item.label} />
        ))}
      </Tabs>
    </div>
  );

  const desktopMenu = (
    <SynonScrollArea className='flex-shrink-0 b-color-border-2 scrollbar-hide' style={{ width: `${SIDEBAR_WIDTH}px` }}>
      <div className='flex flex-col gap-2px'>
        {menuItems.map((item) => (
          <div
            key={item.key}
            className={classNames(
              'flex items-center px-14px py-10px rd-8px cursor-pointer transition-all duration-150 select-none',
              {
                'bg-aou-2 text-t-primary': activeTab === item.key,
                'text-t-secondary hover:bg-fill-1': activeTab !== item.key,
              }
            )}
            onClick={() => setActiveTab(item.key)}
          >
            <span className='mr-12px text-16px line-height-[10px]'>{item.icon}</span>
            <span className='text-14px font-500 flex-1 lh-22px'>{item.label}</span>
          </div>
        ))}
      </div>
    </SynonScrollArea>
  );

  return (
    <SettingsViewModeProvider value='modal'>
      <SettingsTabNavigateProvider value={handleTabChange}>
        <SynonModal
          visible={visible}
          onCancel={onCancel}
          footer={null}
          className='settings-modal'
          style={{
            width: isMobile
              ? `min(calc(100vw - 32px), ${MODAL_WIDTH.mobile}px)`
              : `clamp(var(--app-min-width, 360px), 100vw, ${MODAL_WIDTH.desktop}px)`,
            maxHeight: isMobile ? MODAL_HEIGHT.mobile : undefined,
            borderRadius: '16px',
          }}
          contentStyle={{ padding: isMobile ? '16px' : '24px 24px 32px' }}
          title={t('settings.title')}
        >
          <div
            className={classNames('overflow-hidden gap-0', isMobile ? 'flex flex-col min-h-0' : 'flex mt-20px')}
            style={{
              height: isMobile ? MODAL_HEIGHT.mobileContent : `${MODAL_HEIGHT.desktop}px`,
            }}
          >
            {isMobile ? mobileMenu : desktopMenu}

            <SynonScrollArea
              className={classNames('flex-1 min-h-0', isMobile ? 'overflow-y-auto' : 'flex flex-col pl-24px gap-16px')}
            >
              {renderContent()}
            </SynonScrollArea>
          </div>
        </SynonModal>
      </SettingsTabNavigateProvider>
    </SettingsViewModeProvider>
  );
};

export default SettingsModal;
