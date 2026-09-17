/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 *
 * Unit tests for common/adapter/apiModelMapper.ts (T1 in N3 test checklist).
 * Tests model transformation between frontend and backend formats.
 */

import { describe, it, expect } from 'vitest';
import {
  fromApiModel,
  fromApiConversation,
  fromApiPaginatedConversations,
  buildCreateConversationBody,
  type ApiProviderWithModel,
} from '@/common/adapter/apiModelMapper';
import type { TProviderWithModel } from '@/common/config/storage';

describe('apiModelMapper', () => {
  describe('buildCreateConversationBody', () => {
    const unsupportedRuntimeModel = {
      id: 'openai',
      use_model: 'gemini-2.5-pro',
      platform: 'openai',
      name: 'x',
      base_url: 'y',
      api_key: 'z',
    } as TProviderWithModel;

    it('omits unsupported runtime top-level models for assistant-first creates', () => {
      const body = buildCreateConversationBody({
        name: 'hello',
        model: unsupportedRuntimeModel,
        assistant: { id: 'bare:unsupportedRuntime' },
        extra: { workspace: '' },
      } as Parameters<typeof buildCreateConversationBody>[0] & { model: TProviderWithModel });

      expect(body.type).toBeUndefined();
      expect('model' in body).toBe(false);
    });

    it('omits type for assistant-first ACP creates', () => {
      const body = buildCreateConversationBody({
        name: 'hello',
        assistant: { id: 'bare:claude' },
        extra: {},
      });

      expect(body.type).toBeUndefined();
      expect('model' in body).toBe(false);
    });

    it('strips legacy type when assistant identity is present', () => {
      const body = buildCreateConversationBody({
        type: 'acp',
        name: 'hello',
        assistant: { id: 'bare:claude' },
        extra: {},
      });

      expect(body.type).toBeUndefined();
    });

    it('omits the top-level model for ACP creates that pass an empty placeholder', () => {
      const body = buildCreateConversationBody({
        name: 'hello',
        model: {} as TProviderWithModel,
        assistant: { id: 'bare:claude' },
        extra: {},
      } as Parameters<typeof buildCreateConversationBody>[0] & { model: TProviderWithModel });

      expect(body.type).toBeUndefined();
      expect('model' in body).toBe(false);
    });

    it('omits the top-level model when no model is provided', () => {
      const body = buildCreateConversationBody({
        name: 'hello',
        assistant: { id: 'bare:claude' },
        extra: {},
      });

      expect('model' in body).toBe(false);
    });
  });

  describe('fromApiModel', () => {
    it('maps backend format to frontend and fills empty provider fields', () => {
      const input: ApiProviderWithModel = {
        provider_id: 'p',
        model: 'm',
      };

      const result = fromApiModel(input);

      expect(result).toEqual({
        id: 'p',
        platform: '',
        name: '',
        base_url: '',
        api_key: '',
        use_model: 'm',
      });
    });

    it('uses use_model field when present instead of model', () => {
      const input: ApiProviderWithModel = {
        provider_id: 'p',
        model: 'm-fallback',
        use_model: 'm-primary',
      };

      const result = fromApiModel(input);

      expect(result.use_model).toBe('m-primary');
    });

    it('falls back to model field when use_model is not present', () => {
      const input: ApiProviderWithModel = {
        provider_id: 'p',
        model: 'm-fallback',
      };

      const result = fromApiModel(input);

      expect(result.use_model).toBe('m-fallback');
    });
  });

  describe('fromApiConversation', () => {
    it('transforms model from ApiProviderWithModel to TProviderWithModel', () => {
      const raw = {
        id: 'conv1',
        model: {
          provider_id: 'p',
          model: 'm',
        } as ApiProviderWithModel,
      };

      const result = fromApiConversation(raw);

      expect(result.model).toEqual({
        id: 'p',
        platform: '',
        name: '',
        base_url: '',
        api_key: '',
        use_model: 'm',
      });
    });

    it('handles missing model field', () => {
      const raw = {
        id: 'conv1',
      };

      const result = fromApiConversation(raw);

      expect(result.model).toBeUndefined();
    });

    it('handles null model field', () => {
      const raw = {
        id: 'conv1',
        model: null,
      };

      const result = fromApiConversation(raw);

      expect(result.model).toBeUndefined();
    });

    it('infers custom_workspace=true when workspace is non-empty and not temporary', () => {
      const raw = {
        id: 'conv1',
        extra: {
          workspace: '/tmp',
          is_temporary_workspace: false,
        },
      };

      const result = fromApiConversation(raw);

      expect(result.extra?.custom_workspace).toBe(true);
    });

    it('infers custom_workspace=false when is_temporary_workspace=true', () => {
      const raw = {
        id: 'conv1',
        extra: {
          workspace: '/tmp',
          is_temporary_workspace: true,
        },
      };

      const result = fromApiConversation(raw);

      expect(result.extra?.custom_workspace).toBe(false);
    });

    it('infers custom_workspace=false when workspace is empty string', () => {
      const raw = {
        id: 'conv1',
        extra: {
          workspace: '',
          is_temporary_workspace: false,
        },
      };

      const result = fromApiConversation(raw);

      expect(result.extra?.custom_workspace).toBe(false);
    });

    it('does not overwrite existing custom_workspace field', () => {
      const raw = {
        id: 'conv1',
        extra: {
          workspace: '/tmp',
          is_temporary_workspace: false,
          custom_workspace: true,
        },
      };

      const result = fromApiConversation(raw);

      // Should preserve the existing value
      expect(result.extra?.custom_workspace).toBe(true);
    });

    it('returns input unchanged for non-object types', () => {
      expect(fromApiConversation(null)).toBeNull();
      expect(fromApiConversation(undefined)).toBeUndefined();
      expect(fromApiConversation('string')).toBe('string');
      expect(fromApiConversation(123)).toBe(123);
    });
  });

  describe('fromApiPaginatedConversations', () => {
    it('maps items through fromApiConversation and preserves total/has_more', () => {
      const input = {
        items: [
          {
            id: 'conv1',
            model: {
              provider_id: 'p1',
              model: 'm1',
            } as ApiProviderWithModel,
          },
          {
            id: 'conv2',
          },
        ],
        total: 2,
        has_more: false,
      };

      const result = fromApiPaginatedConversations(input);

      expect(result.total).toBe(2);
      expect(result.has_more).toBe(false);
      expect(result.items).toHaveLength(2);
      expect(result.items[0].model).toEqual({
        id: 'p1',
        platform: '',
        name: '',
        base_url: '',
        api_key: '',
        use_model: 'm1',
      });
      expect(result.items[1].model).toBeUndefined();
    });

    it('handles empty items array', () => {
      const input = {
        items: [],
        total: 0,
        has_more: false,
      };

      const result = fromApiPaginatedConversations(input);

      expect(result.items).toHaveLength(0);
      expect(result.total).toBe(0);
      expect(result.has_more).toBe(false);
    });
  });
});
