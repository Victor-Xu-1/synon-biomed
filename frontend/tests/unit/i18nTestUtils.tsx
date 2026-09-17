import { render, type RenderOptions, type RenderResult } from '@testing-library/react';
import i18next, { type i18n } from 'i18next';
import React from 'react';
import { I18nextProvider } from 'react-i18next';
import enResources from '@/renderer/services/i18n/locales/en-US';
import zhResources from '@/renderer/services/i18n/locales/zh-CN';

export type TestLanguage = 'zh-CN' | 'en-US';

export async function createTestI18n(language: TestLanguage = 'zh-CN'): Promise<i18n> {
  const instance = i18next.createInstance();
  await instance.init({
    lng: language,
    fallbackLng: 'zh-CN',
    resources: {
      'zh-CN': {
        translation: zhResources,
      },
      'en-US': {
        translation: enResources,
      },
    },
    interpolation: { escapeValue: false },
  });
  return instance;
}

export async function renderWithI18n(
  ui: React.ReactElement,
  language: TestLanguage = 'zh-CN',
  options?: RenderOptions
): Promise<RenderResult & { i18n: i18n }> {
  const instance = await createTestI18n(language);
  const { wrapper: AdditionalWrapper, ...renderOptions } = options ?? {};
  const Wrapper = ({ children }: { children: React.ReactNode }) => (
    <I18nextProvider i18n={instance}>
      {AdditionalWrapper ? <AdditionalWrapper>{children}</AdditionalWrapper> : children}
    </I18nextProvider>
  );

  return {
    ...render(ui, { ...renderOptions, wrapper: Wrapper }),
    i18n: instance,
  };
}
