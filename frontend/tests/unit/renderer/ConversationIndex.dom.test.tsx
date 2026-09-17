import { screen, waitFor } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';
import type { TChatConversation } from '@/common/config/storage';

const swrMock = vi.fn();
const navigateMock = vi.fn();
const mutateMock = vi.fn(async () => undefined);
const closePreviewMock = vi.fn();
const syncTitleMock = vi.fn();
let routeConversationId = 'frame-missing';
let listChangedListener:
  | ((event: { conversation_id: string; action: 'created' | 'updated' | 'deleted' }) => void)
  | undefined;
let reconnectedListener: (() => void) | undefined;

vi.mock('swr', () => ({
  default: (...args: unknown[]) => swrMock(...args),
}));

vi.mock('react-router', () => ({
  useNavigate: () => navigateMock,
  useParams: () => ({ id: routeConversationId }),
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    conversation: {
      listChanged: {
        on: (listener: typeof listChangedListener) => {
          listChangedListener = listener;
          return () => {
            if (listChangedListener === listener) listChangedListener = undefined;
          };
        },
      },
    },
  },
}));

vi.mock('@/renderer/pages/conversation/Preview/context/PreviewContext', () => ({
  usePreviewContext: () => ({ closePreview: closePreviewMock }),
}));

vi.mock('@/renderer/hooks/chat/useAutoTitle', () => ({
  useAutoTitle: () => ({ syncTitleFromHistory: syncTitleMock }),
}));

vi.mock('@/renderer/hooks/context/AuthContext', () => ({
  useAuth: () => ({ user: { id: 'owner-test' } }),
}));

vi.mock('@/renderer/hooks/context/RealtimeContext', () => ({
  useRealtime: () => ({
    runtime: {
      subscribeReconnected: (listener: () => void) => {
        reconnectedListener = listener;
        return () => {
          if (reconnectedListener === listener) reconnectedListener = undefined;
        };
      },
    },
  }),
}));

vi.mock('@/renderer/pages/conversation/components/ChatConversation', () => ({
  default: ({ conversation }: { conversation: { name: string } }) => <div>{conversation.name}</div>,
}));

import ChatConversationIndex from '@/renderer/pages/conversation';
import { rememberConversationRouteSnapshot } from '@/renderer/pages/conversation/utils/conversationCache';

describe('ChatConversationIndex unavailable states', () => {
  beforeEach(() => {
    swrMock.mockReset();
    navigateMock.mockClear();
    mutateMock.mockClear();
    closePreviewMock.mockClear();
    syncTitleMock.mockClear();
    routeConversationId = 'frame-missing';
    listChangedListener = undefined;
    reconnectedListener = undefined;
  });

  afterEach(() => vi.restoreAllMocks());

  it('shows a compact loading state while the initial conversation request is loading', async () => {
    swrMock.mockReturnValue({
      data: undefined,
      error: undefined,
      isLoading: true,
      mutate: mutateMock,
    });
    await renderWithI18n(<ChatConversationIndex />);

    expect(screen.getByTestId('conversation-loading-surface')).toHaveAttribute('aria-busy', 'true');
    expect(screen.getByTestId('conversation-loading-surface')).toHaveAttribute('role', 'status');
    expect(screen.getByTestId('conversation-loading-indicator')).toBeInTheDocument();
    expect(screen.queryByAltText('SYNON-Biomed')).not.toBeInTheDocument();
    expect(screen.queryByTestId('conversation-loading-composer')).not.toBeInTheDocument();
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
  });

  it('replaces a missing conversation with the clean new-task route', async () => {
    swrMock.mockReturnValue({
      data: null,
      error: undefined,
      isLoading: false,
      mutate: mutateMock,
    });
    await renderWithI18n(<ChatConversationIndex />);

    await expectGuidReplacement();
    expect(closePreviewMock).toHaveBeenCalledTimes(1);
    expect(screen.queryByTestId('conversation-load-error')).not.toBeInTheDocument();
  });

  it('keeps a transient initial failure mounted and revalidates on backend reconnect', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    swrMock.mockReturnValue({
      data: undefined,
      error: new Error('provider failed with key sk-ant-api03-shouldNotLeak123456'),
      isLoading: false,
      mutate: mutateMock,
    });
    await renderWithI18n(<ChatConversationIndex />);

    expect(navigateMock).not.toHaveBeenCalled();
    expect(closePreviewMock).not.toHaveBeenCalled();
    expect(screen.getByTestId('conversation-loading-surface')).toHaveAttribute('aria-busy', 'true');
    expect(screen.queryByTestId('conversation-load-error')).not.toBeInTheDocument();
    expect(screen.queryByText(/shouldNotLeak/)).not.toBeInTheDocument();
    expect(JSON.stringify(warn.mock.calls)).toContain('[REDACTED_KEY]');
    expect(JSON.stringify(warn.mock.calls)).not.toContain('shouldNotLeak');

    reconnectedListener?.();
    expect(mutateMock).toHaveBeenCalledTimes(1);
  });

  it('keeps cached conversation content visible when background revalidation fails', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    swrMock.mockReturnValue({
      data: { id: 'frame-cached', name: 'Cached session' },
      error: new Error('temporary network failure'),
      isLoading: false,
      mutate: mutateMock,
    });
    await renderWithI18n(<ChatConversationIndex />);

    expect(screen.getByText('Cached session')).toBeInTheDocument();
    expect(screen.queryByTestId('conversation-load-error')).not.toBeInTheDocument();
    expect(navigateMock).not.toHaveBeenCalled();
  });

  it('keeps the route snapshot visible while SWR revalidates it', async () => {
    swrMock.mockReturnValue({
      data: { id: 'frame-prefetched', name: 'Prefetched session' },
      error: undefined,
      isLoading: true,
      mutate: mutateMock,
    });
    await renderWithI18n(<ChatConversationIndex />);

    expect(screen.getByText('Prefetched session')).toBeInTheDocument();
    expect(screen.queryByTestId('conversation-loading-surface')).not.toBeInTheDocument();
  });

  it('renders a normal conversation without unavailable-state chrome', async () => {
    swrMock.mockReturnValue({
      data: { id: 'frame-ready', name: 'Ready session' },
      error: undefined,
      isLoading: false,
      mutate: mutateMock,
    });
    await renderWithI18n(<ChatConversationIndex />);

    expect(screen.getByText('Ready session')).toBeInTheDocument();
    expect(screen.queryByTestId('conversation-load-error')).not.toBeInTheDocument();
  });

  it('scopes the conversation detail cache to the authenticated owner', async () => {
    routeConversationId = 'frame-owner-scoped';
    swrMock.mockReturnValue({
      data: { id: routeConversationId, name: 'Owner scoped session' },
      error: undefined,
      isLoading: false,
      mutate: mutateMock,
    });
    await renderWithI18n(<ChatConversationIndex />);

    expect(swrMock).toHaveBeenCalledWith(
      'conversation/owner-test/frame-owner-scoped',
      expect.any(Function),
      expect.objectContaining({ revalidateOnMount: true })
    );
  });

  it('does not repeat the detail request after navigation just prefetched it', async () => {
    routeConversationId = 'frame-recent-detail';
    const conversation = { id: routeConversationId, name: 'Recently prefetched session' };
    rememberConversationRouteSnapshot('owner-test', conversation as TChatConversation, 'detail');
    swrMock.mockReturnValue({
      data: conversation,
      error: undefined,
      isLoading: false,
      mutate: mutateMock,
    });

    await renderWithI18n(<ChatConversationIndex />);

    expect(swrMock).toHaveBeenCalledWith(
      'conversation/owner-test/frame-recent-detail',
      expect.any(Function),
      expect.objectContaining({ revalidateOnMount: false })
    );
  });

  it('does not revalidate an active Synon Biomed detail for metadata updates', async () => {
    swrMock.mockReturnValue({
      data: {
        id: 'frame-running',
        name: 'Running chemistry task',
        extra: { backend: 'synonbiomed' },
        runtime: { is_processing: true, has_task: true },
      },
      error: undefined,
      isLoading: false,
      mutate: mutateMock,
    });
    routeConversationId = 'frame-running';
    await renderWithI18n(<ChatConversationIndex />);

    listChangedListener?.({ conversation_id: 'frame-running', action: 'updated' });

    expect(mutateMock).not.toHaveBeenCalled();
  });

  it('does not revalidate an active Synon Biomed detail for creation notifications', async () => {
    swrMock.mockReturnValue({
      data: {
        id: 'frame-running',
        name: 'Running chemistry task',
        extra: { backend: 'synonbiomed' },
        runtime: { is_processing: true, has_task: true },
      },
      error: undefined,
      isLoading: false,
      mutate: mutateMock,
    });
    routeConversationId = 'frame-running';
    await renderWithI18n(<ChatConversationIndex />);

    listChangedListener?.({ conversation_id: 'frame-running', action: 'created' });

    expect(mutateMock).not.toHaveBeenCalled();
  });

  it('preserves an open preview across same-conversation remount work and closes only on a route-id change', async () => {
    swrMock.mockReturnValue({
      data: { id: 'frame-missing', name: 'Ready session' },
      error: undefined,
      isLoading: false,
      mutate: mutateMock,
    });
    const view = await renderWithI18n(<ChatConversationIndex />);

    expect(closePreviewMock).not.toHaveBeenCalled();
    view.rerender(<ChatConversationIndex />);
    expect(closePreviewMock).not.toHaveBeenCalled();

    routeConversationId = 'frame-next';
    swrMock.mockReturnValue({
      data: { id: 'frame-next', name: 'Next session' },
      error: undefined,
      isLoading: false,
      mutate: mutateMock,
    });
    view.rerender(<ChatConversationIndex />);
    expect(closePreviewMock).toHaveBeenCalledTimes(1);
  });

  it('redirects a missing conversation independently of locale', async () => {
    swrMock.mockReturnValue({
      data: null,
      error: undefined,
      isLoading: false,
      mutate: mutateMock,
    });
    await renderWithI18n(<ChatConversationIndex />, 'en-US');

    await expectGuidReplacement();
    expect(screen.queryByText('Session deleted')).not.toBeInTheDocument();
  });
});

async function expectGuidReplacement() {
  await waitFor(() =>
    expect(navigateMock).toHaveBeenCalledWith('/guid', {
      replace: true,
      state: { resetAssistant: true },
    })
  );
}
