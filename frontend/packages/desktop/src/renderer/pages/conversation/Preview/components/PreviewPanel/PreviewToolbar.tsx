/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { PreviewHistoryTarget } from '@/common/types/office/preview';
import { Dropdown, Menu } from '@arco-design/web-react';
import React from 'react';
import { useTranslation } from 'react-i18next';
import { shouldShowDownload } from './previewToolbarUtils';

interface PreviewToolbarProps {
  content_type: string;
  file_name?: string;
  canEdit: boolean;
  isEditing: boolean;
  isDirty: boolean;
  isSaving: boolean;
  isFullscreen: boolean;
  showOpenInSystemButton: boolean;
  historyTarget: PreviewHistoryTarget | null;
  onEdit: () => void;
  onCancelEdit: () => void;
  onSaveEdit: () => void;
  onFullscreenToggle: () => void;
  onOpenInSystem: () => void;
  onDownload: () => void;
  onClose: () => void;
  inspectMode?: boolean;
  onInspectModeToggle?: () => void;
  regionCommentMode?: boolean;
  onRegionCommentModeToggle?: () => void;
  leftExtra?: React.ReactNode;
  rightExtra?: React.ReactNode;
  tabs?: PreviewToolbarTab[];
  activeTabId?: string | null;
  onSwitchTab?: (tabId: string) => void;
}

export interface PreviewToolbarTab {
  id: string;
  title: string;
  isDirty?: boolean;
}

type ToolbarButtonProps = {
  label: string;
  onClick: () => void;
  children: React.ReactNode;
  pressed?: boolean;
  disabled?: boolean;
  className?: string;
};

const ToolbarButton = React.forwardRef<HTMLButtonElement, ToolbarButtonProps>(function ToolbarButton(
  { label, onClick, children, pressed, disabled, className = '' },
  ref
) {
  return (
    <button
      ref={ref}
      type='button'
      aria-label={label}
      title={label}
      aria-pressed={pressed}
      disabled={disabled}
      onClick={onClick}
      className={`size-30px inline-flex shrink-0 items-center justify-center border-0 rd-6px bg-transparent text-t-secondary transition-colors hover:bg-fill-2 hover:text-t-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand disabled:cursor-not-allowed disabled:opacity-40 ${className}`}
    >
      {children}
    </button>
  );
});

const PreviewToolbar: React.FC<PreviewToolbarProps> = ({
  content_type,
  file_name,
  canEdit,
  isEditing,
  isDirty,
  isSaving,
  isFullscreen,
  showOpenInSystemButton,
  historyTarget,
  onEdit,
  onCancelEdit,
  onSaveEdit,
  onFullscreenToggle,
  onOpenInSystem,
  onDownload,
  onClose,
  inspectMode,
  onInspectModeToggle,
  regionCommentMode,
  onRegionCommentModeToggle,
  leftExtra,
  rightExtra,
  tabs = [],
  activeTabId,
  onSwitchTab,
}) => {
  const { t } = useTranslation();
  const showDownload = shouldShowDownload(content_type, showOpenInSystemButton);
  const saveLabel = historyTarget?.artifact_id ? t('preview.saveAsNewVersion') : t('preview.saveChanges');
  const showFileSwitcher = tabs.length > 1 && Boolean(onSwitchTab);
  const fileIdentity = (
    <span className='min-w-0 truncate text-13px font-600 text-t-primary' title={file_name}>
      {file_name}
    </span>
  );
  const fileSwitcher = showFileSwitcher ? (
    <Dropdown
      trigger='click'
      position='bl'
      droplist={
        <Menu className='min-w-180px max-w-320px'>
          {tabs.map((tab) => (
            <Menu.Item key={tab.id} onClick={() => onSwitchTab?.(tab.id)}>
              <span className='flex min-w-0 items-center gap-8px'>
                <span className='min-w-0 flex-1 truncate'>{tab.title}</span>
                {tab.isDirty ? <span className='size-6px shrink-0 rd-full bg-brand' /> : null}
                {tab.id === activeTabId ? <CheckIcon /> : null}
              </span>
            </Menu.Item>
          ))}
        </Menu>
      }
    >
      <button
        type='button'
        aria-label={t('preview.currentFile')}
        title={file_name}
        className='min-w-0 max-w-full flex items-center gap-5px border-0 rd-6px bg-transparent px-0 py-3px text-left hover:text-brand focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand'
      >
        {fileIdentity}
        <ChevronIcon />
      </button>
    </Dropdown>
  ) : (
    fileIdentity
  );
  return (
    <div
      data-testid='preview-unified-toolbar'
      className='h-46px flex shrink-0 items-center justify-between gap-12px border-b border-arco-1 bg-1 px-12px'
    >
      <div className='min-w-0 flex flex-1 items-center gap-8px overflow-hidden'>
        {fileSwitcher}
        {isEditing ? <span className='text-11px text-t-tertiary'>{t('preview.editing')}</span> : null}
        {leftExtra}
      </div>

      <div className='flex shrink-0 items-center gap-4px'>
        {rightExtra}
        {isEditing ? (
          <>
            <button
              type='button'
              onClick={onCancelEdit}
              className='h-30px border border-arco-2 rd-6px bg-transparent px-12px text-12px text-t-primary hover:bg-fill-2 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand'
            >
              {t('common.cancel')}
            </button>
            <button
              type='button'
              disabled={!isDirty || isSaving}
              onClick={onSaveEdit}
              className='h-30px border-0 rd-6px bg-brand px-12px text-12px font-600 text-white hover:bg-brand-hover focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand disabled:cursor-not-allowed disabled:opacity-45'
            >
              {isSaving ? t('preview.saving') : saveLabel}
            </button>
          </>
        ) : (
          <>
            {canEdit ? (
              <ToolbarButton label={t('preview.edit')} onClick={onEdit}>
                <PencilIcon />
              </ToolbarButton>
            ) : null}
            {showOpenInSystemButton ? (
              <ToolbarButton label={t('preview.openInSystemApp')} onClick={onOpenInSystem}>
                <ExternalIcon />
              </ToolbarButton>
            ) : null}
            {onInspectModeToggle ? (
              <ToolbarButton
                label={inspectMode ? t('preview.html.inspectElementDisable') : t('preview.html.inspectElementEnable')}
                onClick={onInspectModeToggle}
                pressed={inspectMode}
              >
                <InspectIcon />
              </ToolbarButton>
            ) : null}
            {onRegionCommentModeToggle ? (
              <button
                type='button'
                className={`preview-toolbar__region-comment${regionCommentMode ? ' is-active' : ''}`}
                aria-label={
                  regionCommentMode ? t('preview.regionComment.finishMode') : t('preview.regionComment.startMode')
                }
                title={regionCommentMode ? t('preview.regionComment.finishMode') : t('preview.regionComment.startMode')}
                aria-pressed={regionCommentMode}
                onClick={onRegionCommentModeToggle}
              >
                <RegionCommentIcon />
                {regionCommentMode ? <span>{t('preview.regionComment.active')}</span> : null}
              </button>
            ) : null}
            <ToolbarButton
              label={isFullscreen ? t('preview.exitFullscreen') : t('preview.openFullscreen')}
              onClick={onFullscreenToggle}
              pressed={isFullscreen}
            >
              <FullscreenIcon active={isFullscreen} />
            </ToolbarButton>
            {showDownload ? (
              <ToolbarButton label={t('preview.downloadFile')} onClick={onDownload}>
                <DownloadIcon />
              </ToolbarButton>
            ) : null}
            <ToolbarButton label={t('preview.closePreview')} onClick={onClose}>
              <CloseIcon />
            </ToolbarButton>
          </>
        )}
      </div>
    </div>
  );
};

const PencilIcon = () => (
  <svg width='16' height='16' viewBox='0 0 24 24' fill='none' stroke='currentColor' strokeWidth='1.8'>
    <path d='m4 20 4.2-1 10.9-10.9a2.1 2.1 0 0 0-3-3L5.2 16 4 20Z' />
    <path d='m14.8 6.4 3 3' />
  </svg>
);
const ExternalIcon = () => (
  <svg width='16' height='16' viewBox='0 0 24 24' fill='none' stroke='currentColor' strokeWidth='1.8'>
    <path d='M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6' />
    <path d='M15 3h6v6M10 14 21 3' />
  </svg>
);
const InspectIcon = () => (
  <svg width='16' height='16' viewBox='0 0 24 24' fill='none' stroke='currentColor' strokeWidth='1.8'>
    <path d='m3 3 7.1 17 2.5-7.4L20 10 3 3Z' />
    <path d='m13 13 6 6' />
  </svg>
);
const RegionCommentIcon = () => (
  <svg width='16' height='16' viewBox='0 0 24 24' fill='none' stroke='currentColor' strokeWidth='1.8'>
    <path d='M7 18.5 3.5 21l1-4.2A8.5 8.5 0 1 1 7 18.5Z' />
    <path d='M8 9h8M8 13h5' />
  </svg>
);
const FullscreenIcon: React.FC<{ active: boolean }> = ({ active }) => (
  <svg width='16' height='16' viewBox='0 0 24 24' fill='none' stroke='currentColor' strokeWidth='1.8'>
    {active ? (
      <>
        <path d='M8 3v5H3M16 3v5h5M8 21v-5H3M16 21v-5h5' />
      </>
    ) : (
      <>
        <path d='M8 3H3v5M16 3h5v5M8 21H3v-5M16 21h5v-5' />
      </>
    )}
  </svg>
);
const DownloadIcon = () => (
  <svg width='16' height='16' viewBox='0 0 24 24' fill='none' stroke='currentColor' strokeWidth='1.8'>
    <path d='M12 3v12m0 0 5-5m-5 5-5-5M5 21h14' />
  </svg>
);
const CloseIcon = () => (
  <svg width='16' height='16' viewBox='0 0 24 24' fill='none' stroke='currentColor' strokeWidth='1.8'>
    <path d='m6 6 12 12M18 6 6 18' />
  </svg>
);
const ChevronIcon = () => (
  <svg width='12' height='12' viewBox='0 0 24 24' fill='none' stroke='currentColor' strokeWidth='2'>
    <path d='m7 10 5 5 5-5' />
  </svg>
);
const CheckIcon = () => (
  <svg width='14' height='14' viewBox='0 0 24 24' fill='none' stroke='currentColor' strokeWidth='2'>
    <path d='m5 12 4 4L19 6' />
  </svg>
);

export default PreviewToolbar;
