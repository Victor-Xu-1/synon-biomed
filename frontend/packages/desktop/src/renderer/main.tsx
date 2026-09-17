/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

// Browser adapter setup
import '@/common/adapter/browser';

// React and core dependencies
import type { PropsWithChildren } from 'react';
import React, { useEffect } from 'react';
import { createRoot } from 'react-dom/client';

// Context providers
import { AuthProvider } from './hooks/context/AuthContext';
import { ThemeProvider } from './hooks/context/ThemeContext';

// Arco Design
import { ConfigProvider, Modal, Typography } from '@arco-design/web-react';
// Configure Arco Design to use React 18's createRoot, fixing Message component's CopyReactDOM.render error
import '@arco-design/web-react/es/_util/react-19-adapter';
import '@arco-design/web-react/dist/css/arco.css';
import enUS from '@arco-design/web-react/es/locale/en-US';
import zhCN from '@arco-design/web-react/es/locale/zh-CN';
import { useTranslation } from 'react-i18next';

// Styles
import 'uno.css';
import './styles/arco-override.css';
import './styles/themes/index.css';
import './styles/markdown.css';
import './styles/workspace-theme.css';

// i18n
import './services/i18n';
import { registerPwa } from './services/registerPwa';

import { bootstrapRendererConfig } from '@renderer/services/bootstrapRenderer';
import { installWebChunkRecovery } from '@renderer/services/webChunkRecovery';

import Router from './components/layout/Router';
import HOC from './utils/ui/HOC';
import { scheduleReactRootCleanup } from './utils/ui/reactRootLifecycle';
import type { BackendStartupFailureInfo } from '@/common/types/platform/webRuntime';

// Configuration is a stale-while-revalidate input to the renderer, not a boot
// dependency. Theme, language, and font consumers all have safe synchronous
// defaults and subscribe to the same ConfigService cache. Starting the request
// here preserves the early fetch without hiding the login or application shell
// behind a network request.
const disposeWebChunkRecovery = installWebChunkRecovery();
window.addEventListener('pagehide', disposeWebChunkRecovery, { once: true });
void bootstrapRendererConfig();

const arcoLocales: Record<string, typeof enUS> = {
  'zh-CN': zhCN,
  'en-US': enUS,
};

const AppProviders: React.FC<PropsWithChildren> = ({ children }) =>
  React.createElement(AuthProvider, null, React.createElement(ThemeProvider, null, children));

const Config: React.FC<PropsWithChildren> = ({ children }) => {
  const {
    i18n: { language },
  } = useTranslation();
  const arcoLocale = arcoLocales[language] ?? enUS;

  useEffect(() => {
    document.documentElement.lang = language;
  }, [language]);

  return React.createElement(ConfigProvider, { theme: { primaryColor: '#4e4e4a' }, locale: arcoLocale }, children);
};

const Main = () => <Router />;

const App = HOC.Wrapper(Config)(Main);

const BackendStartupFailureDialog: React.FC<{ failure: BackendStartupFailureInfo }> = ({ failure }) => {
  const { t } = useTranslation();
  const title = t('common.backendStartup.synonBiomed.title');
  const description = failure.message || t('common.backendStartup.synonBiomed.description');

  return (
    <div className='min-h-screen bg-1'>
      <Modal visible closable={false} maskClosable={false} footer={null} title={title}>
        <div className='text-t-1'>
          <Typography.Paragraph className='mb-0 text-t-secondary'>{description}</Typography.Paragraph>
        </div>
      </Modal>
    </div>
  );
};

void registerPwa();

const root = createRoot(document.getElementById('root')!);
const backendStartupFailure = window.__backendStartupFailure;
if (backendStartupFailure?.reason === 'synon_biomed_backend_unavailable') {
  root.render(
    <Config>
      <BackendStartupFailureDialog failure={backendStartupFailure} />
    </Config>
  );
} else {
  root.render(
    <AppProviders>
      <App />
    </AppProviders>
  );

  let cleanedUp = false;
  const cleanup = () => {
    if (cleanedUp) return;
    cleanedUp = true;
    root.unmount();
  };
  window.addEventListener('pagehide', cleanup, { once: true });
  const hot = (import.meta as ImportMeta & { hot?: { dispose: (callback: () => void) => void } }).hot;
  hot?.dispose(() => {
    window.removeEventListener('pagehide', cleanup);
    window.removeEventListener('pagehide', disposeWebChunkRecovery);
    disposeWebChunkRecovery();
    // Vite may dispose this entry while React is in the middle of a commit.
    // Unmounting synchronously races that commit and can surface a transient
    // removeChild error overlay during route changes/HMR.
    scheduleReactRootCleanup(cleanup);
  });
}
