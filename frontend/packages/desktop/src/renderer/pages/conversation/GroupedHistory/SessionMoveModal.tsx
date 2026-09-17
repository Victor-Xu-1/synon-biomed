import type { TChatConversation } from '@/common/config/storage';
import type { SynonBiomedProject } from '@/renderer/services/synonBiomedGateway';
import { Alert, Empty, Modal, Select, Spin } from '@arco-design/web-react';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

type SessionMoveModalProps = {
  conversation: TChatConversation | null;
  projects: SynonBiomedProject[];
  currentProjectId: string | null;
  loading: boolean;
  error: string | null;
  onCancel: () => void;
  onSubmit: (projectId: string) => void;
};

const SessionMoveModal: React.FC<SessionMoveModalProps> = ({
  conversation,
  projects,
  currentProjectId,
  loading,
  error,
  onCancel,
  onSubmit,
}) => {
  const { t } = useTranslation();
  const choices = useMemo(
    () => projects.filter((project) => project.projectId !== currentProjectId),
    [currentProjectId, projects]
  );
  const [projectId, setProjectId] = useState('');

  useEffect(() => {
    setProjectId(choices[0]?.projectId ?? '');
  }, [choices, conversation]);

  return (
    <Modal
      visible={conversation !== null}
      title={t('conversation.history.sessionMove.title')}
      okText={t('conversation.history.sessionMove.confirm')}
      cancelText={t('conversation.history.cancelEdit')}
      confirmLoading={loading}
      okButtonProps={{ disabled: !projectId || loading }}
      onOk={() => projectId && onSubmit(projectId)}
      onCancel={onCancel}
      style={{ width: 'min(520px, calc(100vw - 32px))' }}
      alignCenter
      unmountOnExit
      getPopupContainer={() => document.body}
    >
      <div className='flex flex-col gap-14px'>
        <p className='m-0 text-14px leading-22px text-t-secondary'>
          {t('conversation.history.sessionMove.description', { name: conversation?.name })}
        </p>
        {loading && projects.length === 0 ? (
          <div className='h-64px flex-center'>
            <Spin size={18} />
          </div>
        ) : choices.length === 0 ? (
          <Empty description={t('conversation.history.sessionMove.empty')} />
        ) : (
          <Select
            aria-label={t('conversation.history.sessionMove.projectLabel')}
            value={projectId}
            onChange={setProjectId}
            options={choices.map((project) => ({ label: project.name, value: project.projectId }))}
          />
        )}
        {error && <Alert type='error' content={error} />}
      </div>
    </Modal>
  );
};

export default SessionMoveModal;
