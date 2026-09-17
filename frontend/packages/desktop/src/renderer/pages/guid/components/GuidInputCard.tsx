/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import FilePreview from '@/renderer/components/media/FilePreview';
import UploadProgressBar from '@/renderer/components/media/UploadProgressBar';
import ComposerContextChips from '@/renderer/components/chat/SendBox/ComposerContextChips';
import type { ComposerContextItem } from '@/renderer/components/chat/SendBox/composerCompositionModel';
import { useLayoutContext } from '@/renderer/hooks/context/LayoutContext';
import { useCompositionInput } from '@/renderer/hooks/chat/useCompositionInput';
import { Input } from '@arco-design/web-react';
import { Close, Paperclip } from '@icon-park/react';
import React from 'react';
import styles from '../index.module.css';
import type { GuidLocalFile } from '../hooks/useGuidInput';

const formatLocalFileSize = (bytes: number): string => {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
};

const getLocalFileExtension = (fileName: string): string => {
  const extension = fileName.split('.').pop()?.trim();
  return extension ? extension.toUpperCase() : 'FILE';
};

const GuidLocalFilePreview: React.FC<{ attachment: GuidLocalFile; onRemove: () => void }> = ({
  attachment,
  onRemove,
}) => (
  <div
    className='relative inline-flex h-60px min-w-0 max-w-240px items-center gap-10px rounded-10px border border-solid bg-fill-1 px-10px transition-colors hover:bg-fill-2'
    data-testid='guid-local-file'
    data-file-name={attachment.file.name}
    style={{ borderColor: 'color-mix(in srgb, var(--color-border-2) 70%, transparent)' }}
  >
    <div
      className='flex h-36px w-36px shrink-0 items-center justify-center rounded-8px bg-fill-2 text-t-secondary'
      data-testid='guid-local-file-icon'
      aria-hidden='true'
    >
      <Paperclip theme='outline' size='17' fill='var(--text-secondary)' aria-hidden='true' />
    </div>
    <div className='min-w-0 leading-16px'>
      <div className='truncate text-13px font-500 text-t-primary' title={attachment.file.name}>
        {attachment.file.name}
      </div>
      <div className='flex items-center gap-4px text-12px text-t-secondary'>
        <span data-file-extension={getLocalFileExtension(attachment.file.name)}>
          {getLocalFileExtension(attachment.file.name)}
        </span>
        <span aria-hidden='true'>·</span>
        <span>{formatLocalFileSize(attachment.file.size)}</span>
      </div>
    </div>
    <button
      type='button'
      className='absolute -right-6px -top-6px flex h-16px w-16px items-center justify-center rounded-50% border border-solid bg-fill-1 text-t-secondary transition-colors hover:bg-fill-2 hover:text-t-primary'
      aria-label={`Remove ${attachment.file.name}`}
      style={{ borderColor: 'color-mix(in srgb, var(--color-border-2) 70%, transparent)' }}
      onClick={(event) => {
        event.stopPropagation();
        onRemove();
      }}
    >
      <Close theme='filled' size='10' />
    </button>
  </div>
);

type GuidInputCardProps = {
  // Input state
  input: string;
  onInputChange: (value: string) => void;
  onKeyDown: (event: React.KeyboardEvent) => void;
  onPaste: React.ClipboardEventHandler;
  onFocus: () => void;
  onBlur: () => void;
  placeholder: string;

  // Styling
  isInputActive: boolean;
  isFileDragging: boolean;
  activeBorderColor: string;
  inactiveBorderColor: string;
  activeShadow: string;
  inactiveShadow: string;
  surfaceBackgroundColor: string;
  dragHandlers: React.HTMLAttributes<HTMLDivElement>;

  // Files
  files: string[];
  localFiles?: GuidLocalFile[];
  contextItems?: ComposerContextItem[];
  onRemoveFile: (path: string) => void;
  onRemoveLocalFile?: (id: string) => void;
  onRemoveContextItem?: (key: string) => void;

  // Action row
  actionRow: React.ReactNode;
  slashCommandMenu?: React.ReactNode;
};

const GuidInputCard: React.FC<GuidInputCardProps> = ({
  input,
  onInputChange,
  onKeyDown,
  onPaste,
  onFocus,
  onBlur,
  placeholder,
  isInputActive,
  isFileDragging,
  activeBorderColor,
  inactiveBorderColor,
  activeShadow,
  inactiveShadow,
  surfaceBackgroundColor,
  dragHandlers,
  files,
  localFiles = [],
  contextItems = [],
  onRemoveFile,
  onRemoveLocalFile = () => {},
  onRemoveContextItem = () => {},
  actionRow,
  slashCommandMenu,
}) => {
  const layout = useLayoutContext();
  const isMobile = layout?.isMobile ?? false;
  const { compositionHandlers, isComposing } = useCompositionInput();
  const textareaAutoSize = isMobile ? { minRows: 1, maxRows: 8 } : { minRows: 1, maxRows: 12 };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (isComposing.current) return;
    onKeyDown(e);
  };

  return (
    <div
      className={`${styles.guidInputCardWrap} guid-input-card-shell relative flex flex-col ${slashCommandMenu ? 'overflow-visible' : 'overflow-hidden'} ${isFileDragging ? 'guid-input-card-shell--dragging' : ''}`}
      data-testid='guid-composer'
      style={
        {
          zIndex: 1,
          '--guid-composer-border-color': inactiveBorderColor,
          '--guid-composer-active-border-color': activeBorderColor,
          '--guid-composer-active-shadow': activeShadow,
          '--guid-composer-shadow': inactiveShadow,
          '--guid-composer-surface': surfaceBackgroundColor,
          ...(isFileDragging
            ? {
                backgroundColor: 'var(--color-primary-light-1)',
              }
            : {}),
        } as React.CSSProperties
      }
      {...dragHandlers}
    >
      <div className={`${styles.guidInputInner} relative p-16px flex flex-col`} data-input-active={isInputActive}>
        {contextItems.length > 0 ? (
          <div className='mb-10px'>
            <ComposerContextChips items={contextItems} onRemove={onRemoveContextItem} />
          </div>
        ) : null}
        <Input.TextArea
          autoSize={textareaAutoSize}
          placeholder={placeholder}
          spellCheck={false}
          className={`text-14px focus:b-none rounded-xl !bg-transparent !b-none !resize-none !py-0 !pr-0 !pl-7px ${styles.lightPlaceholder}`}
          value={input}
          onChange={onInputChange}
          onPaste={onPaste}
          onFocus={onFocus}
          onBlur={onBlur}
          {...compositionHandlers}
          onKeyDown={handleKeyDown}
          data-testid='guid-input'
        />
        <div className={styles.composerSpacer} aria-hidden='true' />
        {(files.length > 0 || localFiles.length > 0) && (
          <div className='flex flex-wrap items-center gap-8px mt-12px mb-12px'>
            {files.map((path) => (
              <FilePreview key={path} path={path} onRemove={() => onRemoveFile(path)} />
            ))}
            {localFiles.map((attachment) => (
              <GuidLocalFilePreview
                key={attachment.id}
                attachment={attachment}
                onRemove={() => onRemoveLocalFile(attachment.id)}
              />
            ))}
          </div>
        )}
        <UploadProgressBar source='sendbox' />
        {actionRow}
        {slashCommandMenu && (
          <div className='absolute left-0 right-0 top-[calc(100%+4px)] z-70'>{slashCommandMenu}</div>
        )}
      </div>
    </div>
  );
};

export default GuidInputCard;
