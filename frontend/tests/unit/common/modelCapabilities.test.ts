import { describe, expect, it } from 'vitest';

import type { IProvider } from '@/common/config/storage';
import { hasSpecificModelCapability } from '@/common/utils/modelCapabilities';

const provider: IProvider = {
  id: 'provider-1',
  platform: 'custom',
  name: 'Custom provider',
  base_url: 'https://models.example.test/v1',
  api_key: '',
  models: [],
};

describe('model capability inference', () => {
  it('does not infer capabilities from Claude model identifiers', () => {
    expect(hasSpecificModelCapability(provider, 'claude-3-7-sonnet', 'text')).toBeUndefined();
    expect(hasSpecificModelCapability(provider, 'claude-3-7-sonnet', 'vision')).toBeUndefined();
    expect(hasSpecificModelCapability(provider, 'claude-3-7-sonnet', 'function_calling')).toBeUndefined();
  });

  it('keeps provider-neutral third-party model inference', () => {
    expect(hasSpecificModelCapability(provider, 'deepseek-v4-flash', 'text')).toBe(true);
    expect(hasSpecificModelCapability(provider, 'deepseek-v4-flash', 'function_calling')).toBe(true);
    expect(hasSpecificModelCapability(provider, 'qwen-vl-max', 'vision')).toBe(true);
  });
});
