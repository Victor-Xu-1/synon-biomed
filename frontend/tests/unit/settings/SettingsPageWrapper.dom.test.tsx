import { act, fireEvent, render, screen } from '@testing-library/react';
import React from 'react';
import { MemoryRouter } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const layoutState = vi.hoisted(() => ({
  isMobile: true,
  setSiderCollapsed: vi.fn(),
}));

vi.mock('@/renderer/hooks/context/LayoutContext', () => ({
  useLayoutContext: () => layoutState,
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string }) =>
      ({
        'settings.synonBiomedOpenAccount': '打开账户侧栏',
        'settings.synonBiomedAccountMenu': '账户与设置',
      })[key] ??
      options?.defaultValue ??
      key,
  }),
}));

import SettingsPageWrapper from '@/renderer/pages/settings/components/SettingsPageWrapper';
import { navigateSettingsRoute } from '@/renderer/pages/settings/settingsNavigation';

describe('SettingsPageWrapper mobile navigation', () => {
  beforeEach(() => {
    layoutState.isMobile = true;
    window.location.hash = '#/settings/general';
    vi.clearAllMocks();
  });

  it('opens the existing sidebar so the account and support menu stays reachable on narrow screens', () => {
    renderWrapper();

    fireEvent.click(screen.getByRole('button', { name: '打开账户侧栏' }));

    expect(layoutState.setSiderCollapsed).toHaveBeenCalledWith(false);
  });

  it('does not duplicate the mobile account entry on desktop', () => {
    layoutState.isMobile = false;
    renderWrapper();

    expect(screen.queryByRole('button', { name: '打开账户侧栏' })).not.toBeInTheDocument();
  });

  it('exposes the single shared visual-system contract to every settings route', () => {
    const { container } = renderWrapper();

    expect(container.querySelector('.settings-page-wrapper')).toHaveAttribute(
      'data-settings-visual-system',
      'scientific-connectors-v3'
    );
  });

  it('keeps the mounted content route stable and hides it while a new lazy route resolves', () => {
    const { container } = renderWrapper();
    const wrapper = container.querySelector('.settings-page-wrapper');
    expect(wrapper).toHaveAttribute('data-settings-route', 'general');

    act(() => navigateSettingsRoute('tools'));

    expect(wrapper).toHaveAttribute('data-settings-route', 'general');
    expect(wrapper).toHaveClass('settings-page-wrapper--transitioning');
  });
});

function renderWrapper() {
  return render(
    <MemoryRouter initialEntries={['/settings/general']}>
      <SettingsPageWrapper>
        <div>Settings content</div>
      </SettingsPageWrapper>
    </MemoryRouter>
  );
}
