/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { PreviewHistoryTarget, PreviewSnapshotInfo } from '@/common/types/office/preview';
import React from 'react';
import { useTranslation } from 'react-i18next';

/**
 * PreviewHistoryDropdown 组件属性
 * PreviewHistoryDropdown component props
 */
interface PreviewHistoryDropdownProps {
  /**
   * 历史版本列表
   * History versions list
   */
  historyVersions: PreviewSnapshotInfo[];

  /**
   * 是否正在加载
   * Whether loading
   */
  historyLoading: boolean;

  /**
   * 加载错误信息
   * Loading error message
   */
  historyError: boolean;

  /**
   * 历史目标
   * History target
   */
  historyTarget: PreviewHistoryTarget | null;

  /**
   * 当前主题
   * Current theme
   */
  currentTheme: 'light' | 'dark';

  /**
   * 选择快照回调
   * Select snapshot callback
   */
  onSnapshotSelect: (snapshot: PreviewSnapshotInfo) => void;
}

/**
 * 预览历史下拉菜单组件
 * Preview history dropdown menu component
 *
 * 显示历史版本列表，支持选择历史版本恢复内容
 * Displays history versions list, supports selecting history versions to restore content
 */
const PreviewHistoryDropdown: React.FC<PreviewHistoryDropdownProps> = ({
  historyVersions,
  historyLoading,
  historyError,
  historyTarget,
  currentTheme,
  onSnapshotSelect,
}) => {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage || i18n.language;
  const formatSnapshotDate = (timestamp: number) => {
    const date = new Date(timestamp);
    if (Number.isNaN(date.getTime())) return t('preview.unknownDate');
    return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(date);
  };
  const formatSnapshotSize = (bytes: number) => {
    const kilobytes = Number.isFinite(bytes) ? Math.max(0, bytes) / 1024 : 0;
    return t('preview.sizeKilobytes', {
      size: new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(kilobytes),
    });
  };

  return (
    <div
      className='min-w-220px rd-6px shadow-lg'
      style={{
        backgroundColor: currentTheme === 'dark' ? '#1d1d1f' : '#ffffff',
        border: '1px solid var(--border-base, #e5e6eb)',
        zIndex: 9999,
      }}
    >
      {/* 头部：历史版本标题 + 文件名 / Header: History title + filename */}
      <div className='px-8px py-6px' style={{ borderColor: 'var(--border-base, #e5e6eb)' }}>
        <div className='text-12px text-t-secondary'>{t('preview.historyVersions')}</div>
        <div className='text-11px text-t-tertiary truncate'>
          {historyTarget?.file_name || historyTarget?.title || t('preview.currentFile')}
        </div>
      </div>

      {/* 列表内容：固定高度可滚动 / List content: fixed height scrollable */}
      <div className='overflow-y-auto' style={{ maxHeight: '240px' }}>
        {historyLoading ? (
          <div className='py-16px text-center text-12px text-t-secondary'>{t('preview.loading')}</div>
        ) : historyError ? (
          <div className='py-16px text-center text-12px text-t-primary'>{t('preview.loadHistoryFailed')}</div>
        ) : historyVersions.length === 0 ? (
          <div className='py-16px text-center text-12px text-t-secondary'>{t('preview.noHistory')}</div>
        ) : (
          historyVersions.map((snapshot) => (
            <div
              key={snapshot.id}
              className='px-12px py-8px cursor-pointer hover:bg-2 transition-colors'
              onClick={() => onSnapshotSelect(snapshot)}
            >
              <div className='text-12px text-t-primary'>{formatSnapshotDate(snapshot.created_at)}</div>
              <div className='text-11px text-t-tertiary'>{formatSnapshotSize(snapshot.size)}</div>
            </div>
          ))
        )}
      </div>
    </div>
  );
};

export default PreviewHistoryDropdown;
