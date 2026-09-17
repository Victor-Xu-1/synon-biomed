import type { TChatConversation } from '@/common/config/storage';
import type { SynonBiomedSessionUpdate } from '@/renderer/services/synonBiomedSessionActions';
import { Alert, Input, Modal } from '@arco-design/web-react';
import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';

type SessionEditorModalProps = {
  conversation: TChatConversation | null;
  loading: boolean;
  error: string | null;
  onCancel: () => void;
  onSubmit: (update: SynonBiomedSessionUpdate) => void;
};

const SessionEditorModal: React.FC<SessionEditorModalProps> = ({
  conversation,
  loading,
  error,
  onCancel,
  onSubmit,
}) => {
  const { t } = useTranslation();
  const [name, setName] = useState('');
  const [taskSummary, setTaskSummary] = useState('');

  useEffect(() => {
    setName(conversation?.name ?? '');
    setTaskSummary(conversation?.desc ?? '');
  }, [conversation]);

  const submit = () => {
    const trimmedName = name.trim();
    if (!trimmedName || loading) return;
    onSubmit({ name: trimmedName, taskSummary: taskSummary.trim() });
  };

  return (
    <Modal
      visible={conversation !== null}
      title={t('conversation.history.sessionEditor.title')}
      okText={t('conversation.history.saveName')}
      cancelText={t('conversation.history.cancelEdit')}
      confirmLoading={loading}
      okButtonProps={{ disabled: !name.trim() }}
      onOk={submit}
      onCancel={onCancel}
      style={{ width: 'min(520px, calc(100vw - 32px))' }}
      alignCenter
      unmountOnExit
      getPopupContainer={() => document.body}
    >
      <div className='flex flex-col gap-16px'>
        <label className='flex flex-col gap-6px'>
          <span className='text-13px font-[500] text-t-primary'>{t('conversation.history.sessionEditor.name')}</span>
          <Input
            value={name}
            onChange={setName}
            placeholder={t('conversation.history.sessionEditor.namePlaceholder')}
            autoFocus
          />
        </label>
        <label className='flex flex-col gap-6px'>
          <span className='text-13px font-[500] text-t-primary'>{t('conversation.history.sessionEditor.summary')}</span>
          <textarea
            value={taskSummary}
            onChange={(event) => setTaskSummary(event.target.value)}
            rows={4}
            className='w-full box-border resize-none rd-4px border border-solid border-[var(--color-border-2)] bg-1 px-10px py-8px text-14px leading-20px text-t-primary outline-none focus:border-[rgb(var(--primary-6))]'
            placeholder={t('conversation.history.sessionEditor.summaryPlaceholder')}
          />
        </label>
        {error && <Alert type='error' content={error} />}
      </div>
    </Modal>
  );
};

export default SessionEditorModal;
