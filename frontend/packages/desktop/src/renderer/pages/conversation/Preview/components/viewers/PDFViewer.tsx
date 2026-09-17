/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import { isElectronDesktop } from '@/renderer/utils/platform';
import { buildNativePdfPreviewSrc, buildPdfSrc } from '../../previewUrls';
import { usePreviewToolbarExtras } from '../../context/PreviewToolbarExtrasContext';
import PreviewLoadingState from '@/renderer/components/media/PreviewLoadingState';
import { Button, Message } from '@arco-design/web-react';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

interface PDFPreviewProps {
  /**
   * PDF file path (absolute path on disk)
   * PDF 文件路径（磁盘上的绝对路径）
   */
  file_path?: string;
  /**
   * PDF content as base64 or blob URL
   * PDF 内容（base64 或 blob URL）
   */
  content?: string;
  hideToolbar?: boolean;
}

// Electron webview 元素的类型定义 / Type definition for Electron webview element
interface ElectronWebView extends HTMLElement {
  src: string;
}

const PDFPreview: React.FC<PDFPreviewProps> = ({ file_path, content, hideToolbar = false }) => {
  const { t } = useTranslation();
  const isDesktop = isElectronDesktop();
  const [errorCode, setErrorCode] = useState<'pathMissing' | 'loadFailed' | null>(null);
  const [loading, setLoading] = useState(true);
  const webviewRef = useRef<ElectronWebView>(null);
  const [messageApi, messageContextHolder] = Message.useMessage();
  const toolbarExtrasContext = usePreviewToolbarExtras();
  const usePortalToolbar = Boolean(toolbarExtrasContext) && !hideToolbar;
  const pdfSource = useMemo(() => {
    try {
      return { src: buildPdfSrc(file_path, content), failed: false };
    } catch {
      return { src: '', failed: true };
    }
  }, [content, file_path]);
  const displayedErrorCode = pdfSource.failed ? 'loadFailed' : errorCode;
  const browserPreviewSrc = useMemo(() => buildNativePdfPreviewSrc(pdfSource.src), [pdfSource.src]);

  const handleOpenInSystem = useCallback(async () => {
    if (!file_path) {
      messageApi.error(t('preview.errors.openWithoutPath'));
      return;
    }

    try {
      await ipcBridge.shell.openFile.invoke(file_path);
      messageApi.success(t('preview.openInSystemSuccess'));
    } catch {
      messageApi.error(t('preview.openInSystemFailed'));
    }
  }, [file_path, messageApi, t]);

  useEffect(() => {
    setLoading(true);
    setErrorCode(null);

    if (!file_path && !content) {
      setErrorCode('pathMissing');
      setLoading(false);
      return;
    }

    if (pdfSource.failed || !pdfSource.src) {
      setErrorCode('loadFailed');
      setLoading(false);
      return;
    }

    // Browser builds use the native PDF renderer, framed to a readable scale by
    // browserPreviewSrc. Electron uses the webview lifecycle below because it
    // exposes did-finish-load/did-fail-load rather than iframe load events.
    if (!isDesktop) return;

    const webview = webviewRef.current;
    if (!webview) {
      setErrorCode('loadFailed');
      setLoading(false);
      return;
    }

    const handleLoad = () => {
      setLoading(false);
    };
    const handleError = () => {
      setErrorCode('loadFailed');
      setLoading(false);
    };

    webview.addEventListener('did-finish-load', handleLoad);
    webview.addEventListener('did-fail-load', handleError);

    return () => {
      webview.removeEventListener('did-finish-load', handleLoad);
      webview.removeEventListener('did-fail-load', handleError);
    };
  }, [content, file_path, isDesktop, pdfSource.failed, pdfSource.src]);

  // 设置工具栏扩展（必须在所有条件返回之前调用）
  // Set toolbar extras (must be called before any conditional returns)
  useEffect(() => {
    if (!usePortalToolbar || !toolbarExtrasContext || loading || displayedErrorCode) return;
    toolbarExtrasContext.setExtras({
      left: (
        <div className='flex items-center gap-8px'>
          <span className='text-13px text-t-secondary'>📄 {t('preview.pdf.title')}</span>
          <span className='text-11px text-t-tertiary'>{t('preview.readOnlyLabel')}</span>
        </div>
      ),
      right: null,
    });
    return () => toolbarExtrasContext.setExtras(null);
  }, [displayedErrorCode, loading, t, toolbarExtrasContext, usePortalToolbar]);

  return (
    <div className='h-full w-full bg-1 flex flex-col'>
      {messageContextHolder}
      {!usePortalToolbar && !hideToolbar && !loading && !displayedErrorCode && (
        <div className='flex items-center justify-between h-40px px-12px bg-2 flex-shrink-0'>
          <div className='flex items-center gap-8px'>
            <span className='text-13px text-t-secondary'>📄 {t('preview.pdf.title')}</span>
            <span className='text-11px text-t-tertiary'>{t('preview.readOnlyLabel')}</span>
          </div>
          {file_path && (
            <Button size='mini' type='text' onClick={handleOpenInSystem} title={t('preview.openInSystemApp')}>
              <svg width='14' height='14' viewBox='0 0 24 24' fill='none' stroke='currentColor' strokeWidth='2'>
                <path d='M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6' />
                <polyline points='15 3 21 3 21 9' />
                <line x1='10' y1='14' x2='21' y2='3' />
              </svg>
              <span>{t('preview.openInSystemApp')}</span>
            </Button>
          )}
        </div>
      )}
      {/* PDF 内容区域 / PDF content area */}
      <div className='relative flex-1 overflow-hidden bg-1'>
        {/* key 确保文件路径改变时 webview 重新挂载 / key ensures webview remounts when file path changes */}
        {pdfSource.src && isDesktop && (
          <webview
            key={pdfSource.src}
            ref={webviewRef}
            src={pdfSource.src}
            className='w-full h-full'
            style={{ display: 'inline-flex' }}
          />
        )}
        {pdfSource.src && !isDesktop && (
          <iframe
            key={browserPreviewSrc}
            src={browserPreviewSrc}
            className='size-full border-0'
            title={t('preview.pdf.title')}
            data-testid='pdf-browser-preview'
            onLoad={() => setLoading(false)}
            onError={() => {
              setErrorCode('loadFailed');
              setLoading(false);
            }}
          />
        )}
        {loading && !displayedErrorCode && (
          <div className='absolute inset-0 bg-1'>
            <PreviewLoadingState label={t('preview.pdf.loading')} />
          </div>
        )}
        {displayedErrorCode && (
          <div className='absolute inset-0 flex items-center justify-center bg-1 px-24px'>
            <div className='text-center'>
              <div className='mb-8px text-16px text-t-error'>
                {t(displayedErrorCode === 'pathMissing' ? 'preview.pdf.pathMissing' : 'preview.pdf.loadFailed')}
              </div>
              <div className='text-12px text-t-secondary'>{t('preview.pdf.unableDisplay')}</div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
};

export default PDFPreview;
