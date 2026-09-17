import { act, renderHook } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const { navigateMock, prefetchConversationRouteMock, warmConversationNavigationMock } = vi.hoisted(() => ({
  navigateMock: vi.fn(),
  prefetchConversationRouteMock: vi.fn().mockResolvedValue(undefined),
  warmConversationNavigationMock: vi.fn().mockResolvedValue(undefined),
}));

vi.mock('react-router', () => ({
  useNavigate: () => navigateMock,
  useParams: () => ({ id: 'conversation-current' }),
}));
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
vi.mock('@/renderer/hooks/context/AuthContext', () => ({
  useAuth: () => ({ user: { id: 'owner-a' } }),
}));
vi.mock('@/renderer/pages/conversation/conversationRoute', () => ({
  prefetchConversationRoute: prefetchConversationRouteMock,
  warmConversationNavigation: warmConversationNavigationMock,
}));
vi.mock('@/renderer/pages/conversation/conversationRoutePerformance', () => ({
  beginConversationRoutePerformance: vi.fn(),
}));
vi.mock('@/renderer/utils/ui/focus', () => ({
  blockMobileInputFocus: vi.fn(),
  blurActiveElement: vi.fn(),
}));

import type { TChatConversation } from '@/common/config/storage';
import { useConversationActions } from '@/renderer/pages/conversation/GroupedHistory/hooks/useConversationActions';

const conversation: TChatConversation = {
  id: 'conversation-target',
  type: 'acp',
  name: 'Target',
  desc: '',
  created_at: 1,
  modified_at: 1,
  status: 'finished',
  extra: { backend: 'synonbiomed' },
};

describe('useConversationActions route intent', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('keeps pointer intent lightweight and starts exactly the mounted message request on click', async () => {
    let releaseWarmup: (() => void) | undefined;
    warmConversationNavigationMock.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          releaseWarmup = resolve;
        })
    );
    const { result } = renderHook(() =>
      useConversationActions({
        batchMode: false,
        selectedConversationIds: new Set(),
        setSelectedConversationIds: vi.fn(),
        toggleSelectedConversation: vi.fn(),
      })
    );

    act(() => result.current.handleConversationPrefetch(conversation));
    expect(prefetchConversationRouteMock).toHaveBeenCalledOnce();
    expect(warmConversationNavigationMock).not.toHaveBeenCalled();

    act(() => result.current.handleConversationClick(conversation));
    expect(warmConversationNavigationMock).toHaveBeenCalledOnce();
    expect(warmConversationNavigationMock).toHaveBeenCalledWith({
      ownerId: 'owner-a',
      conversationId: 'conversation-target',
      summary: conversation,
    });
    expect(navigateMock).toHaveBeenCalledWith('/conversation/conversation-target');
    expect(releaseWarmup).toBeTypeOf('function');
    await act(async () => releaseWarmup?.());
  });
});
