/**
 * SkillConfirmModals — Two small confirmation modals:
 * 1. Delete pending skill confirmation
 * 2. Remove custom skill from assistant confirmation
 */
import type { Message } from '@arco-design/web-react';
import type { PendingSkill } from './types';
import { Modal } from '@arco-design/web-react';
import React from 'react';
import { useTranslation } from 'react-i18next';

type SkillConfirmModalsProps = {
  // Delete pending skill
  deletePendingSkillName: string | null;
  setDeletePendingSkillName: (v: string | null) => void;
  pendingSkills: PendingSkill[];
  setPendingSkills: (v: PendingSkill[]) => void;

  // Delete custom skill
  deleteCustomSkillName: string | null;
  setDeleteCustomSkillName: (v: string | null) => void;

  // Shared state
  customSkills: string[];
  setCustomSkills: (v: string[]) => void;
  selectedSkills: string[];
  setSelectedSkills: (v: string[]) => void;

  message: ReturnType<typeof Message.useMessage>[0];
};

const SkillConfirmModals: React.FC<SkillConfirmModalsProps> = ({
  deletePendingSkillName,
  setDeletePendingSkillName,
  pendingSkills,
  setPendingSkills,
  deleteCustomSkillName,
  setDeleteCustomSkillName,
  customSkills,
  setCustomSkills,
  selectedSkills,
  setSelectedSkills,
  message,
}) => {
  const { t } = useTranslation();

  return (
    <>
      {/* Delete Pending Skill Confirmation Modal */}
      <Modal
        visible={deletePendingSkillName !== null}
        onCancel={() => setDeletePendingSkillName(null)}
        title={t('settings.deletePendingSkillTitle')}
        okButtonProps={{ status: 'danger' }}
        okText={t('common.delete')}
        cancelText={t('common.cancel')}
        onOk={() => {
          if (deletePendingSkillName) {
            setPendingSkills(pendingSkills.filter((s) => s.name !== deletePendingSkillName));
            setCustomSkills(customSkills.filter((s) => s !== deletePendingSkillName));
            setSelectedSkills(selectedSkills.filter((s) => s !== deletePendingSkillName));
            setDeletePendingSkillName(null);
            message.success(t('settings.skillDeleted'));
          }
        }}
        className='w-[90vw] md:w-[400px]'
        wrapStyle={{ zIndex: 10000 }}
        maskStyle={{ zIndex: 9999 }}
      >
        <p>{t('settings.deletePendingSkillConfirm', { name: deletePendingSkillName ?? '' })}</p>
        <div className='mt-12px text-12px text-t-secondary bg-fill-2 p-12px rounded-lg'>
          {t('settings.deletePendingSkillNote')}
        </div>
      </Modal>

      {/* Remove Custom Skill from Assistant Modal */}
      <Modal
        visible={deleteCustomSkillName !== null}
        onCancel={() => setDeleteCustomSkillName(null)}
        title={t('settings.removeCustomSkillTitle')}
        okButtonProps={{ status: 'danger' }}
        okText={t('common.remove')}
        cancelText={t('common.cancel')}
        onOk={() => {
          if (deleteCustomSkillName) {
            setCustomSkills(customSkills.filter((s) => s !== deleteCustomSkillName));
            setSelectedSkills(selectedSkills.filter((s) => s !== deleteCustomSkillName));
            setDeleteCustomSkillName(null);
            message.success(t('settings.skillRemovedFromAssistant'));
          }
        }}
        className='w-[90vw] md:w-[400px]'
        wrapStyle={{ zIndex: 10000 }}
        maskStyle={{ zIndex: 9999 }}
      >
        <p>{t('settings.removeCustomSkillConfirm', { name: deleteCustomSkillName ?? '' })}</p>
        <div className='mt-12px text-12px text-t-secondary bg-fill-2 p-12px rounded-lg'>
          {t('settings.removeCustomSkillNote')}
        </div>
      </Modal>
    </>
  );
};

export default SkillConfirmModals;
