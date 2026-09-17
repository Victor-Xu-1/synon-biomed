import React from 'react';
import { useTranslation } from 'react-i18next';

type SettingsPaginationProps = {
  page: number;
  totalPages: number;
  onChange: (page: number) => void;
  label: string;
};

/** Compact, keyboard-friendly pager used when a settings catalog exceeds one screen. */
const SettingsPagination: React.FC<SettingsPaginationProps> = ({ page, totalPages, onChange, label }) => {
  const { t } = useTranslation();
  const paginationRef = React.useRef<HTMLElement>(null);
  const previousPageRef = React.useRef(page);
  const resetContentScroll = React.useCallback(() => {
    const content = paginationRef.current?.closest<HTMLElement>('.settings-page-content');
    if (content) content.scrollTop = 0;
  }, []);

  React.useLayoutEffect(() => {
    if (previousPageRef.current === page) return;
    previousPageRef.current = page;
    resetContentScroll();
  }, [page, resetContentScroll]);

  const changePage = React.useCallback(
    (nextPage: number) => {
      if (nextPage === page) resetContentScroll();
      onChange(nextPage);
    },
    [onChange, page, resetContentScroll]
  );

  if (totalPages <= 1) return null;
  return (
    <nav ref={paginationRef} className='settings-pagination' aria-label={label}>
      <button
        type='button'
        className='settings-pagination__arrow'
        aria-label={t('settings.settingsPaginationPrevious')}
        disabled={page === 1}
        onClick={() => changePage(Math.max(1, page - 1))}
      >
        ‹
      </button>
      {Array.from({ length: totalPages }, (_, index) => index + 1).map((value) => (
        <button
          key={value}
          type='button'
          className='settings-pagination__page'
          aria-current={value === page ? 'page' : undefined}
          aria-label={`${label} ${value}`}
          onClick={() => changePage(value)}
        >
          {value}
        </button>
      ))}
      <button
        type='button'
        className='settings-pagination__arrow'
        aria-label={t('settings.settingsPaginationNext')}
        disabled={page === totalPages}
        onClick={() => changePage(Math.min(totalPages, page + 1))}
      >
        ›
      </button>
    </nav>
  );
};

export default SettingsPagination;
