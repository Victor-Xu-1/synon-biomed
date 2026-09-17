import {
  createSynonBiomedExpertProfile,
  deleteSynonBiomedExpertProfile,
  normalizeSynonBiomedExpertName,
  updateSynonBiomedExpertProfile,
  type SynonBiomedExpertProfile,
} from '@/renderer/services/agents/synonBiomedExpertProfiles';
import { Form, Input, Message, Modal, Switch } from '@arco-design/web-react';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

type SynonBiomedExpertProfileModalProps = {
  visible: boolean;
  profile: SynonBiomedExpertProfile | null;
  onClose: () => void;
  onChanged: () => void | Promise<void>;
};

const SynonBiomedExpertProfileModal: React.FC<SynonBiomedExpertProfileModalProps> = ({
  visible,
  profile,
  onClose,
  onChanged,
}) => {
  const { t } = useTranslation();
  const [name, setName] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [description, setDescription] = useState('');
  const [systemPrompt, setSystemPrompt] = useState('');
  const [enabled, setEnabled] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!visible) return;
    setName(profile?.name ?? '');
    setDisplayName(profile?.displayName ?? '');
    setDescription(profile?.description ?? '');
    setSystemPrompt(profile?.systemPrompt ?? '');
    setEnabled(profile?.enabled ?? true);
  }, [profile, visible]);

  const normalizedName = useMemo(() => normalizeSynonBiomedExpertName(name), [name]);
  const valid = normalizedName.length >= 2 && displayName.trim().length > 0 && description.trim().length > 0 && !saving;

  const save = async () => {
    if (!valid) return;
    setSaving(true);
    try {
      const input = {
        name: normalizedName,
        displayName: displayName.trim(),
        description: description.trim(),
        systemPrompt,
        enabled,
      };
      if (profile) await updateSynonBiomedExpertProfile(profile.name, input);
      else await createSynonBiomedExpertProfile(input);
      Message.success(
        profile ? t('settings.expertsSettings.profile.updated') : t('settings.expertsSettings.profile.created')
      );
      await onChanged();
      onClose();
    } catch (error) {
      console.error('Failed to save expert profile:', error);
      Message.error(t('settings.expertsSettings.profile.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  const requestDelete = () => {
    if (!profile) return;
    Modal.confirm({
      title: t('settings.expertsSettings.deleteTitle'),
      content: t('settings.expertsSettings.deleteBody', { name: profile.displayName }),
      okButtonProps: { status: 'danger' },
      onOk: async () => {
        try {
          await deleteSynonBiomedExpertProfile(profile.name);
          Message.success(t('settings.expertsSettings.profile.deleted'));
          await onChanged();
          onClose();
        } catch (error) {
          console.error('Failed to delete expert profile:', error);
          Message.error(t('settings.expertsSettings.deleteFailed'));
          throw error;
        }
      },
    });
  };

  return (
    <Modal
      title={profile ? t('settings.expertsSettings.profile.editTitle') : t('settings.expertsSettings.profile.addTitle')}
      visible={visible}
      onCancel={onClose}
      onOk={() => void save()}
      confirmLoading={saving}
      okButtonProps={{ disabled: !valid }}
      okText={t('common.save')}
      cancelText={t('common.cancel')}
      unmountOnExit
    >
      <Form layout='vertical'>
        <Form.Item
          label={t('settings.expertsSettings.profile.identifier')}
          required
          extra={t('settings.expertsSettings.profile.identifierHint')}
        >
          <Input
            aria-label={t('settings.expertsSettings.profile.identifierAria')}
            value={name}
            onChange={setName}
            placeholder='LITERATURE_REVIEWER'
            maxLength={32}
          />
        </Form.Item>
        <Form.Item label={t('settings.expertsSettings.name')} required>
          <Input
            aria-label={t('settings.expertsSettings.profile.nameAria')}
            value={displayName}
            onChange={setDisplayName}
            placeholder={t('settings.expertsSettings.profile.namePlaceholder')}
          />
        </Form.Item>
        <Form.Item label={t('settings.expertsSettings.descriptionField')} required>
          <Input.TextArea
            aria-label={t('settings.expertsSettings.profile.descriptionAria')}
            value={description}
            onChange={setDescription}
            placeholder={t('settings.expertsSettings.profile.descriptionPlaceholder')}
            autoSize={{ minRows: 2, maxRows: 4 }}
          />
        </Form.Item>
        <Form.Item label={t('settings.expertsSettings.profile.systemPrompt')}>
          <Input.TextArea
            aria-label={t('settings.expertsSettings.profile.systemPrompt')}
            value={systemPrompt}
            onChange={setSystemPrompt}
            placeholder={t('settings.expertsSettings.profile.systemPromptPlaceholder')}
            autoSize={{ minRows: 5, maxRows: 10 }}
          />
        </Form.Item>
        <div className='flex items-center justify-between gap-12px py-6px'>
          <span className='text-13px text-t-primary'>{t('settings.expertsSettings.enabled')}</span>
          <Switch
            aria-label={t('settings.expertsSettings.profile.enableAria')}
            checked={enabled}
            onChange={setEnabled}
          />
        </div>
        {profile ? (
          <button type='button' className='settings-text-danger-button mt-12px' onClick={requestDelete}>
            {t('settings.expertsSettings.deleteExpert')}
          </button>
        ) : null}
      </Form>
    </Modal>
  );
};

export default SynonBiomedExpertProfileModal;
