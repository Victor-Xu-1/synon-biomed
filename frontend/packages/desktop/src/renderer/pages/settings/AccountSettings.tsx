import { Button, Message, Modal } from '@arco-design/web-react';
import { Refresh } from '@icon-park/react';
import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useAuth } from '@/renderer/hooks/context/AuthContext';
import { useSynonBiomedAccountInsights } from '@/renderer/hooks/account/useSynonBiomedAccountInsights';
import { useSynonBiomedUserProfile } from '@/renderer/hooks/useSynonBiomedUserProfile';
import { readSynonBiomedAvatarFile } from '@/renderer/services/synonBiomedUserProfile';
import { copyText } from '@/renderer/utils/ui/clipboard';
import AccountActivityHeatmap from './account/AccountActivityHeatmap';
import AccountInsightsPanels from './account/AccountInsightsPanels';
import AccountMetricsStrip from './account/AccountMetricsStrip';
import AccountProfileHero from './account/AccountProfileHero';
import AccountSecurityPanel from './account/AccountSecurityPanel';
import './account/AccountSettings.css';
import SettingsPageWrapper from './components/SettingsPageWrapper';

declare const __APP_VERSION__: string;
const APP_VERSION = typeof __APP_VERSION__ === 'string' ? __APP_VERSION__ : 'dev';

const AccountSettings: React.FC = () => {
  const { t } = useTranslation();
  const { user, logout } = useAuth();
  const { profile, saveProfile } = useSynonBiomedUserProfile(
    user?.id,
    user?.username ?? t('settings.synonBiomedLocalUser')
  );
  const accountInsights = useSynonBiomedAccountInsights();
  const [editingName, setEditingName] = useState(false);
  const [nameDraft, setNameDraft] = useState('');
  const [avatarLoading, setAvatarLoading] = useState(false);
  const [privacyVisible, setPrivacyVisible] = useState(false);
  const username = user?.username?.trim() || t('settings.synonBiomedLocalUser');

  const saveDisplayName = () => {
    const displayName = nameDraft.trim();
    if (!displayName) {
      Message.error(t('settings.accountSettings.displayNameRequired'));
      return;
    }
    try {
      saveProfile({ ...profile, displayName });
      setEditingName(false);
      Message.success(t('settings.accountSettings.profileSaved'));
    } catch (error) {
      console.error('Failed to save Synon Biomed display name:', error);
      Message.error(t('settings.accountSettings.profileSaveFailed'));
    }
  };

  const updateAvatar = async (file: File | undefined) => {
    if (!file || avatarLoading) return;
    setAvatarLoading(true);
    try {
      const avatarDataUrl = await readSynonBiomedAvatarFile(file);
      saveProfile({ ...profile, avatarDataUrl });
      Message.success(t('settings.accountSettings.avatarSaved'));
    } catch (error) {
      console.error('Failed to save Synon Biomed avatar:', error);
      const code = error instanceof Error ? error.message : '';
      Message.error(
        code === 'user_profile_avatar_type_invalid'
          ? t('settings.accountSettings.avatarTypeInvalid')
          : code === 'user_profile_avatar_size_invalid'
            ? t('settings.accountSettings.avatarSizeInvalid')
            : t('settings.accountSettings.avatarSaveFailed')
      );
    } finally {
      setAvatarLoading(false);
    }
  };

  const removeAvatar = () => {
    try {
      saveProfile({ ...profile, avatarDataUrl: null });
      Message.success(t('settings.accountSettings.avatarRemoved'));
    } catch (error) {
      console.error('Failed to remove Synon Biomed avatar:', error);
      Message.error(t('settings.accountSettings.avatarSaveFailed'));
    }
  };

  const copyAccountSummary = async () => {
    const insights = accountInsights.data;
    const lines = [
      t('settings.accountSettings.summaryHeading'),
      `${t('settings.accountSettings.displayNameLabel')}: ${profile.displayName}`,
      `${t('settings.accountSettings.localIdentity')}: @${username}`,
      `${t('settings.accountSettings.totalTasks')}: ${insights?.metrics.totalTasks ?? '—'}`,
      `${t('settings.accountSettings.completedTasks')}: ${insights?.metrics.completedTasks ?? '—'}`,
      `${t('settings.accountSettings.totalProjects')}: ${insights?.metrics.projectCount ?? '—'}`,
      `${t('settings.accountSettings.totalArtifacts')}: ${insights?.metrics.artifactCount ?? '—'}`,
      `${t('common.version')}: v${APP_VERSION}`,
    ];
    try {
      await copyText(lines.join('\n'));
      Message.success(t('settings.accountSettings.summaryCopied'));
    } catch (error) {
      console.error('Failed to copy Synon Biomed account summary:', error);
      Message.error(t('settings.accountSettings.summaryCopyFailed'));
    }
  };

  return (
    <SettingsPageWrapper contentClassName='max-w-1080px'>
      <div className='account-settings pb-24px' data-testid='synon-account-settings'>
        <AccountProfileHero
          displayName={profile.displayName}
          username={username}
          avatarDataUrl={profile.avatarDataUrl}
          editing={editingName}
          nameDraft={nameDraft}
          avatarLoading={avatarLoading}
          onBeginEdit={() => {
            setNameDraft(profile.displayName);
            setEditingName(true);
          }}
          onCancelEdit={() => setEditingName(false)}
          onNameDraftChange={setNameDraft}
          onSaveName={saveDisplayName}
          onAvatarSelected={(file) => void updateAvatar(file)}
          onRemoveAvatar={removeAvatar}
          onCopySummary={() => void copyAccountSummary()}
          onOpenPrivacy={() => setPrivacyVisible(true)}
        />

        <AccountMetricsStrip
          insights={accountInsights.data}
          loading={accountInsights.loading && !accountInsights.data}
        />

        {accountInsights.loading && !accountInsights.data ? (
          <div className='account-loading-panel' role='status' aria-live='polite'>
            <span className='account-loading-panel__pulse' aria-hidden='true' />
            <span>{t('settings.accountSettings.loadingInsights')}</span>
          </div>
        ) : accountInsights.error && !accountInsights.data ? (
          <div className='account-error-panel' role='alert'>
            <strong>{t('settings.accountSettings.insightsLoadFailed')}</strong>
            <span>{t('settings.accountSettings.insightsLoadFailedDescription')}</span>
            <Button size='small' onClick={accountInsights.retry}>
              <Refresh theme='outline' size={14} />
              {t('common.retry')}
            </Button>
          </div>
        ) : accountInsights.data ? (
          <>
            <AccountActivityHeatmap insights={accountInsights.data} />
            <AccountInsightsPanels insights={accountInsights.data} onRetry={accountInsights.retry} />
          </>
        ) : null}

        <AccountSecurityPanel onSignedOut={logout} />

        <Modal
          visible={privacyVisible}
          title={t('settings.accountSettings.privacyTitle')}
          footer={null}
          focusLock
          unmountOnExit
          onCancel={() => setPrivacyVisible(false)}
        >
          <ul className='account-privacy-list'>
            <li>
              <strong>{t('settings.accountSettings.privacyProfileTitle')}</strong>
              <span>{t('settings.accountSettings.privacyProfileDescription')}</span>
            </li>
            <li>
              <strong>{t('settings.accountSettings.privacyActivityTitle')}</strong>
              <span>{t('settings.accountSettings.privacyActivityDescription')}</span>
            </li>
            <li>
              <strong>{t('settings.accountSettings.privacyShareTitle')}</strong>
              <span>{t('settings.accountSettings.privacyShareDescription')}</span>
            </li>
          </ul>
        </Modal>
      </div>
    </SettingsPageWrapper>
  );
};

export default AccountSettings;
