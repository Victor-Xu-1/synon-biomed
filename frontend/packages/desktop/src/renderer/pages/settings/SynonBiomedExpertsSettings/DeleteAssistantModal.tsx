/**
 * DeleteAssistantModal — Confirmation modal for deleting an assistant.
 */
import type { AssistantListItem } from './types';
import AssistantAvatar from './AssistantAvatar';
import { Modal } from '@arco-design/web-react';
import React from 'react';
import { useTranslation } from 'react-i18next';

type DeleteAssistantModalProps = {
  visible: boolean;
  onCancel: () => void;
  onConfirm: () => void;
  activeAssistant: AssistantListItem | null;
};

const DeleteAssistantModal: React.FC<DeleteAssistantModalProps> = ({
  visible,
  onCancel,
  onConfirm,
  activeAssistant,
}) => {
  const { t } = useTranslation();

  return (
    <Modal
      title={t('settings.deleteAssistantTitle')}
      visible={visible}
      onCancel={onCancel}
      onOk={onConfirm}
      okButtonProps={{ status: 'danger' }}
      wrapClassName='delete-assistant-modal'
      data-testid='modal-delete-assistant'
      okText={t('common.delete')}
      cancelText={t('common.cancel')}
      className='w-[90vw] md:w-[400px]'
      wrapStyle={{ zIndex: 10000 }}
      maskStyle={{ zIndex: 9999 }}
    >
      <p>{t('settings.deleteAssistantConfirm')}</p>
      {activeAssistant && (
        <div className='mt-12px p-12px bg-fill-2 rounded-lg flex items-center gap-12px'>
          <AssistantAvatar assistant={activeAssistant} size={32} />
          <div>
            <div className='font-medium'>{activeAssistant.name}</div>
            <div className='text-12px text-t-secondary'>{activeAssistant.description}</div>
          </div>
        </div>
      )}
    </Modal>
  );
};

export default DeleteAssistantModal;
