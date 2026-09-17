import { Input, Modal } from '@arco-design/web-react';
import React from 'react';
import { useTranslation } from 'react-i18next';
import { validateDocumentResourceSource } from './documentResourceModel';

export type DocumentInsertDialog = { kind: 'link' | 'image'; source: string; label: string } | null;

type DocumentResourceDialogProps = {
  value: DocumentInsertDialog;
  replacingImage: boolean;
  onChange: (value: Exclude<DocumentInsertDialog, null>) => void;
  onConfirm: () => void;
  onCancel: () => void;
};

const DocumentResourceDialog: React.FC<DocumentResourceDialogProps> = ({
  value,
  replacingImage,
  onChange,
  onConfirm,
  onCancel,
}) => {
  const { t } = useTranslation();
  const validation = value ? validateDocumentResourceSource(value.source, value.kind) : null;
  const validationError = value?.source.trim() && validation && 'reason' in validation ? validation.reason : null;

  return (
    <Modal
      visible={Boolean(value)}
      title={t(
        value?.kind === 'link'
          ? 'preview.documentEditor.insertLink'
          : replacingImage
            ? 'preview.documentEditor.replaceImage'
            : 'preview.documentEditor.insertImage'
      )}
      okText={t('common.confirm')}
      cancelText={t('common.cancel')}
      okButtonProps={{ disabled: !validation?.valid }}
      onOk={onConfirm}
      onCancel={onCancel}
    >
      {value ? (
        <div className='flex flex-col gap-12px'>
          <Input
            aria-label={t('preview.documentEditor.resourceUrl')}
            placeholder={t('preview.documentEditor.resourceUrl')}
            value={value.source}
            onChange={(source) => onChange({ ...value, source })}
          />
          <Input
            aria-label={t(
              value.kind === 'link' ? 'preview.documentEditor.linkText' : 'preview.documentEditor.imageAlt'
            )}
            placeholder={t(
              value.kind === 'link' ? 'preview.documentEditor.linkText' : 'preview.documentEditor.imageAlt'
            )}
            value={value.label}
            onChange={(label) => onChange({ ...value, label })}
          />
          {validationError ? (
            <div className='document-wysiwyg-editor__dialog-error' role='alert'>
              {t(`preview.documentEditor.resourceError.${validationError}`)}
            </div>
          ) : null}
        </div>
      ) : null}
    </Modal>
  );
};

export default DocumentResourceDialog;
