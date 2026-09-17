import SynonSelect from '@/renderer/components/base/SynonSelect';
import type { SelectHandle } from '@arco-design/web-react/es/Select/interface';
import React, { useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Message } from '@arco-design/web-react';
import { SUPPORTED_LANGUAGES, SUPPORTED_LANGUAGE_LABELS } from '@/common/config/i18n';
import { changeLanguage, normalizeLanguageCode } from '@/renderer/services/i18n';

const LanguageSwitcher: React.FC = () => {
  const { i18n, t } = useTranslation();
  const selectRef = useRef<SelectHandle>(null);

  const handleLanguageChange = useCallback(
    (value: string) => {
      selectRef.current?.blur?.();

      const applyLanguage = () => {
        changeLanguage(value).catch((error: Error) => {
          console.error('Failed to change language:', error);
          Message.error(t('settings.languageChangeFailed'));
        });
      };

      if (typeof window !== 'undefined' && 'requestAnimationFrame' in window) {
        window.requestAnimationFrame(() => window.requestAnimationFrame(applyLanguage));
      } else {
        setTimeout(applyLanguage, 0);
      }
    },
    [t]
  );

  const selectedLanguage = normalizeLanguageCode(i18n.resolvedLanguage || i18n.language);

  return (
    <div className='flex items-center gap-8px'>
      <SynonSelect
        ref={selectRef}
        className='w-160px'
        value={selectedLanguage}
        aria-label={t('settings.language')}
        data-testid='language-switcher'
        onChange={handleLanguageChange}
      >
        {SUPPORTED_LANGUAGES.map((language) => (
          <SynonSelect.Option key={language} value={language}>
            {SUPPORTED_LANGUAGE_LABELS[language]}
          </SynonSelect.Option>
        ))}
      </SynonSelect>
    </div>
  );
};

export default LanguageSwitcher;
