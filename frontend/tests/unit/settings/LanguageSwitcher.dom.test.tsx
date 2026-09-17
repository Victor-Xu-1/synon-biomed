import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import LanguageSwitcher from '@/renderer/components/settings/LanguageSwitcher';

const mocks = vi.hoisted(() => ({
  changeLanguage: vi.fn(),
}));

vi.mock('@/renderer/services/i18n', () => ({
  changeLanguage: mocks.changeLanguage,
  normalizeLanguageCode: (language: string) => language,
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    i18n: { language: 'zh-CN', resolvedLanguage: 'zh-CN' },
    t: (key: string) => key,
  }),
}));

vi.mock('@/renderer/components/base/SynonSelect', async () => {
  const ReactModule = await import('react');
  const Select = ReactModule.forwardRef<
    HTMLSelectElement,
    React.PropsWithChildren<{
      value?: string;
      onChange?: (value: string) => void;
      'aria-label'?: string;
      'data-testid'?: string;
    }>
  >(({ children, onChange, ...props }, ref) => (
    <select ref={ref} onChange={(event) => onChange?.(event.target.value)} {...props}>
      {children}
    </select>
  ));
  const Option: React.FC<React.PropsWithChildren<{ value: string }>> = ({ children, value }) => (
    <option value={value}>{children}</option>
  );
  return { default: Object.assign(Select, { Option }) };
});

describe('LanguageSwitcher', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.changeLanguage.mockResolvedValue(undefined);
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => {
      callback(0);
      return 1;
    });
  });

  it('offers only the two fully supported product languages and switches immediately', async () => {
    render(<LanguageSwitcher />);

    const select = screen.getByTestId('language-switcher');
    expect(screen.getAllByRole('option').map((option) => option.textContent)).toEqual(['中文', 'English']);

    fireEvent.change(select, { target: { value: 'en-US' } });
    await waitFor(() => expect(mocks.changeLanguage).toHaveBeenCalledWith('en-US'));
  });
});
