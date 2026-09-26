import '@arco-design/web-react/es/_util/react-19-adapter';
import '@arco-design/web-react/dist/css/arco.css';
import 'uno.css';
import '@/renderer/styles/themes/index.css';
import '@/renderer/styles/workspace-theme.css';
import React from 'react';
import { createRoot } from 'react-dom/client';
import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import enCommon from '@/renderer/services/i18n/locales/en-US/common.json';
import enConversation from '@/renderer/services/i18n/locales/en-US/conversation.json';
import zhCommon from '@/renderer/services/i18n/locales/zh-CN/common.json';
import zhConversation from '@/renderer/services/i18n/locales/zh-CN/conversation.json';
import ContextUsagePanel from '@/renderer/components/synonBiomed/runtime/ContextUsagePanel';

await i18n.use(initReactI18next).init({
  lng: 'zh-CN',
  resources: {
    'en-US': { translation: { common: enCommon, conversation: enConversation } },
    'zh-CN': { translation: { common: zhCommon, conversation: zhConversation } },
  },
  interpolation: { escapeValue: false },
});

createRoot(document.getElementById('root')!).render(
  <main style={{ position: 'fixed', right: 64, bottom: 110 }}>
    <ContextUsagePanel conversationId='browser-context-usage' />
  </main>
);
