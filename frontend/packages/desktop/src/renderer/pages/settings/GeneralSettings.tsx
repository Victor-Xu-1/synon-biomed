import { Input, Message, Modal, Select, Tag } from '@arco-design/web-react';
import React, { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  COOL_DARK_THEME_ID,
  COOL_LIGHT_THEME_ID,
  COOL_SYSTEM_THEME_ID,
  NIGHT_DARK_THEME_ID,
  SYSTEM_THEME_ID,
  WHITE_LIGHT_THEME_ID,
  WARM_LIGHT_THEME_ID,
} from '@/common/theme/constants';
import { useThemeContext } from '@/renderer/hooks/context/ThemeContext';
import LanguageSwitcher from '@/renderer/components/settings/LanguageSwitcher';
import {
  loadSynonBiomedContactEmail,
  removeSynonBiomedContactEmail,
  setSynonBiomedContactEmail,
  type SynonBiomedContactEmail,
} from '@/renderer/services/synonBiomedWorkspaceSettings';
import SettingsPageHeader from './components/SettingsPageHeader';
import SettingsPageWrapper from './components/SettingsPageWrapper';
import ThirdPartyLicensesModal from './components/ThirdPartyLicensesModal';
import { SettingsSection } from './components/SettingsPrimitives';
import MessageChannelsSettings from './MessageChannelsSettings';

declare const __APP_VERSION__: string;
const APP_VERSION = typeof __APP_VERSION__ === 'string' ? __APP_VERSION__ : 'dev';

type VisualThemePreset = 'warm' | 'cool' | 'white' | 'night';

function getVisualThemePreset(activeId: string, resolvedThemeId?: string | null): VisualThemePreset {
  const id = resolvedThemeId || activeId;
  if (id === COOL_LIGHT_THEME_ID || id === COOL_DARK_THEME_ID || id === COOL_SYSTEM_THEME_ID) return 'cool';
  if (id === WHITE_LIGHT_THEME_ID) return 'white';
  if (id === NIGHT_DARK_THEME_ID) return 'night';
  return 'warm';
}

function getVisualThemeId(preset: VisualThemePreset): string {
  if (preset === 'cool') return COOL_LIGHT_THEME_ID;
  if (preset === 'white') return WHITE_LIGHT_THEME_ID;
  if (preset === 'night') return NIGHT_DARK_THEME_ID;
  return WARM_LIGHT_THEME_ID;
}

const GeneralSettings: React.FC = () => {
  const { i18n, t } = useTranslation();
  const { selectTheme, activeId: activeThemeId, activeTheme } = useThemeContext();
  const selectedThemeId = activeThemeId ?? SYSTEM_THEME_ID;
  const activeVisualTheme = getVisualThemePreset(selectedThemeId, activeTheme?.id);
  const [contact, setContact] = useState<SynonBiomedContactEmail | null>(null);
  const [emailEditor, setEmailEditor] = useState(false);
  const [email, setEmail] = useState('');
  const [emailError, setEmailError] = useState(false);
  const [removeEmailOpen, setRemoveEmailOpen] = useState(false);
  const [removingEmail, setRemovingEmail] = useState(false);
  const [licenseOpen, setLicenseOpen] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const nextContact = await loadSynonBiomedContactEmail();
      setContact(nextContact);
    } catch (error) {
      console.error('Failed to load general settings:', error);
      Message.error(i18n.t('settings.generalSettings.loadGeneralFailed'));
    }
  }, [i18n]);

  useEffect(() => void refresh(), [refresh]);

  const saveEmail = async () => {
    const value = email.trim();
    if (!value || !contact) return;
    if (!isValidContactEmail(value)) {
      setEmailError(true);
      return;
    }
    try {
      await setSynonBiomedContactEmail(value, contact.noticeVersion);
      setEmailEditor(false);
      setEmail('');
      setEmailError(false);
      await refresh();
    } catch (error) {
      console.error('Failed to save contact email:', error);
      Message.error(t('settings.generalSettings.saveContactFailed'));
    }
  };

  const removeEmail = async () => {
    setRemovingEmail(true);
    try {
      await removeSynonBiomedContactEmail();
      setRemoveEmailOpen(false);
      await refresh();
    } catch (error) {
      console.error('Failed to remove contact email:', error);
      Message.error(t('settings.generalSettings.removeContactFailed'));
    } finally {
      setRemovingEmail(false);
    }
  };

  const changeTheme = async (preset: VisualThemePreset) => {
    try {
      await selectTheme(getVisualThemeId(preset));
    } catch (error) {
      console.error('Failed to apply theme:', error);
      Message.error(t('settings.cssTheme.applyFailed'));
    }
  };

  return (
    <SettingsPageWrapper>
      <div className='settings-general-page flex flex-col' data-testid='synon-general-settings'>
        <SettingsPageHeader
          title={t('settings.generalSettings.title')}
          description={t('settings.generalSettings.description')}
        />

        <div className='general-preferences-grid'>
          <SettingsSection
            title={t('settings.generalSettings.languageTitle')}
            description={t('settings.generalSettings.languageDescription')}
            icon='general'
          >
            <SettingsListRow
              label={t('settings.generalSettings.languageLabel')}
              hint={t('settings.generalSettings.languageHint')}
            >
              <LanguageSwitcher />
            </SettingsListRow>
          </SettingsSection>

          <SettingsSection
            title={t('settings.generalSettings.appearanceTitle')}
            description={t('settings.generalSettings.appearanceDescription')}
            icon='general'
          >
            <div className='py-4px grid gap-10px'>
              <div className='settings-list-row flex items-center justify-between gap-12px flex-wrap'>
                <span className='text-13px text-t-primary'>{t('settings.generalSettings.themeFamily')}</span>
                <Select
                  value={activeVisualTheme}
                  onChange={(preset) => void changeTheme(preset as VisualThemePreset)}
                  className='w-180px max-w-full'
                  aria-label={t('settings.generalSettings.themeFamily')}
                  data-testid='theme-family-select'
                >
                  <Select.Option value='cool'>{t('settings.generalSettings.themeCool')}</Select.Option>
                  <Select.Option value='warm'>{t('settings.generalSettings.themeWarm')}</Select.Option>
                  <Select.Option value='white'>{t('settings.generalSettings.themeWhite')}</Select.Option>
                  <Select.Option value='night'>{t('settings.generalSettings.themeNight')}</Select.Option>
                </Select>
              </div>
            </div>
          </SettingsSection>
        </div>

        <MessageChannelsSettings />

        <SettingsSection
          className='general-contact-section'
          title={t('settings.generalSettings.contactTitle')}
          icon='network'
          actions={
            <div className='flex gap-6px'>
              <button
                type='button'
                className='settings-action-button'
                onClick={() => {
                  setEmail(contact?.email ?? '');
                  setEmailError(false);
                  setEmailEditor((open) => !open);
                }}
              >
                {contact?.decision === 'allowed'
                  ? t('settings.generalSettings.change')
                  : t('settings.generalSettings.set')}
              </button>
              {contact?.decision && contact.decision !== 'revoked' ? (
                <button type='button' className='settings-text-danger-button' onClick={() => setRemoveEmailOpen(true)}>
                  {t('settings.generalSettings.remove')}
                </button>
              ) : null}
            </div>
          }
        >
          <div className='py-12px'>
            <div className='flex items-center gap-8px'>
              <span className='text-13px text-t-primary'>{contactStatus(contact, t)}</span>
              {contact?.noticeStale ? <Tag color='orange'>{t('settings.generalSettings.contactStale')}</Tag> : null}
            </div>
            <p className='m-0 mt-5px text-11px text-t-tertiary'>{t('settings.generalSettings.contactDescription')}</p>
            {emailEditor ? (
              <div className='mt-12px flex flex-col gap-9px'>
                <p className='m-0 whitespace-pre-wrap text-11px leading-5 text-t-secondary'>{contact?.noticeText}</p>
                <div className='flex gap-8px'>
                  <Input
                    value={email}
                    onChange={(value) => {
                      setEmail(value);
                      setEmailError(false);
                    }}
                    type='email'
                    placeholder='you@example.com'
                    onPressEnter={() => void saveEmail()}
                  />
                  <button
                    type='button'
                    className='settings-action-button'
                    disabled={!email.trim()}
                    onClick={() => void saveEmail()}
                  >
                    {t('settings.generalSettings.agreeAndSave')}
                  </button>
                </div>
                {emailError ? (
                  <div role='alert' className='text-11px text-danger-6'>
                    {t('settings.generalSettings.invalidEmail')}
                  </div>
                ) : null}
              </div>
            ) : null}
          </div>
        </SettingsSection>

        <SettingsSection
          className='general-about-section'
          title={t('settings.generalSettings.aboutTitle')}
          icon='general'
        >
          <div className='py-12px flex flex-col gap-8px'>
            <div className='flex items-center justify-between gap-12px'>
              <span className='text-13px font-600 text-t-primary'>
                {t('settings.generalSettings.productVersion', {
                  version: APP_VERSION,
                })}
              </span>
              <Tag color='green'>{t('settings.generalSettings.localWorkbench')}</Tag>
            </div>
            <div className='text-11px text-t-tertiary'>{t('settings.generalSettings.localChannel')}</div>
            <div className='flex gap-8px pt-2px'>
              <button
                type='button'
                className='settings-action-button'
                data-testid='third-party-licenses-open'
                onClick={() => setLicenseOpen(true)}
              >
                {t('settings.generalSettings.thirdPartyLicenses')}
              </button>
            </div>
          </div>
        </SettingsSection>
      </div>
      <Modal
        title={t('settings.generalSettings.removeContactTitle')}
        visible={removeEmailOpen}
        onCancel={() => setRemoveEmailOpen(false)}
        onOk={() => void removeEmail()}
        confirmLoading={removingEmail}
        okButtonProps={{ status: 'danger' }}
        okText={t('settings.generalSettings.remove')}
        unmountOnExit
      >
        {t('settings.generalSettings.removeContactBody')}
      </Modal>
      <ThirdPartyLicensesModal visible={licenseOpen} onClose={() => setLicenseOpen(false)} />
    </SettingsPageWrapper>
  );
};

function contactStatus(contact: SynonBiomedContactEmail | null, t: (key: string) => string): string {
  if (contact?.decision === 'allowed' && contact.email) return contact.email;
  if (contact?.decision === 'declined') return t('settings.generalSettings.contactDeclined');
  if (contact?.decision === 'revoked') return t('settings.generalSettings.contactRevoked');
  return t('settings.generalSettings.contactUnset');
}

function isValidContactEmail(value: string): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value);
}

const SettingsListRow: React.FC<React.PropsWithChildren<{ label: string; hint: string }>> = ({
  label,
  hint,
  children,
}) => (
  <div className='py-10px flex flex-col sm:flex-row sm:items-center justify-between gap-8px sm:gap-16px'>
    <div className='min-w-0'>
      <div className='text-13px text-t-primary'>{label}</div>
      <div className='mt-2px text-11px text-t-tertiary'>{hint}</div>
    </div>
    <div className='shrink-0'>{children}</div>
  </div>
);

export default GeneralSettings;
