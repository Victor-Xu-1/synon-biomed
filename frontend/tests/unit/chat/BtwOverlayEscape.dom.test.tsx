import BtwOverlay from '@/renderer/components/chat/BtwOverlay';
import { cleanup, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { shouldDismissBtwOverlay } from '@/renderer/components/chat/BtwOverlay/btwEscapePolicy';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@/renderer/hooks/context/ThemeContext', () => ({
  useThemeContext: () => ({
    theme: 'light',
    activeTheme: {
      id: 'light',
      name: 'Light',
      appearance: 'light',
      builtin: true,
      created_at: 1,
      updated_at: 1,
    },
    fontScale: 1,
    fontSizes: { chat: 16, code: 13, ui: 14 },
  }),
}));

afterEach(cleanup);

describe('side-question overlay escape policy', () => {
  it('dismisses only Escape inside the panel and yields to open dialogs', () => {
    const panel = document.createElement('div');
    const input = document.createElement('input');
    panel.appendChild(input);
    document.body.appendChild(panel);

    expect(shouldDismissBtwOverlay(new KeyboardEvent('keydown', { key: 'Enter' }), panel)).toBe(false);
    expect(shouldDismissBtwOverlay(new KeyboardEvent('keydown', { key: 'Escape' }), panel)).toBe(true);
    expect(
      shouldDismissBtwOverlay({ key: 'Escape', defaultPrevented: false, isComposing: false, target: input }, panel)
    ).toBe(true);

    const dialog = document.createElement('div');
    dialog.setAttribute('role', 'dialog');
    document.body.appendChild(dialog);
    expect(
      shouldDismissBtwOverlay({ key: 'Escape', defaultPrevented: false, isComposing: false, target: input }, panel)
    ).toBe(false);

    dialog.remove();
    panel.remove();
  });

  it('uses one labelled modal dialog surface for an open side question', async () => {
    await renderWithI18n(
      <BtwOverlay
        answer='A concise answer'
        isLoading={false}
        isOpen
        onDismiss={vi.fn()}
        question='A concise question'
      />
    );

    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveAttribute('aria-modal', 'true');
    expect(dialog).toHaveAccessibleName();
    const backdrop = document.body.querySelector('[aria-hidden="true"]');
    expect(backdrop).not.toBeNull();
    expect(dialog.contains(backdrop)).toBe(false);
  });
});
