/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import React from 'react';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { inflateSync } from 'node:zlib';

// Mirror the project convention: t() echoes the key so labels/tooltips are assertable.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: 'en' } }),
}));

// react-router: control location, capture navigate.
const navigate = vi.fn();
let currentPathname = '/guid';
const platformMocks = vi.hoisted(() => ({
  isElectronDesktopMock: vi.fn(() => false),
}));
vi.mock('react-router', () => ({
  useNavigate: () => navigate,
  useLocation: () => ({ pathname: currentPathname, search: '', hash: '' }),
  useNavigationType: () => 'POP',
  Outlet: () => null,
}));

// Hidden devtools easter-egg target (icon) — assert it is independent of navigation.
const openDevTools = vi.fn(() => Promise.resolve());
vi.mock('@/common', () => ({
  ipcBridge: {
    application: {
      openDevTools: { invoke: () => openDevTools() },
      logStream: { on: () => () => {} },
    },
    task: { stopAll: { invoke: () => Promise.resolve({ success: false }) } },
  },
}));

// Trim Layout's collaborators to keep this a focused brand-behaviour test.
vi.mock('@/renderer/components/layout/PwaPullToRefresh', () => ({ default: () => null }));
vi.mock('@/renderer/components/layout/Titlebar', () => ({ default: () => null }));
vi.mock('@renderer/hooks/system/useDeepLink', () => ({ useDeepLink: () => {} }));
vi.mock('@renderer/hooks/system/notification/useNotificationClick', () => ({ useNotificationClick: () => {} }));
vi.mock('@renderer/hooks/system/notification/useBrowserNotification', () => ({ useBrowserNotification: () => {} }));
vi.mock('@renderer/hooks/file/useDirectorySelection', () => ({
  useDirectorySelection: () => ({ contextHolder: null }),
}));
vi.mock('@renderer/utils/ui/siderTooltip', () => ({ cleanupSiderTooltips: () => {} }));
vi.mock('@renderer/hooks/ui/useConversationShortcuts', () => ({ useConversationShortcuts: () => {} }));
vi.mock('@renderer/utils/platform', () => ({ isElectronDesktop: platformMocks.isElectronDesktopMock }));

import Layout from '@renderer/components/layout/Layout';

const TestSider = () => <div>sider</div>;
const renderLayout = () => render(<Layout sider={<TestSider />} />);

const BACK_KEY = 'common.back';

const readBrandLockupAlpha = () => {
  const png = readFileSync(resolve(process.cwd(), 'public/branding/synon-biomed-lockup.png'));
  const idat: Buffer[] = [];
  let width = 0;
  let height = 0;
  let bitDepth = 0;
  let colorType = 0;
  let interlace = 0;

  for (let offset = 8; offset < png.length; ) {
    const length = png.readUInt32BE(offset);
    const type = png.toString('ascii', offset + 4, offset + 8);
    const dataStart = offset + 8;
    const dataEnd = dataStart + length;
    if (type === 'IHDR') {
      width = png.readUInt32BE(dataStart);
      height = png.readUInt32BE(dataStart + 4);
      bitDepth = png[dataStart + 8];
      colorType = png[dataStart + 9];
      interlace = png[dataStart + 12];
    } else if (type === 'IDAT') {
      idat.push(png.subarray(dataStart, dataEnd));
    } else if (type === 'IEND') {
      break;
    }
    offset = dataEnd + 4;
  }

  expect({ bitDepth, colorType, interlace }).toEqual({ bitDepth: 8, colorType: 6, interlace: 0 });
  const inflated = inflateSync(Buffer.concat(idat));
  const bytesPerPixel = 4;
  const stride = width * bytesPerPixel;
  let previous = Buffer.alloc(stride);
  let cursor = 0;
  let transparentPixels = 0;
  let opaquePixels = 0;

  const paeth = (left: number, above: number, upperLeft: number) => {
    const estimate = left + above - upperLeft;
    const leftDistance = Math.abs(estimate - left);
    const aboveDistance = Math.abs(estimate - above);
    const upperLeftDistance = Math.abs(estimate - upperLeft);
    if (leftDistance <= aboveDistance && leftDistance <= upperLeftDistance) return left;
    if (aboveDistance <= upperLeftDistance) return above;
    return upperLeft;
  };

  for (let rowIndex = 0; rowIndex < height; rowIndex += 1) {
    const filter = inflated[cursor];
    cursor += 1;
    const row = Buffer.from(inflated.subarray(cursor, cursor + stride));
    cursor += stride;
    for (let index = 0; index < stride; index += 1) {
      const left = index >= bytesPerPixel ? row[index - bytesPerPixel] : 0;
      const above = previous[index];
      const upperLeft = index >= bytesPerPixel ? previous[index - bytesPerPixel] : 0;
      if (filter === 1) row[index] = (row[index] + left) & 0xff;
      else if (filter === 2) row[index] = (row[index] + above) & 0xff;
      else if (filter === 3) row[index] = (row[index] + Math.floor((left + above) / 2)) & 0xff;
      else if (filter === 4) row[index] = (row[index] + paeth(left, above, upperLeft)) & 0xff;
      else if (filter !== 0) throw new Error(`Unsupported PNG filter ${filter}`);
    }
    for (let index = 3; index < stride; index += bytesPerPixel) {
      if (row[index] === 0) transparentPixels += 1;
      else if (row[index] === 255) opaquePixels += 1;
    }
    previous = row;
  }

  return { width, height, transparentPixels, opaquePixels };
};

describe('Layout shell behavior', () => {
  beforeEach(() => {
    Object.defineProperty(window, 'matchMedia', {
      writable: true,
      value: (query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addListener: () => {},
        removeListener: () => {},
        addEventListener: () => {},
        removeEventListener: () => {},
        dispatchEvent: () => false,
      }),
    });
    navigate.mockClear();
    openDevTools.mockClear();
    platformMocks.isElectronDesktopMock.mockReturnValue(false);
    localStorage.clear();
    sessionStorage.clear();
    currentPathname = '/guid';
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('navigates to the recorded last non-settings path when clicked in a settings route', () => {
    currentPathname = '/settings/about';
    sessionStorage.setItem('synon-ai:last-non-settings-path', '/conversation/abc');
    renderLayout();

    fireEvent.click(screen.getByLabelText(BACK_KEY));
    expect(navigate).toHaveBeenCalledWith('/conversation/abc');
  });

  it('falls back to /guid in a settings route when no path is recorded', () => {
    currentPathname = '/settings/system';
    renderLayout();

    fireEvent.click(screen.getByLabelText(BACK_KEY));
    expect(navigate).toHaveBeenCalledWith('/guid');
  });

  it('falls back to /guid when the recorded path is itself a settings path', () => {
    currentPathname = '/settings/about';
    sessionStorage.setItem('synon-ai:last-non-settings-path', '/settings/system');
    renderLayout();

    fireEvent.click(screen.getByLabelText(BACK_KEY));
    expect(navigate).toHaveBeenCalledWith('/guid');
  });

  it('activates via keyboard (Enter and Space) in a settings route', async () => {
    currentPathname = '/settings/about';
    sessionStorage.setItem('synon-ai:last-non-settings-path', '/conversation/abc');
    renderLayout();

    const brand = screen.getByLabelText(BACK_KEY);
    const user = userEvent.setup();
    brand.focus();
    await user.keyboard('{Enter}');
    await user.keyboard(' ');
    expect(navigate).toHaveBeenCalledTimes(2);
    expect(navigate).toHaveBeenCalledWith('/conversation/abc');
  });

  it('ignores non-activation keys in a settings route', () => {
    currentPathname = '/settings/about';
    sessionStorage.setItem('synon-ai:last-non-settings-path', '/conversation/abc');
    renderLayout();

    const brand = screen.getByLabelText(BACK_KEY);
    fireEvent.keyDown(brand, { key: 'Tab' });
    fireEvent.keyDown(brand, { key: 'a' });
    expect(navigate).not.toHaveBeenCalled();
  });

  it('sets the authenticated shell document title to Synon Biomed', () => {
    document.title = 'Synon Biomed - \u767b\u5f55';
    currentPathname = '/guid';

    renderLayout();

    expect(document.title).toBe('Synon Biomed');
  });

  it('renders the supplied complete SYNON-Biomed lockup without a separate wordmark in a non-settings route', () => {
    currentPathname = '/guid';
    renderLayout();

    // No actionable role/label in chat routes.
    expect(screen.queryByLabelText(BACK_KEY)).toBeNull();
    expect(screen.queryByText('SYNON-Biomed')).toBeNull();
    expect(screen.queryByText('Synon Biomed')).toBeNull();
    const brandLockup = screen.getByTestId('synon-biomed-brand-lockup');
    expect(brandLockup.tagName).toBe('IMG');
    expect(brandLockup).toHaveAttribute('alt', 'SYNON-Biomed');
    expect(brandLockup).toHaveAttribute('src', './branding/synon-biomed-lockup.png?v=0cac2ebf');
    fireEvent.click(brandLockup);
    expect(navigate).not.toHaveBeenCalled();
  });

  it('ships the complete lockup with a real transparent canvas', () => {
    const alpha = readBrandLockupAlpha();

    expect(alpha).toMatchObject({ width: 2172, height: 724 });
    expect(alpha.transparentPixels).toBeGreaterThan(1_000_000);
    expect(alpha.opaquePixels).toBeGreaterThan(10_000);
  });

  it('does not navigate when the wordmark is clicked in a non-settings route', () => {
    currentPathname = '/conversation/xyz';
    renderLayout();

    fireEvent.click(screen.getByTestId('synon-biomed-brand-lockup'));
    expect(navigate).not.toHaveBeenCalled();
  });

  it('clicking the logo icon counts toward the devtools easter-egg and never navigates', () => {
    currentPathname = '/guid';
    renderLayout();

    const icon = screen.getByTestId('synon-biomed-brand-lockup').parentElement as HTMLElement;
    expect(icon).toBeTruthy();
    for (let i = 0; i < 4; i++) fireEvent.click(icon);
    expect(openDevTools).toHaveBeenCalled();
    expect(navigate).not.toHaveBeenCalled();
  });

  it('does not expose the retired SynonAI updater for tray update events', () => {
    platformMocks.isElectronDesktopMock.mockReturnValue(true);
    const openListener = vi.fn();
    window.addEventListener('synon-ai-open-update-modal', openListener);

    try {
      renderLayout();

      window.dispatchEvent(new Event('tray:check-update'));

      expect(navigate).not.toHaveBeenCalled();
      expect(openListener).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener('synon-ai-open-update-modal', openListener);
    }
  });

  it('resizes the desktop sider continuously and clamps it to both width boundaries', () => {
    const { container } = renderLayout();
    const sider = container.querySelector('.layout-sider') as HTMLElement;
    const handle = screen.getByTestId('sider-resize-handle');

    expect(sider).toHaveStyle({ width: '260px' });
    fireEvent.mouseDown(handle, { clientX: 260 });
    fireEvent.mouseMove(window, { clientX: 360 });
    expect(sider).toHaveStyle({ width: '360px' });
    expect(handle).toHaveAttribute('aria-valuenow', '360');

    fireEvent.mouseMove(window, { clientX: 700 });
    expect(sider).toHaveStyle({ width: '420px' });
    fireEvent.mouseMove(window, { clientX: 210 });
    expect(sider).toHaveStyle({ width: '220px' });
    fireEvent.mouseUp(window);
    expect(document.body.style.cursor).toBe('');
  });

  it('automatically hides when dragged left beyond the collapse threshold', () => {
    const { container } = renderLayout();
    const sider = container.querySelector('.layout-sider') as HTMLElement;
    const handle = screen.getByTestId('sider-resize-handle');

    fireEvent.mouseDown(handle, { clientX: 260 });
    fireEvent.mouseMove(window, { clientX: 160 });

    expect(sider).toHaveClass('collapsed');
    expect(handle).toHaveAttribute('aria-valuenow', '0');
    fireEvent.mouseUp(window);
  });

  it('persists the last desktop width and supports keyboard resizing', () => {
    const firstRender = renderLayout();
    const firstHandle = screen.getByTestId('sider-resize-handle');
    fireEvent.mouseDown(firstHandle, { clientX: 260 });
    fireEvent.mouseMove(window, { clientX: 340 });
    fireEvent.mouseUp(window);
    firstRender.unmount();

    const { container } = renderLayout();
    const sider = container.querySelector('.layout-sider') as HTMLElement;
    const handle = screen.getByTestId('sider-resize-handle');
    expect(sider).toHaveStyle({ width: '340px' });

    fireEvent.keyDown(handle, { key: 'ArrowRight' });
    expect(sider).toHaveStyle({ width: '356px' });
    fireEvent.keyDown(handle, { key: 'End' });
    expect(sider).toHaveStyle({ width: '420px' });
    fireEvent.keyDown(handle, { key: 'Home' });
    expect(sider).toHaveClass('collapsed');
  });
});
