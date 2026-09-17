/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { renderHook } from '@testing-library/react';
import { describe, expect, it, vi, beforeEach } from 'vitest';
import type { TChatConversation } from '@/common/config/storage';
import {
  resolveAssistantConfigId,
  usePresetAssistantInfo,
} from '@/renderer/hooks/synonBiomed/runtime/usePresetAssistantInfo';

const useSWRMock = vi.fn();
let currentLanguage = 'en-US';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    i18n: { language: currentLanguage },
  }),
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    assistants: {
      list: { invoke: vi.fn() },
    },
    extensions: {
      getAcpAdapters: { invoke: vi.fn() },
    },
    remoteAgent: {
      get: { invoke: vi.fn() },
    },
  },
}));

vi.mock('@/renderer/utils/platform', () => ({
  resolveExtensionAssetUrl: (value: string | undefined) => value,
  resolveBackendAssetUrl: (value: string | undefined) => value,
}));

vi.mock('swr', () => ({
  __esModule: true,
  default: (...args: unknown[]) => useSWRMock(...args),
}));

describe('usePresetAssistantInfo', () => {
  beforeEach(() => {
    useSWRMock.mockReset();
    currentLanguage = 'en-US';
  });

  it('prefers explicit Synon Biomed preset assistant identity over runtime metadata', () => {
    useSWRMock.mockImplementation((key: unknown) => {
      if (key === 'assistants') {
        return {
          data: [
            {
              id: 'assistant-social',
              name: 'Social Job Publisher',
              avatar: 'http://127.0.0.1:56663/api/assistants/social-job-publisher/avatar',
              name_i18n: {},
            },
          ],
          isLoading: false,
        };
      }
      if (key === 'extensions.acpAdapters') return { data: [], isLoading: false };
      return { data: undefined, isLoading: false };
    });

    const conversation = makeConversation({
      agent_id: 'runtime-social',
      preset_assistant_id: 'assistant-social',
      backend: 'gemini',
    });

    const { result } = renderHook(() => usePresetAssistantInfo(conversation));

    expect(result.current.info).toEqual({
      name: 'Social Job Publisher',
      logo: 'http://127.0.0.1:56663/api/assistants/social-job-publisher/avatar',
      isEmoji: false,
      backend: undefined,
      assistantId: 'assistant-social',
    });
  });

  it('prefers localized assistant names for the active language', () => {
    currentLanguage = 'zh-CN';

    useSWRMock.mockImplementation((key: unknown) => {
      if (key === 'assistants') {
        return {
          data: [
            {
              id: 'academic-paper',
              name: 'Academic Paper',
              avatar: '📚',
              name_i18n: {
                'zh-CN': '学术论文助手',
              },
            },
          ],
          isLoading: false,
        };
      }
      if (key === 'extensions.acpAdapters') return { data: [], isLoading: false };
      return { data: undefined, isLoading: false };
    });

    const conversation = makeConversation({
      assistant_id: 'academic-paper',
      preset_assistant_id: 'academic-paper',
      backend: 'claude',
    });

    const { result } = renderHook(() => usePresetAssistantInfo(conversation));

    expect(result.current.info).toEqual({
      name: '学术论文助手',
      logo: '📚',
      isEmoji: true,
      backend: undefined,
      assistantId: 'academic-paper',
    });
  });

  it('prefers explicit conversation assistant payload before catalog fallbacks', () => {
    useSWRMock.mockImplementation((key: unknown) => {
      if (key === 'assistants') return { data: [], isLoading: false };
      if (key === 'extensions.acpAdapters') return { data: [], isLoading: false };
      return { data: undefined, isLoading: false };
    });

    const conversation = {
      ...makeConversation({
        assistant_id: 'assistant-social',
        backend: 'claude',
      }),
      assistant: {
        id: 'assistant-social',
        source: 'generated',
        name: 'Social Job Publisher',
        avatar: '/api/assistants/assistant-social/avatar',
        backend: 'gemini',
      },
    } as TChatConversation;

    const { result } = renderHook(() => usePresetAssistantInfo(conversation));

    expect(result.current.info).toEqual({
      name: 'Social Job Publisher',
      logo: '/api/assistants/assistant-social/avatar',
      isEmoji: false,
      backend: 'gemini',
      assistantId: 'assistant-social',
    });
  });

  it('restores local absolute assistant snapshot avatars from the backend assistant catalog', () => {
    useSWRMock.mockImplementation((key: unknown) => {
      if (key === 'assistants') {
        return {
          data: [
            {
              id: 'assistant-local-avatar',
              name: 'Local Avatar',
              avatar: '/api/assistants/assistant-local-avatar/avatar',
              name_i18n: {},
            },
          ],
          isLoading: false,
        };
      }
      if (key === 'extensions.acpAdapters') return { data: [], isLoading: false };
      return { data: undefined, isLoading: false };
    });

    const conversation = {
      ...makeConversation({
        assistant_id: 'assistant-local-avatar',
        backend: 'codex',
      }),
      assistant: {
        id: 'assistant-local-avatar',
        source: 'user',
        name: 'Local Avatar',
        avatar: '/Users/demo/.synon-ai/assistant-avatars/assistant-local-avatar.jpg',
        backend: 'codex',
      },
    } as TChatConversation;

    const { result } = renderHook(() => usePresetAssistantInfo(conversation));

    expect(result.current.info).toEqual({
      name: 'Local Avatar',
      logo: '/api/assistants/assistant-local-avatar/avatar',
      isEmoji: false,
      backend: undefined,
      assistantId: 'assistant-local-avatar',
    });
  });

  it('does not expose local absolute assistant snapshot avatars when the catalog cannot restore them', () => {
    useSWRMock.mockImplementation((key: unknown) => {
      if (key === 'assistants') return { data: [], isLoading: false };
      if (key === 'extensions.acpAdapters') return { data: [], isLoading: false };
      return { data: undefined, isLoading: false };
    });

    const conversation = {
      ...makeConversation({
        assistant_id: 'assistant-local-avatar',
        backend: 'codex',
      }),
      assistant: {
        id: 'assistant-local-avatar',
        source: 'user',
        name: 'Local Avatar',
        avatar: '/Users/demo/.synon-ai/assistant-avatars/assistant-local-avatar.jpg',
        backend: 'codex',
      },
    } as TChatConversation;

    const { result } = renderHook(() => usePresetAssistantInfo(conversation));

    expect(result.current.info).toEqual({
      name: 'Local Avatar',
      logo: '',
      isEmoji: false,
      isFallback: true,
      backend: 'codex',
      assistantId: 'assistant-local-avatar',
    });
  });

  it('returns assistant fallback for generated assistants whose avatar is empty', () => {
    useSWRMock.mockImplementation((key: unknown) => {
      if (key === 'assistants') return { data: [], isLoading: false };
      if (key === 'extensions.acpAdapters') return { data: [], isLoading: false };
      return { data: undefined, isLoading: false };
    });

    const conversation = {
      ...makeConversation({
        assistant_id: 'bare-codex',
        backend: 'codex',
      }),
      assistant: {
        id: 'bare-codex',
        source: 'generated',
        name: 'codex',
        avatar: '',
        backend: 'codex',
      },
    } as TChatConversation;

    const { result } = renderHook(() => usePresetAssistantInfo(conversation));

    expect(result.current.info).toEqual({
      name: 'codex',
      logo: '',
      isEmoji: false,
      isFallback: true,
      backend: 'codex',
      assistantId: 'bare-codex',
    });
  });

  it('includes preset assistant backend when the assistant catalog resolves an identity', () => {
    useSWRMock.mockImplementation((key: unknown) => {
      if (key === 'assistants') {
        return {
          data: [
            {
              id: 'assistant-social',
              name: 'Social Job Publisher',
              avatar: '🦜',
              agent_id: 'agent-gemini',
              agent: { type: 'acp', source: 'builtin', acp_backend: 'gemini' },
              name_i18n: {},
            },
          ],
          isLoading: false,
        };
      }
      if (key === 'extensions.acpAdapters') return { data: [], isLoading: false };
      return { data: undefined, isLoading: false };
    });

    const conversation = makeConversation({
      assistant_id: 'assistant-social',
      backend: 'claude',
    });

    const { result } = renderHook(() => usePresetAssistantInfo(conversation));

    expect(result.current.info).toEqual({
      name: 'Social Job Publisher',
      logo: '🦜',
      isEmoji: true,
      backend: 'gemini',
      assistantId: 'assistant-social',
    });
  });

  it('does not fall back to runtime metadata when no Synon Biomed assistant identity exists', () => {
    useSWRMock.mockImplementation((key: unknown) => {
      if (key === 'assistants') return { data: [], isLoading: false };
      if (key === 'extensions.acpAdapters') return { data: [], isLoading: false };
      return { data: undefined, isLoading: false };
    });

    const conversation = makeConversation({
      agent_id: 'runtime-social',
      agent_name: 'Gemini Runtime',
      backend: 'gemini',
    });

    const { result } = renderHook(() => usePresetAssistantInfo(conversation));

    expect(result.current.info).toBeNull();
  });

  it('ignores legacy custom agent row ids when no Synon Biomed assistant identity exists', () => {
    useSWRMock.mockImplementation((key: unknown) => {
      if (key === 'assistants') return { data: [], isLoading: false };
      if (key === 'extensions.acpAdapters') return { data: [], isLoading: false };
      return { data: undefined, isLoading: false };
    });

    const conversation = makeConversation({
      custom_agent_id: 'runtime-social',
      agent_name: 'Gemini Runtime',
      backend: 'gemini',
    });

    const { result } = renderHook(() => usePresetAssistantInfo(conversation));

    expect(result.current.info).toBeNull();
  });

  it('does not fall back to runtime metadata when an explicit assistant identity no longer resolves', () => {
    useSWRMock.mockImplementation((key: unknown) => {
      if (key === 'assistants') return { data: [], isLoading: false };
      if (key === 'extensions.acpAdapters') return { data: [], isLoading: false };
      return { data: undefined, isLoading: false };
    });

    const conversation = makeConversation({
      assistant_id: 'assistant-deleted',
      custom_agent_id: 'runtime-social',
      agent_name: 'Gemini Runtime',
      backend: 'gemini',
    });

    const { result } = renderHook(() => usePresetAssistantInfo(conversation));

    expect(result.current.info).toBeNull();
  });

  it('does not revive a different assistant from legacy custom agent fields', () => {
    useSWRMock.mockImplementation((key: unknown) => {
      if (key === 'assistants') {
        return {
          data: [
            {
              id: 'assistant-legacy',
              name: 'Legacy Planner',
              avatar: '🧭',
              name_i18n: {},
            },
          ],
          isLoading: false,
        };
      }
      if (key === 'extensions.acpAdapters') return { data: [], isLoading: false };
      return { data: undefined, isLoading: false };
    });

    const conversation = makeConversation({
      assistant_id: 'assistant-deleted',
      custom_agent_id: 'assistant-legacy',
      agent_name: 'Gemini Runtime',
      backend: 'gemini',
    });

    const { result } = renderHook(() => usePresetAssistantInfo(conversation));

    expect(result.current.info).toBeNull();
  });

  it('falls back from a stale assistant_id to a valid preset_assistant_id', () => {
    useSWRMock.mockImplementation((key: unknown) => {
      if (key === 'assistants') {
        return {
          data: [
            {
              id: 'assistant-modern',
              name: 'Modern Planner',
              avatar: '🧭',
              name_i18n: {},
            },
          ],
          isLoading: false,
        };
      }
      if (key === 'extensions.acpAdapters') return { data: [], isLoading: false };
      return { data: undefined, isLoading: false };
    });

    const conversation = makeConversation({
      assistant_id: 'assistant-deleted',
      preset_assistant_id: 'assistant-modern',
      agent_name: 'Gemini Runtime',
      backend: 'gemini',
    });

    const { result } = renderHook(() => usePresetAssistantInfo(conversation));

    expect(result.current.info).toEqual({
      name: 'Modern Planner',
      logo: '🧭',
      isEmoji: true,
      assistantId: 'assistant-modern',
    });
  });

  it('infers assistant info from Synon Biomed preset payload without custom agent ids', () => {
    useSWRMock.mockImplementation((key: unknown) => {
      if (key === 'assistants') {
        return {
          data: [
            {
              id: 'assistant-legacy',
              name: 'Legacy Planner',
              avatar: '🧭',
              name_i18n: {},
            },
          ],
          isLoading: false,
        };
      }
      if (key === 'extensions.acpAdapters') return { data: [], isLoading: false };
      return { data: undefined, isLoading: false };
    });

    const conversation = makeConversation({
      backend: 'claude',
      preset_context: '# Legacy Planner',
    });

    const { result } = renderHook(() => usePresetAssistantInfo(conversation));

    expect(result.current.info).toEqual({
      name: 'Legacy Planner',
      logo: '🧭',
      isEmoji: true,
      assistantId: 'assistant-legacy',
    });
  });

  it('does not fall back to a backend name when legacy runtime rows lack assistant identity', () => {
    useSWRMock.mockImplementation((key: unknown) => {
      if (key === 'assistants') return { data: [], isLoading: false };
      if (key === 'extensions.acpAdapters') return { data: [], isLoading: false };
      return { data: undefined, isLoading: false };
    });

    const conversation = makeConversation({
      agent_id: 'runtime-social',
      backend: 'openclaw-gateway',
    });

    const { result } = renderHook(() => usePresetAssistantInfo(conversation));

    expect(result.current.info).toBeNull();
  });

  it('treats legacy custom_agent_id as runtime-only when resolving explicit assistant identity', () => {
    expect(
      resolveAssistantConfigId(
        makeConversation({
          custom_agent_id: 'runtime-social',
        })
      )
    ).toBeNull();

    expect(
      resolveAssistantConfigId(
        makeConversation({
          preset_assistant_id: 'assistant-modern',
          custom_agent_id: 'runtime-social',
        })
      )
    ).toBe('assistant-modern');
  });
});

function makeConversation(extra: Record<string, unknown>): TChatConversation {
  return {
    id: 'conv-1',
    user_id: 'user-1',
    name: '测试',
    type: 'acp',
    model: {},
    extra,
    status: 'finished',
    source: 'synon-ai',
    created_at: 1,
    modified_at: 1,
    pinned: false,
  } as TChatConversation;
}
