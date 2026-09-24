/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@arco-design/web-react', () => ({
  Dropdown: ({
    children,
    droplist,
    popupVisible,
    onVisibleChange,
  }: {
    children: React.ReactNode;
    droplist: React.ReactNode;
    popupVisible?: boolean;
    onVisibleChange?: (visible: boolean) => void;
  }) => (
    <div>
      <div onClick={() => onVisibleChange?.(!popupVisible)}>{children}</div>
      {popupVisible ? droplist : null}
    </div>
  ),
  Spin: () => <span aria-hidden='true' />,
}));

vi.mock('@icon-park/react', () => ({
  Close: () => <span aria-hidden='true'>×</span>,
}));

import ContextUsagePanel from '@/renderer/components/synonBiomed/runtime/ContextUsagePanel';

const usage = (usedTokens = 20, sessionId = 'conversation-1') => ({
  status: 'available',
  snapshot: {
    sessionId,
    requestId: 'request-one',
    model: 'model',
    observedAt: '2026-01-01T00:00:00Z',
    state: 'complete',
    source: 'provider',
    usedTokens,
    limitTokens: 100,
    limitSource: 'configured',
    outputTokens: 3,
    hasMedia: false,
    inputEstimates: [
      { key: 'systemPrompt', tokens: 4 },
      { key: 'messages', tokens: 8 },
      { key: 'toolDefinitions', tokens: 2 },
    ],
  },
});

describe('ContextUsagePanel', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(usage()))))
    );
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('loads the real context contract without ACP props or message sampling', async () => {
    await renderWithI18n(<ContextUsagePanel conversationId='conversation-1' />, 'en-US');
    const trigger = screen.getByTestId('synon-biomed-context-usage-trigger');
    expect(trigger.tagName).toBe('BUTTON');
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(trigger);
    expect(await screen.findByTestId('context-usage-percent')).toHaveTextContent('20.0%');
    expect(screen.getByTestId('context-usage-source')).toHaveTextContent('provider-reported usage');
    expect(screen.getByTestId('context-usage-panel')).toHaveTextContent('does not show remaining context');
    expect(screen.getByTestId('context-usage-legend')).toHaveTextContent('Tool definitions (including MCP)');
    expect(vi.mocked(fetch).mock.calls.every(([input]) => String(input).endsWith('/context-usage'))).toBe(true);
    await act(async () => {
      fireEvent.click(screen.getByTestId('context-usage-close'));
    });
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
  });

  it('identifies the default runner budget as unverified', async () => {
    const defaultBudget = usage();
    defaultBudget.snapshot.limitSource = 'runner_default';
    vi.mocked(fetch).mockImplementation(() => Promise.resolve(new Response(JSON.stringify(defaultBudget))));
    await renderWithI18n(<ContextUsagePanel conversationId='conversation-1' />, 'en-US');
    fireEvent.click(screen.getByTestId('synon-biomed-context-usage-trigger'));
    expect(await screen.findByTestId('context-usage-source')).toHaveTextContent('not a verified model limit');
    expect(screen.queryByTestId('context-usage-percent')).not.toBeInTheDocument();
    expect(screen.getByTestId('synon-biomed-context-usage-trigger').querySelectorAll('circle')[1]).toHaveAttribute(
      'stroke-dasharray',
      '2 3'
    );
  });

  it('shows a timestamp cleanly when the provider model name is absent', async () => {
    const unnamedModel = usage();
    unnamedModel.snapshot.model = '';
    vi.mocked(fetch).mockImplementation(() => Promise.resolve(new Response(JSON.stringify(unnamedModel))));
    await renderWithI18n(<ContextUsagePanel conversationId='conversation-1' />, 'en-US');
    fireEvent.click(screen.getByTestId('synon-biomed-context-usage-trigger'));
    const observed = await screen.findByTestId('context-usage-observed');
    expect(observed.querySelector('time')).toHaveAttribute('datetime', unnamedModel.snapshot.observedAt);
    expect(observed.textContent?.trimStart().startsWith('·')).toBe(false);
  });

  it('renders a valid zero rather than an endless spinner', async () => {
    const zero = usage(0);
    zero.snapshot.source = 'estimated';
    zero.snapshot.outputTokens = 0;
    zero.snapshot.inputEstimates.forEach((row) => {
      row.tokens = 0;
    });
    vi.mocked(fetch).mockImplementation(() => Promise.resolve(new Response(JSON.stringify(zero))));
    await renderWithI18n(<ContextUsagePanel conversationId='conversation-1' />, 'zh-CN');
    fireEvent.click(screen.getByTestId('synon-biomed-context-usage-trigger'));
    expect(await screen.findByTestId('context-usage-percent')).toHaveTextContent('0.0%');
    expect(screen.queryByTestId('context-usage-loading')).not.toBeInTheDocument();
    expect(screen.getByTestId('context-usage-legend')).toHaveTextContent('最近响应');
  });

  it('does not report zero response tokens before a response exists', async () => {
    const pending = usage();
    pending.snapshot.state = 'request';
    pending.snapshot.source = 'estimated';
    pending.snapshot.outputTokens = 0;
    vi.mocked(fetch).mockImplementation(() => Promise.resolve(new Response(JSON.stringify(pending))));
    await renderWithI18n(<ContextUsagePanel conversationId='conversation-1' />, 'en-US');
    fireEvent.click(screen.getByTestId('synon-biomed-context-usage-trigger'));
    expect(await screen.findByText(/response usage is not yet available/)).toBeInTheDocument();
    expect(screen.getByTestId('context-usage-legend')).not.toHaveTextContent('Latest response');
  });

  it('marks a failed request and keeps unknown response usage hidden', async () => {
    const failed = usage();
    failed.snapshot.state = 'failed';
    failed.snapshot.source = 'estimated';
    failed.snapshot.outputTokens = 0;
    vi.mocked(fetch).mockImplementation(() => Promise.resolve(new Response(JSON.stringify(failed))));
    await renderWithI18n(<ContextUsagePanel conversationId='conversation-1' />, 'en-US');
    fireEvent.click(screen.getByTestId('synon-biomed-context-usage-trigger'));
    expect(await screen.findByText(/last request failed/)).toBeInTheDocument();
    expect(screen.getByTestId('context-usage-legend')).not.toHaveTextContent('Latest response');
  });

  it('shows missing records explicitly and allows retry', async () => {
    vi.mocked(fetch).mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ status: 'unavailable' }))));
    await renderWithI18n(<ContextUsagePanel conversationId='conversation-1' />, 'en-US');
    fireEvent.click(screen.getByTestId('synon-biomed-context-usage-trigger'));
    expect(await screen.findByText(/No saved context record yet/)).toBeInTheDocument();
    expect(screen.queryByTestId('context-usage-loading')).not.toBeInTheDocument();
    vi.mocked(fetch).mockImplementation(() => Promise.resolve(new Response(JSON.stringify(usage(30)))));
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByTestId('context-usage-percent')).toHaveTextContent('30.0%');
  });

  it('shows failures without silently substituting old usage', async () => {
    vi.mocked(fetch).mockImplementation(() => Promise.resolve(new Response('{}', { status: 503 })));
    await renderWithI18n(<ContextUsagePanel conversationId='conversation-1' />, 'en-US');
    fireEvent.click(screen.getByTestId('synon-biomed-context-usage-trigger'));
    expect(await screen.findByText(/Could not load context usage/)).toBeInTheDocument();
    expect(screen.queryByTestId('context-usage-percent')).not.toBeInTheDocument();
  });

  it('bounds a stalled request and exposes retry instead of spinning forever', async () => {
    // This transport never settles, even after abort. The UI deadline must.
    vi.mocked(fetch).mockImplementation(() => new Promise(() => {}));
    await renderWithI18n(<ContextUsagePanel conversationId='conversation-1' />, 'en-US');
    vi.useFakeTimers();
    fireEvent.click(screen.getByTestId('synon-biomed-context-usage-trigger'));
    expect(screen.getByTestId('context-usage-loading')).toBeInTheDocument();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(8000);
    });
    expect(screen.getByText(/Could not load context usage/)).toBeInTheDocument();
    expect(screen.queryByTestId('context-usage-loading')).not.toBeInTheDocument();
    const attempts = vi.mocked(fetch).mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30000);
    });
    expect(vi.mocked(fetch).mock.calls.length).toBe(attempts);
  });

  it('polls live usage without overlapping requests and cancels on unmount', async () => {
    const rendered = await renderWithI18n(<ContextUsagePanel conversationId='conversation-1' active />, 'en-US');
    fireEvent.click(screen.getByTestId('synon-biomed-context-usage-trigger'));
    await screen.findByTestId('context-usage-percent');
    vi.useFakeTimers();
    // Reopening creates a fresh polling timer under the controlled clock.
    fireEvent.click(screen.getByTestId('context-usage-close'));
    fireEvent.click(screen.getByTestId('synon-biomed-context-usage-trigger'));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const before = vi.mocked(fetch).mock.calls.length;
    vi.mocked(fetch).mockImplementation(() => Promise.resolve(new Response(JSON.stringify(usage(40)))));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(vi.mocked(fetch).mock.calls.length).toBe(before + 1);
    expect(screen.getByTestId('context-usage-percent')).toHaveTextContent('40.0%');
    rendered.unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10000);
    });
    expect(vi.mocked(fetch).mock.calls.length).toBe(before + 1);
  });

  it('does not show a late result from a different conversation', async () => {
    const pending: Array<(response: Response) => void> = [];
    vi.mocked(fetch).mockImplementation(() => new Promise((resolve) => pending.push(resolve)));
    const rendered = await renderWithI18n(<ContextUsagePanel conversationId='conversation-1' />, 'en-US');
    fireEvent.click(screen.getByTestId('synon-biomed-context-usage-trigger'));
    rendered.rerender(<ContextUsagePanel conversationId='conversation-2' />);
    await act(async () => {
      for (const resolve of pending.slice(0, -1)) resolve(new Response(JSON.stringify(usage(99))));
      pending.at(-1)?.(new Response(JSON.stringify(usage(5, 'conversation-2'))));
    });
    await waitFor(() => expect(screen.getByTestId('context-usage-percent')).toHaveTextContent('5.0%'));
    expect(screen.getByTestId('context-usage-percent')).not.toHaveTextContent('99.0%');
  });
});
