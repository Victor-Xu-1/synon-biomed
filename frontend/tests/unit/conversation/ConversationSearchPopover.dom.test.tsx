import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { MemoryRouter, useLocation } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const mocks = vi.hoisted(() => ({
  search: vi.fn(),
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    database: {
      searchConversationMessages: {
        invoke: mocks.search,
      },
    },
  },
}));

vi.mock('@/renderer/components/base/SynonModal', () => ({
  default: ({ visible, children }: { visible: boolean; children: React.ReactNode }) =>
    visible ? <div role='dialog'>{children}</div> : null,
}));

vi.mock('@/renderer/hooks/synonBiomed/runtime/usePresetAssistantInfo', () => ({
  usePresetAssistantInfo: () => ({ info: null }),
}));
vi.mock('@/renderer/utils/synonBiomed/runtime/runtimeLogo', () => ({
  useAgentLogos: () => ({}),
}));
vi.mock('@/renderer/pages/conversation/utils/conversationAssistantIdentity', () => ({
  resolveConversationLeadingMark: () => ({ kind: 'none' }),
}));
vi.mock('@/renderer/utils/ui/focus', () => ({
  blockMobileInputFocus: vi.fn(),
  blurActiveElement: vi.fn(),
}));
vi.mock('@/renderer/components/synonBiomed/SynonBiomedAvatar', () => ({
  default: () => <span data-testid='assistant-avatar' />,
}));
vi.mock('@arco-design/web-react', () => ({
  Spin: () => <span data-testid='spinner' />,
}));
vi.mock('@icon-park/react', () => ({
  Close: () => <span />,
  CloseSmall: () => <span />,
  MessageOne: () => <span />,
  Search: () => <span />,
}));

import ConversationSearchPopover from '@/renderer/pages/conversation/GroupedHistory/ConversationSearchPopover';

const makeItem = (id: string, name: string, messageId = '') => ({
  conversation: {
    id,
    name,
    desc: '',
    type: 'acp',
    created_at: 1,
    modified_at: 2,
    status: 'finished',
    model: { provider_id: 'provider', model: 'model' },
    extra: { backend: 'synonbiomed', project_name: 'Discovery' },
  },
  message_id: messageId,
  message_type: 'text',
  message_created_at: 1787557580781,
  preview_text: messageId ? `Matched message ${name}` : '',
  project_name: 'Discovery',
  match_kind: messageId ? 'message' : 'recent',
  match_count: messageId ? 1 : 0,
  relevance: messageId ? 700 : 0,
});

const LocationProbe = () => {
  const location = useLocation();
  return (
    <output data-testid='location'>
      {location.pathname}|{JSON.stringify(location.state)}
    </output>
  );
};

const RouterWrapper = ({ children }: { children: React.ReactNode }) => (
  <MemoryRouter initialEntries={['/guid']}>
    {children}
    <LocationProbe />
  </MemoryRouter>
);

describe('ConversationSearchPopover', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', {
      configurable: true,
      value: vi.fn(),
    });
  });

  it('loads recent conversations, searches messages, supports keyboard selection, and preserves target navigation', async () => {
    mocks.search.mockImplementation(({ keyword }: { keyword: string }) =>
      Promise.resolve({
        items: keyword
          ? [makeItem('conversation-a', 'Alpha', 'message-a'), makeItem('conversation-b', 'Beta', 'message-b')]
          : [makeItem('conversation-recent', 'Recent conversation')],
        total: keyword ? 2 : 1,
        page: 0,
        page_size: 30,
        has_more: false,
      })
    );

    await renderWithI18n(
      <ConversationSearchPopover renderTrigger={({ onClick }) => <button onClick={onClick}>open search</button>} />,
      'en-US',
      { wrapper: RouterWrapper }
    );

    fireEvent.click(screen.getByRole('button', { name: 'open search' }));
    expect(await screen.findByText('Recent conversation')).toBeInTheDocument();
    expect(mocks.search).toHaveBeenCalledWith({ keyword: '', page: 0, page_size: 30 });

    const input = screen.getByRole('combobox');
    fireEvent.change(input, { target: { value: 'Matched' } });
    expect(await screen.findByText('Alpha')).toBeInTheDocument();
    expect(await screen.findByText('Beta')).toBeInTheDocument();
    await waitFor(() => expect(mocks.search).toHaveBeenLastCalledWith({ keyword: 'Matched', page: 0, page_size: 30 }));

    fireEvent.keyDown(input, { key: 'ArrowDown' });
    fireEvent.keyDown(input, { key: 'Enter' });
    await waitFor(() =>
      expect(screen.getByTestId('location')).toHaveTextContent(
        '/conversation/conversation-b|{"targetMessageId":"message-b","fromConversationSearch":true}'
      )
    );
  });

  it('shows a compact retry state and recovers through the same search request', async () => {
    let shouldFail = true;
    mocks.search.mockImplementation(() => {
      if (shouldFail) return Promise.reject(new Error('search unavailable'));
      return Promise.resolve({
        items: [makeItem('conversation-recovered', 'Recovered conversation')],
        total: 1,
        page: 0,
        page_size: 30,
        has_more: false,
      });
    });
    vi.spyOn(console, 'error').mockImplementation(() => undefined);

    await renderWithI18n(
      <ConversationSearchPopover renderTrigger={({ onClick }) => <button onClick={onClick}>open search</button>} />,
      'en-US',
      { wrapper: RouterWrapper }
    );
    fireEvent.click(screen.getByRole('button', { name: 'open search' }));
    expect(await screen.findByText('Conversation search is temporarily unavailable')).toBeInTheDocument();

    shouldFail = false;
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText('Recovered conversation')).toBeInTheDocument();
  });

  it('ignores an older response that arrives after the current query', async () => {
    let resolveInitial:
      | ((value: {
          items: ReturnType<typeof makeItem>[];
          total: number;
          page: number;
          page_size: number;
          has_more: boolean;
        }) => void)
      | undefined;
    mocks.search.mockImplementation(({ keyword }: { keyword: string }) => {
      if (!keyword) {
        return new Promise((resolve) => {
          resolveInitial = resolve;
        });
      }
      return Promise.resolve({
        items: [makeItem('conversation-current', 'Current result', 'message-current')],
        total: 1,
        page: 0,
        page_size: 30,
        has_more: false,
      });
    });

    await renderWithI18n(
      <ConversationSearchPopover renderTrigger={({ onClick }) => <button onClick={onClick}>open search</button>} />,
      'en-US',
      { wrapper: RouterWrapper }
    );
    fireEvent.click(screen.getByRole('button', { name: 'open search' }));
    await waitFor(() => expect(mocks.search).toHaveBeenCalledWith({ keyword: '', page: 0, page_size: 30 }));

    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'current' } });
    expect(await screen.findByRole('option', { name: /Current result/ })).toBeInTheDocument();
    resolveInitial?.({
      items: [makeItem('conversation-stale', 'Stale result')],
      total: 1,
      page: 0,
      page_size: 30,
      has_more: false,
    });

    await waitFor(() => expect(screen.queryByText('Stale result')).toBeNull());
    expect(screen.getByRole('option', { name: /Current result/ })).toBeInTheDocument();
  });
});
