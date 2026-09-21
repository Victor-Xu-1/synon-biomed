/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
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
  Spin: () => <span data-testid='context-usage-loading' />,
}));

vi.mock('@icon-park/react', () => ({
  Close: () => <span aria-hidden='true'>×</span>,
}));

import ContextUsagePanel from '@/renderer/components/synonBiomed/runtime/ContextUsagePanel';

describe('ContextUsagePanel', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const path = String(input);
        return Promise.resolve(
          new Response(JSON.stringify(path.includes('/messages') ? { items: [] } : {}), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          })
        );
      })
    );
  });

  it('uses ACP usage data and never requests the cumulative frame projection', async () => {
    await renderWithI18n(
      <ContextUsagePanel conversationId='conversation-1' tokenUsage={{ total_tokens: 20 }} contextLimit={100} />,
      'en-US'
    );
    const trigger = await screen.findByTestId('synon-biomed-context-usage-trigger');
    expect(trigger.tagName).toBe('BUTTON');
    expect(trigger).toHaveAttribute('aria-expanded', 'false');

    fireEvent.click(trigger);
    await waitFor(() => expect(screen.getByTestId('context-usage-legend')).toBeInTheDocument());

    expect(vi.mocked(fetch).mock.calls.every(([input]) => String(input).includes('/messages'))).toBe(true);
    const messageRequest = vi.mocked(fetch).mock.calls.find(([input]) => String(input).includes('/messages'));
    expect(messageRequest?.[1]).toEqual(expect.objectContaining({ cache: 'no-store' }));
    expect(trigger).toHaveAttribute('aria-expanded', 'true');
  });
});
