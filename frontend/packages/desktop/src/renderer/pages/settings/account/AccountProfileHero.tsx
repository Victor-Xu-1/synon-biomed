import { Button, Input } from '@arco-design/web-react';
import { Camera, CopyOne, Delete, Edit, Lock } from '@icon-park/react';
import React, { useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { SYNON_BIOMED_DISPLAY_NAME_MAX_LENGTH } from '@/renderer/services/synonBiomedUserProfile';
import { SettingsGeneratedArtwork } from '../components/SettingsGeneratedAsset';
import './AccountProfileHero.css';

type AccountProfileHeroProps = {
  displayName: string;
  username: string;
  avatarDataUrl: string | null;
  editing: boolean;
  nameDraft: string;
  avatarLoading: boolean;
  onBeginEdit: () => void;
  onCancelEdit: () => void;
  onNameDraftChange: (value: string) => void;
  onSaveName: () => void;
  onAvatarSelected: (file: File | undefined) => void;
  onRemoveAvatar: () => void;
  onCopySummary: () => void;
  onOpenPrivacy: () => void;
};

const AccountProfileHero: React.FC<AccountProfileHeroProps> = ({
  displayName,
  username,
  avatarDataUrl,
  editing,
  nameDraft,
  avatarLoading,
  onBeginEdit,
  onCancelEdit,
  onNameDraftChange,
  onSaveName,
  onAvatarSelected,
  onRemoveAvatar,
  onCopySummary,
  onOpenPrivacy,
}) => {
  const { t } = useTranslation();
  const avatarInputRef = useRef<HTMLInputElement | null>(null);
  const avatarLetter = displayName.charAt(0).toUpperCase();

  return (
    <section className='account-profile-hero' aria-labelledby='account-profile-name' data-testid='account-profile-hero'>
      <div className='account-profile-hero__identity'>
        <div className='account-profile-avatar-wrap'>
          <button
            type='button'
            className='account-profile-avatar'
            aria-label={t('settings.accountSettings.changeAvatar')}
            onClick={() => avatarInputRef.current?.click()}
          >
            {avatarDataUrl ? <img src={avatarDataUrl} alt='' /> : <span>{avatarLetter}</span>}
            <span className='account-profile-avatar__overlay' aria-hidden='true'>
              <Camera theme='outline' size={20} />
            </span>
          </button>
          <span className='account-profile-avatar__privacy' aria-hidden='true'>
            <Lock theme='outline' size={13} />
          </span>
        </div>
        <input
          ref={avatarInputRef}
          type='file'
          accept='image/png,image/jpeg,image/webp'
          className='hidden'
          data-testid='account-avatar-input'
          onChange={(event) => onAvatarSelected(event.currentTarget.files?.[0])}
        />

        {editing ? (
          <div className='account-profile-editor'>
            <Input
              value={nameDraft}
              maxLength={SYNON_BIOMED_DISPLAY_NAME_MAX_LENGTH}
              autoFocus
              aria-label={t('settings.accountSettings.displayNameLabel')}
              placeholder={t('settings.accountSettings.displayNamePlaceholder')}
              onChange={onNameDraftChange}
              onPressEnter={onSaveName}
            />
            <div className='account-profile-editor__buttons'>
              <Button type='primary' size='small' onClick={onSaveName}>
                {t('settings.accountSettings.save')}
              </Button>
              <Button size='small' onClick={onCancelEdit}>
                {t('settings.accountSettings.cancel')}
              </Button>
              <Button size='small' loading={avatarLoading} onClick={() => avatarInputRef.current?.click()}>
                <Camera theme='outline' size={14} />
                {t('settings.accountSettings.changeAvatar')}
              </Button>
              {avatarDataUrl ? (
                <Button size='small' status='danger' onClick={onRemoveAvatar}>
                  <Delete theme='outline' size={14} />
                  {t('settings.accountSettings.removeAvatar')}
                </Button>
              ) : null}
            </div>
            <p>{t('settings.accountSettings.avatarHelp')}</p>
          </div>
        ) : (
          <div className='account-profile-hero__copy'>
            <h2 id='account-profile-name'>{displayName}</h2>
            <div className='account-profile-hero__handle'>
              <span>@{username}</span>
              <span className='account-profile-hero__separator' aria-hidden='true'>
                ·
              </span>
              <span className='account-local-badge'>
                <Lock theme='outline' size={12} />
                {t('settings.accountSettings.localPrivate')}
              </span>
            </div>
            <p className='account-profile-hero__privacy-copy'>{t('settings.accountSettings.localPrivacySummary')}</p>
          </div>
        )}
      </div>

      {!editing ? <SettingsGeneratedArtwork id='account-network' className='account-profile-network' /> : null}

      <div className='account-profile-hero__actions' aria-label={t('settings.accountSettings.profileActions')}>
        <button
          type='button'
          className='account-profile-action'
          aria-label={t('settings.accountSettings.editName')}
          onClick={onBeginEdit}
        >
          <Edit theme='outline' size={16} />
          <span>{t('settings.accountSettings.editProfile')}</span>
        </button>
        <button type='button' className='account-profile-action' onClick={onCopySummary}>
          <CopyOne theme='outline' size={16} />
          <span>{t('settings.accountSettings.copySummary')}</span>
        </button>
        <button
          type='button'
          className='account-profile-action account-profile-action--privacy'
          onClick={onOpenPrivacy}
        >
          <Lock theme='outline' size={16} />
          <span>{t('settings.accountSettings.privacyDetails')}</span>
        </button>
      </div>
    </section>
  );
};

export default AccountProfileHero;
