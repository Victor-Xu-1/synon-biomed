import { describe, expect, it } from 'vitest';

import { isSlashCommandListEnabled } from '@/common/chat/slash/availability';

describe('slash command availability', () => {
  it('enables slash commands for ACP conversations only', () => {
    expect(isSlashCommandListEnabled({ conversation_type: 'acp' })).toBe(true);
    expect(isSlashCommandListEnabled({ conversation_type: 'unsupportedRuntime' })).toBe(false);
    expect(isSlashCommandListEnabled({ conversation_type: 'gemini' })).toBe(false);
  });

  it('rejects every retired conversation runtime', () => {
    expect(isSlashCommandListEnabled({ conversation_type: 'codex' })).toBe(false);
    expect(isSlashCommandListEnabled({ conversation_type: 'openclaw-gateway' })).toBe(false);
    expect(isSlashCommandListEnabled({ conversation_type: 'remote' })).toBe(false);
  });
});
