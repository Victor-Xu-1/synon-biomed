import type { TChatConversation } from '@/common/config/storage';
import type { SynonBiomedExecutionRecord } from '@/renderer/components/synonBiomed/runtime/runtimeOperationsModel';
import { Alert, Empty, Modal, Spin } from '@arco-design/web-react';
import React from 'react';
import { useTranslation } from 'react-i18next';

type SessionNotebookModalProps = {
  conversation: TChatConversation | null;
  records: SynonBiomedExecutionRecord[];
  loading: boolean;
  error: string | null;
  onClose: () => void;
};

const SessionNotebookModal: React.FC<SessionNotebookModalProps> = ({
  conversation,
  records,
  loading,
  error,
  onClose,
}) => {
  const { t } = useTranslation();
  return (
    <Modal
      visible={conversation !== null}
      title={t('conversation.history.sessionNotebook.title')}
      footer={null}
      onCancel={onClose}
      style={{ width: 'min(860px, calc(100vw - 32px))' }}
      alignCenter
      unmountOnExit
      getPopupContainer={() => document.body}
    >
      <div className='max-h-[70vh] overflow-y-auto'>
        {loading ? (
          <div className='h-120px flex-center'>
            <Spin />
          </div>
        ) : error ? (
          <Alert type='error' content={error} />
        ) : records.length === 0 ? (
          <Empty description={t('conversation.history.sessionNotebook.empty')} />
        ) : (
          <div className='flex flex-col gap-12px'>
            {records.map((record, index) =>
              record.kind === 'cell' ? (
                <section
                  key={record.id}
                  className='border border-solid border-[var(--color-border-2)] bg-1'
                  aria-label={t('conversation.history.sessionNotebook.cellNumber', {
                    number: record.cellIndex + 1,
                  })}
                >
                  <div className='px-12px py-8px flex items-center justify-between border-b border-solid border-[var(--color-border-2)] text-12px text-t-secondary'>
                    <span>{record.language}</span>
                    <span>{record.exitStatus}</span>
                  </div>
                  <pre className='m-0 p-12px overflow-x-auto whitespace-pre-wrap text-12px leading-18px text-t-primary font-mono'>
                    {record.source}
                  </pre>
                  {(record.stdout || record.stderr) && (
                    <pre className='m-0 px-12px py-10px overflow-x-auto whitespace-pre-wrap border-t border-solid border-[var(--color-border-2)] bg-fill-1 text-12px leading-18px text-t-secondary font-mono'>
                      {record.stdout}
                      {record.stderr}
                    </pre>
                  )}
                </section>
              ) : (
                <div key={record.id} className='px-12px py-9px bg-fill-1 text-12px text-t-secondary'>
                  {index + 1}. {record.label}
                  {record.detail ? ` · ${record.detail}` : ''}
                </div>
              )
            )}
          </div>
        )}
      </div>
    </Modal>
  );
};

export default SessionNotebookModal;
