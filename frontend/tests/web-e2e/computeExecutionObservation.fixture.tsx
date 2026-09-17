import React from 'react';
import { createRoot } from 'react-dom/client';
import { MemoryRouter } from 'react-router';
import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import '@arco-design/web-react/es/_util/react-19-adapter';
import '@arco-design/web-react/dist/css/arco.css';
import 'uno.css';
import '@/renderer/styles/themes/index.css';
import '@/renderer/styles/workspace-theme.css';
import conversation from '@/renderer/services/i18n/locales/zh-CN/conversation.json';
import SynonBiomedComputeRuntimePanel from '@/renderer/components/synonBiomed/runtime/SynonBiomedComputeRuntimePanel';

await i18n.use(initReactI18next).init({
  lng: 'zh-CN',
  resources: { 'zh-CN': { translation: { conversation } } },
  interpolation: { escapeValue: false },
});
createRoot(document.getElementById('root')!).render(
  <MemoryRouter>
    <SynonBiomedComputeRuntimePanel
      rootFrameId='observation-frame'
      projectId={null}
      sessionTitle='Execution observation integration'
    />
  </MemoryRouter>
);
