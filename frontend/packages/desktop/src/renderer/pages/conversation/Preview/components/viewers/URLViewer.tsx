/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React from 'react';
import { useTranslation } from 'react-i18next';
import WebviewHost from '@/renderer/components/media/WebviewHost';
import { resolveSynonBiomedInternetShortcut } from '@/renderer/services/synonBiomedArtifactPreview';

interface URLViewerProps {
  /** URL to display */
  url: string;
  /** Optional title for the page */
  title?: string;
}

/**
 * URL 预览组件 - 用于在应用内预览网页（对话框预览面板）
 * URL Preview component - for previewing web pages within the app (conversation preview panel)
 *
 * Delegates to the shared WebviewHost with navigation bar enabled.
 */
const URLViewer: React.FC<URLViewerProps> = ({ url, title }) => {
  const { t } = useTranslation();
  const resolvedUrl = resolveSynonBiomedInternetShortcut(url);
  if (!resolvedUrl) {
    return (
      <div className='size-full flex-center bg-1 px-24px'>
        <div role='alert' className='max-w-420px text-center text-13px leading-20px text-t-secondary'>
          {t('conversation.urlViewer.invalidUrl')}
        </div>
      </div>
    );
  }
  return <WebviewHost url={resolvedUrl} title={title} showNavBar className='bg-1' />;
};

export default URLViewer;
