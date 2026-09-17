import { Button, Input } from '@arco-design/web-react';
import React from 'react';

type Props = {
  filename: string;
  pathPreview: string;
  loading: boolean;
  fileNameLabel: string;
  fileNamePlaceholder: string;
  pathLabel: string;
  cancelLabel: string;
  backLabel: string;
  saveLabel: string;
  onFilenameChange: (value: string) => void;
  onKeyDown: (event: React.KeyboardEvent) => void;
  onCancel: () => void;
  onBack: () => void;
  onSave: () => void;
};

const ConversationExportFileNamePanel = ({
  filename,
  pathPreview,
  loading,
  fileNameLabel,
  fileNamePlaceholder,
  pathLabel,
  cancelLabel,
  backLabel,
  saveLabel,
  onFilenameChange,
  onKeyDown,
  onCancel,
  onBack,
  onSave,
}: Props) => (
  <div
    className='rounded-14px border border-solid overflow-hidden p-12px flex flex-col gap-10px'
    style={{
      borderColor: 'var(--color-border-2)',
      background: 'color-mix(in srgb, var(--color-bg-1) 88%, transparent)',
      backdropFilter: 'blur(14px) saturate(1.1)',
      WebkitBackdropFilter: 'blur(14px) saturate(1.1)',
    }}
  >
    <div className='text-13px font-semibold text-t-primary'>{fileNameLabel}</div>
    <Input
      autoFocus
      value={filename}
      onChange={onFilenameChange}
      placeholder={fileNamePlaceholder}
      disabled={loading}
      onKeyDown={onKeyDown}
    />
    <div className='text-12px text-t-secondary break-all'>
      {pathLabel}: {pathPreview}
    </div>
    <div className='flex items-center justify-end gap-8px'>
      <Button size='small' type='secondary' disabled={loading} onClick={onCancel}>
        {cancelLabel}
      </Button>
      <Button size='small' type='secondary' disabled={loading} onClick={onBack}>
        {backLabel}
      </Button>
      <Button size='small' type='primary' loading={loading} onClick={onSave}>
        {saveLabel}
      </Button>
    </div>
  </div>
);

export default ConversationExportFileNamePanel;
