import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { MemoryRouter } from 'react-router';
import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import englishTools from '@/renderer/services/i18n/locales/en-US/tools.json';
import chineseTools from '@/renderer/services/i18n/locales/zh-CN/tools.json';
import '@arco-design/web-react/es/_util/react-19-adapter';
import '@arco-design/web-react/dist/css/arco.css';
import 'uno.css';
import '@/renderer/styles/themes/index.css';
import '@/renderer/styles/workspace-theme.css';
import '@/renderer/pages/conversation/Messages/components/MessageToolGroupSummary.css';
import type { NormalizedToolCall } from '@/common/chat/normalizeToolCall';
import ToolOperationDetail from '@/renderer/pages/conversation/Messages/components/ToolOperationDetail';

await i18n.use(initReactI18next).init({
  lng: 'zh-CN',
  resources: {
    'zh-CN': { translation: { tools: chineseTools } },
    'en-US': { translation: { tools: englishTools } },
  },
  interpolation: { escapeValue: false },
});

function PhaseLifecycle() {
  const [status, setStatus] = useState<NormalizedToolCall['status']>('running');
  const item: NormalizedToolCall = {
    key: 'phase-lifecycle',
    name: 'manage_environments',
    status,
    input: JSON.stringify({ mode: 'create', packages: ['runtime-module'] }),
    progress: {
      phase: 'installing_packages',
      completedItems: 1,
      totalItems: 8,
      elapsedMs: 300_000,
      indeterminate: true,
    },
    output:
      status === 'error'
        ? JSON.stringify({
            ok: false,
            status: 'failed',
            failure: {
              category: 'installation_failed',
              diagnostic_tail: 'transfer failed: short body\n/tmp/private/build.log\napi_key=sk-fixture-secret123456',
            },
          })
        : undefined,
  };
  return (
    <main style={{ padding: 20 }}>
      <button onClick={() => setStatus('error')}>Fail operation</button>
      <button onClick={() => setStatus('running')}>Show running</button>
      <button onClick={() => void i18n.changeLanguage('en-US')}>English</button>
      <ToolOperationDetail item={item} />
    </main>
  );
}

createRoot(document.getElementById('root')!).render(
  <MemoryRouter>
    <PhaseLifecycle />
  </MemoryRouter>
);
