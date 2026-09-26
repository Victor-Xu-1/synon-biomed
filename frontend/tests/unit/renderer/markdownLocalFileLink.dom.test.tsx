/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React from 'react';
import type { i18n } from 'i18next';
import { I18nextProvider } from 'react-i18next';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import MarkdownView from '@/renderer/components/Markdown';
import { createTestI18n } from '../i18nTestUtils';

const copyTextMock = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));
const openExternalUrlMock = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));

vi.mock('@/renderer/components/Markdown/ShadowView', () => ({
  __esModule: true,
  default: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
}));

vi.mock('@/renderer/components/Markdown/CodeBlock', () => ({
  __esModule: true,
  default: ({ children }: { children?: React.ReactNode }) => <code>{children}</code>,
}));

vi.mock('@/renderer/components/media/LocalImageView', () => ({
  __esModule: true,
  default: ({ src, alt }: { src: string; alt: string }) => <img src={src} alt={alt} />,
}));

vi.mock('@/renderer/utils/chat/latexDelimiters', () => ({
  convertLatexDelimiters: (text: string) => text,
}));

vi.mock('@/renderer/utils/platform', () => ({
  openExternalUrl: openExternalUrlMock,
}));

vi.mock('@/renderer/utils/ui/clipboard', () => ({
  copyText: copyTextMock,
}));

vi.mock('@arco-design/web-react', () => ({
  Button: ({
    children,
    icon,
    ...props
  }: React.ButtonHTMLAttributes<HTMLButtonElement> & { icon?: React.ReactNode }) => (
    <button type='button' {...props}>
      {icon}
      {children}
    </button>
  ),
  Message: {
    error: vi.fn(),
  },
  Tooltip: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}));

vi.mock('@icon-park/react', () => ({
  Copy: () => <span data-testid='copy-icon' />,
}));

let testI18n: i18n;

const renderWithProviders = (ui: React.ReactElement) => render(<I18nextProvider i18n={testI18n}>{ui}</I18nextProvider>);

describe('MarkdownView local file links', () => {
  beforeAll(async () => {
    testI18n = await createTestI18n('en-US');
  });

  beforeEach(() => {
    copyTextMock.mockClear();
    openExternalUrlMock.mockClear();
  });

  afterEach(() => vi.unstubAllGlobals());

  it('renders local file links as app controls instead of browser anchors', () => {
    const onLocalFileLink = vi.fn();

    renderWithProviders(
      <MarkdownView onLocalFileLink={onLocalFileLink}>
        {'[report.xlsx](/C:/Users/Administrator/AppData/Roaming/SynonAI/report.xlsx)'}
      </MarkdownView>
    );

    expect(screen.queryByRole('link')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'report.xlsx' }));
    expect(onLocalFileLink).toHaveBeenCalledWith(
      'C:/Users/Administrator/AppData/Roaming/SynonAI/report.xlsx',
      expect.objectContaining({
        filePath: 'C:/Users/Administrator/AppData/Roaming/SynonAI/report.xlsx',
      })
    );

    fireEvent.click(screen.getByRole('button', { name: 'Copy' }));
    expect(copyTextMock).toHaveBeenCalledWith('C:/Users/Administrator/AppData/Roaming/SynonAI/report.xlsx');
  });

  it('renders line references as file chips and copies the full reference', () => {
    const onLocalFileLink = vi.fn();

    renderWithProviders(
      <MarkdownView onLocalFileLink={onLocalFileLink}>
        {'[2026-06-19.log](C:/Users/Administrator/AppData/Roaming/SynonAI/logs/2026-06-19.log:1421)'}
      </MarkdownView>
    );

    const fileButton = screen.getByRole('button', { name: /2026-06-19\.log\s+L1421/ });
    fireEvent.click(fileButton);

    expect(onLocalFileLink).toHaveBeenCalledWith(
      'C:/Users/Administrator/AppData/Roaming/SynonAI/logs/2026-06-19.log',
      expect.objectContaining({
        filePath: 'C:/Users/Administrator/AppData/Roaming/SynonAI/logs/2026-06-19.log',
        rawReference: 'C:/Users/Administrator/AppData/Roaming/SynonAI/logs/2026-06-19.log:1421',
        line: 1421,
      })
    );

    fireEvent.click(screen.getByRole('button', { name: 'Copy' }));
    expect(copyTextMock).toHaveBeenCalledWith(
      'C:/Users/Administrator/AppData/Roaming/SynonAI/logs/2026-06-19.log:1421'
    );
  });

  it('renders line and column references as file chips and copies the full reference', () => {
    const onLocalFileLink = vi.fn();

    renderWithProviders(
      <MarkdownView onLocalFileLink={onLocalFileLink}>
        {'[app.log](C:/Users/Administrator/AppData/Roaming/SynonAI/logs/app.log:1421:7)'}
      </MarkdownView>
    );

    const fileButton = screen.getByRole('button', { name: /app\.log\s+L1421:7/ });
    fireEvent.click(fileButton);

    expect(onLocalFileLink).toHaveBeenCalledWith(
      'C:/Users/Administrator/AppData/Roaming/SynonAI/logs/app.log',
      expect.objectContaining({
        filePath: 'C:/Users/Administrator/AppData/Roaming/SynonAI/logs/app.log',
        rawReference: 'C:/Users/Administrator/AppData/Roaming/SynonAI/logs/app.log:1421:7',
        line: 1421,
        column: 7,
      })
    );

    fireEvent.click(screen.getByRole('button', { name: 'Copy' }));
    expect(copyTextMock).toHaveBeenCalledWith('C:/Users/Administrator/AppData/Roaming/SynonAI/logs/app.log:1421:7');
  });

  it('renders hash range references as file chips and copies normalized local references', () => {
    const onLocalFileLink = vi.fn();

    renderWithProviders(
      <MarkdownView onLocalFileLink={onLocalFileLink}>
        {'[user.js 1-260行](/Users/demo/project/user.js#L1-L260)'}
      </MarkdownView>
    );

    expect(screen.queryByRole('link', { name: /user\.js/ })).not.toBeInTheDocument();

    const fileButton = screen.getByRole('button', { name: /user\.js 1-260行\s+L1-L260/ });
    fireEvent.click(fileButton);

    expect(onLocalFileLink).toHaveBeenCalledWith(
      '/Users/demo/project/user.js',
      expect.objectContaining({
        filePath: '/Users/demo/project/user.js',
        rawReference: '/Users/demo/project/user.js#L1-L260',
        line: 1,
        endLine: 260,
      })
    );

    fireEvent.click(screen.getByRole('button', { name: 'Copy' }));
    expect(copyTextMock).toHaveBeenCalledWith('/Users/demo/project/user.js#L1-L260');
  });

  it('does not render a no-op open button when no local file handler is provided', () => {
    renderWithProviders(
      <MarkdownView>{'[report.xlsx](/C:/Users/Administrator/AppData/Roaming/SynonAI/report.xlsx)'}</MarkdownView>
    );

    expect(screen.queryByRole('link')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'report.xlsx' })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Copy' }));
    expect(copyTextMock).toHaveBeenCalledWith('C:/Users/Administrator/AppData/Roaming/SynonAI/report.xlsx');
  });

  it('lets a conversation handler intercept a relative artifact link before external navigation', async () => {
    const onLink = vi.fn().mockResolvedValue(true);
    renderWithProviders(<MarkdownView onLink={onLink}>{'[README](README.md)'}</MarkdownView>);

    fireEvent.click(screen.getByRole('link', { name: 'README' }));

    await waitFor(() => {
      expect(onLink).toHaveBeenCalledWith('README.md');
    });
    expect(openExternalUrlMock).not.toHaveBeenCalled();
  });

  it('falls back to normal external navigation when a conversation handler declines a link', async () => {
    const onLink = vi.fn().mockResolvedValue(false);
    renderWithProviders(<MarkdownView onLink={onLink}>{'[docs](https://synon-ai.com/docs)'}</MarkdownView>);

    fireEvent.click(screen.getByRole('link', { name: 'docs' }));

    await waitFor(() => {
      expect(onLink).toHaveBeenCalledWith('https://synon-ai.com/docs');
      expect(openExternalUrlMock).toHaveBeenCalledWith('https://synon-ai.com/docs');
    });
  });

  it('projects an encoded artifact reference to its real API href while preserving click identity', async () => {
    const onLink = vi.fn().mockResolvedValue(true);
    const resolveLinkHref = vi.fn().mockResolvedValue('/api/artifacts/artifact-report/versions/version-report');

    renderWithProviders(
      <MarkdownView onLink={onLink} resolveLinkHref={resolveLinkHref}>
        {'[Final report](%7B%7Bartifact%3Aversion-report%7D%7D)'}
      </MarkdownView>
    );

    const link = screen.getByRole('link', { name: 'Final report' });
    expect(link).toHaveClass('synon-artifact-link');
    expect(link).toHaveAttribute('data-synon-artifact-link', 'true');
    await waitFor(() => {
      expect(link).toHaveAttribute('href', '/api/artifacts/artifact-report/versions/version-report');
    });
    expect(resolveLinkHref).toHaveBeenCalledWith('%7B%7Bartifact%3Aversion-report%7D%7D');

    fireEvent.click(link);
    await waitFor(() => {
      expect(onLink).toHaveBeenCalledWith('%7B%7Bartifact%3Aversion-report%7D%7D');
    });
    expect(openExternalUrlMock).not.toHaveBeenCalled();
  });

  it('keeps ordinary http links as browser anchors', () => {
    renderWithProviders(<MarkdownView>{'[docs](https://synon-ai.com/docs)'}</MarkdownView>);

    const link = screen.getByRole('link', { name: 'docs' });
    expect(link).toHaveAttribute('href', 'https://synon-ai.com/docs');
  });

  it('keeps http hash links as browser anchors', () => {
    renderWithProviders(<MarkdownView>{'[docs](https://synon-ai.com/docs#L10)'}</MarkdownView>);

    const link = screen.getByRole('link', { name: 'docs' });
    expect(link).toHaveAttribute('href', 'https://synon-ai.com/docs#L10');
  });

  it('adds empty alt text to external raw HTML images without alt text', () => {
    let callback: IntersectionObserverCallback | undefined;
    let target: Element | undefined;
    vi.stubGlobal(
      'IntersectionObserver',
      class {
        constructor(nextCallback: IntersectionObserverCallback) {
          callback = nextCallback;
        }
        observe(nextTarget: Element) {
          target = nextTarget;
        }
        unobserve() {}
        disconnect() {}
      }
    );
    const { container } = renderWithProviders(
      <MarkdownView allowHtml>{'<img src="https://example.com/generated.png" />'}</MarkdownView>
    );

    expect(container.querySelector('img')).toBeNull();
    if (!target) throw new Error('external image was not observed');
    act(() =>
      callback?.(
        [{ target, isIntersecting: true, intersectionRatio: 1 } as IntersectionObserverEntry],
        {} as IntersectionObserver
      )
    );
    const image = container.querySelector('img');
    expect(image).toHaveAttribute('src', 'https://example.com/generated.png');
    expect(image).toHaveAttribute('alt', '');
  });

  it('renders a bounded raster data image from streamed Markdown', () => {
    let callback: IntersectionObserverCallback | undefined;
    let target: Element | undefined;
    vi.stubGlobal(
      'IntersectionObserver',
      class {
        constructor(nextCallback: IntersectionObserverCallback) {
          callback = nextCallback;
        }
        observe(nextTarget: Element) {
          target = nextTarget;
        }
        unobserve() {}
        disconnect() {}
      }
    );
    const source = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB';
    renderWithProviders(<MarkdownView>{`![CRBN binding overview](${source})`}</MarkdownView>);

    if (!target) throw new Error('inline image was not observed');
    act(() =>
      callback?.(
        [{ target, isIntersecting: true, intersectionRatio: 1 } as IntersectionObserverEntry],
        {} as IntersectionObserver
      )
    );
    expect(screen.getByRole('img', { name: 'CRBN binding overview' })).toHaveAttribute('src', source);
  });

  it('renders a relative Synon Biomed image through its resolved artifact URL', async () => {
    let callback: IntersectionObserverCallback | undefined;
    let target: Element | undefined;
    vi.stubGlobal(
      'IntersectionObserver',
      class {
        constructor(nextCallback: IntersectionObserverCallback) {
          callback = nextCallback;
        }
        observe(nextTarget: Element) {
          target = nextTarget;
        }
        unobserve() {}
        disconnect() {}
      }
    );
    const resolveImageSrc = vi.fn().mockResolvedValue('/api/artifacts/artifact-figure');
    renderWithProviders(
      <MarkdownView resolveImageSrc={resolveImageSrc}>{'![Coverage]({{artifact:version-figure}})'}</MarkdownView>
    );

    expect(resolveImageSrc).not.toHaveBeenCalled();
    expect(target).toBeDefined();
    if (!target) throw new Error('artifact image was not observed');
    act(() =>
      callback?.(
        [{ target, isIntersecting: true, intersectionRatio: 1 } as IntersectionObserverEntry],
        {} as IntersectionObserver
      )
    );

    await waitFor(() => {
      expect(screen.getByRole('img', { name: 'Coverage' })).toHaveAttribute('src', '/api/artifacts/artifact-figure');
    });
    expect(resolveImageSrc).toHaveBeenCalledWith('{{artifact:version-figure}}');
  });
});
