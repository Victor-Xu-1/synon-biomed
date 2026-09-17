import type { SynonBiomedProject, SynonBiomedProjectInput } from '@/renderer/services/synonBiomedGateway';
import { Button, Input, Modal } from '@arco-design/web-react';
import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';

type ProjectEditorModalProps = {
  visible: boolean;
  project?: SynonBiomedProject | null;
  loading?: boolean;
  errorMessage?: string | null;
  onCancel: () => void;
  onSubmit: (input: SynonBiomedProjectInput) => void;
};

const ProjectEditorModal: React.FC<ProjectEditorModalProps> = ({
  visible,
  project,
  loading = false,
  errorMessage,
  onCancel,
  onSubmit,
}) => {
  const { t } = useTranslation();
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [context, setContext] = useState('');
  const editing = Boolean(project);

  useEffect(() => {
    if (!visible) return;
    setName(project?.name ?? '');
    setDescription(project?.description ?? '');
    setContext(project?.context ?? '');
  }, [project, visible]);

  const submit = () => {
    const normalizedName = name.trim();
    if (!normalizedName || loading) return;
    onSubmit({
      name: normalizedName,
      description: description.trim() || null,
      context: context.trim() || null,
    });
  };

  return (
    <Modal
      visible={visible}
      title={editing ? t('conversation.history.projectEditor.editTitle') : t('conversation.history.projectsSection')}
      footer={null}
      onCancel={onCancel}
      unmountOnExit
      alignCenter
      getPopupContainer={() => document.body}
      style={{ width: 560, maxWidth: 'calc(100vw - 32px)', borderRadius: 8 }}
    >
      <form
        className='flex flex-col gap-18px pt-4px'
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <div>
          <label htmlFor='synon-project-name' className='block text-14px font-[500] text-t-primary mb-4px'>
            {t('conversation.history.projectEditor.nameLabel')}
          </label>
          <p className='m-0 mb-8px text-12px leading-18px text-t-tertiary'>
            {t('conversation.history.projectEditor.nameHelp')}
          </p>
          <Input
            id='synon-project-name'
            value={name}
            maxLength={255}
            autoFocus
            placeholder={t('conversation.history.projectEditor.namePlaceholder')}
            onChange={setName}
          />
        </div>

        <div>
          <label htmlFor='synon-project-description' className='block text-14px font-[500] text-t-primary mb-4px'>
            {t('conversation.history.projectEditor.descriptionLabel')}
          </label>
          <p className='m-0 mb-8px text-12px leading-18px text-t-tertiary'>
            {t('conversation.history.projectEditor.descriptionHelp')}
          </p>
          <textarea
            id='synon-project-description'
            value={description}
            rows={2}
            placeholder={t('conversation.history.projectEditor.descriptionPlaceholder')}
            className='w-full min-h-64px max-h-112px resize-y box-border px-11px py-7px rd-4px border border-solid border-[var(--color-border-3)] bg-[var(--color-bg-2)] text-14px leading-22px text-t-primary outline-none focus:border-[rgb(var(--primary-6))]'
            onChange={(event) => setDescription(event.target.value)}
          />
        </div>

        <div>
          <label htmlFor='synon-project-context' className='block text-14px font-[500] text-t-primary mb-4px'>
            {t('conversation.history.projectEditor.contextLabel')}
          </label>
          <p className='m-0 mb-8px text-12px leading-18px text-t-tertiary'>
            {t('conversation.history.projectEditor.contextHelp')}
          </p>
          <textarea
            id='synon-project-context'
            value={context}
            rows={3}
            placeholder={t('conversation.history.projectEditor.contextPlaceholder')}
            className='w-full min-h-86px max-h-176px resize-y box-border px-11px py-7px rd-4px border border-solid border-[var(--color-border-3)] bg-[var(--color-bg-2)] text-14px leading-22px text-t-primary outline-none focus:border-[rgb(var(--primary-6))]'
            onChange={(event) => setContext(event.target.value)}
          />
        </div>

        {errorMessage && <div className='text-12px leading-18px text-t-primary'>{errorMessage}</div>}

        <div className='flex justify-end gap-8px pt-2px'>
          <Button type='secondary' disabled={loading} onClick={onCancel}>
            {t('common.cancel')}
          </Button>
          <Button htmlType='button' type='primary' loading={loading} disabled={!name.trim()} onClick={submit}>
            {editing ? t('conversation.history.saveName') : t('conversation.history.createProjectAction')}
          </Button>
        </div>
      </form>
    </Modal>
  );
};

export default ProjectEditorModal;
