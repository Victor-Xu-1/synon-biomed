import { describe, expect, it } from 'vitest';

import {
  isDeprecatedRuntimeAgentType,
  resolveSupportedConversationType,
} from '@/renderer/utils/synonBiomed/runtime/runtimeSupportPolicy';

describe('Guid agent support policy', () => {
  it('treats every non-Synon Biomed runtime value as retired', () => {
    expect(isDeprecatedRuntimeAgentType('synonbiomed')).toBe(false);
    expect(isDeprecatedRuntimeAgentType('acp')).toBe(true);
    expect(isDeprecatedRuntimeAgentType('unsupportedRuntime')).toBe(true);
    expect(isDeprecatedRuntimeAgentType('openclaw-gateway')).toBe(true);
    expect(isDeprecatedRuntimeAgentType('nanobot')).toBe(true);
    expect(isDeprecatedRuntimeAgentType('remote')).toBe(true);
    expect(isDeprecatedRuntimeAgentType('gemini')).toBe(true);
  });

  it('resolves supported top-level conversation type from backend labels', () => {
    expect(resolveSupportedConversationType('unsupportedRuntime')).toBe('acp');
    expect(resolveSupportedConversationType('synonbiomed')).toBe('acp');
    expect(resolveSupportedConversationType('claude')).toBe('acp');
    expect(resolveSupportedConversationType('gemini')).toBe('acp');
    expect(resolveSupportedConversationType('openclaw-gateway')).toBe('acp');
    expect(resolveSupportedConversationType('openclaw')).toBe('acp');
  });
});
