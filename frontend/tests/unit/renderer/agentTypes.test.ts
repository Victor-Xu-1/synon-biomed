import type { TFunction } from 'i18next';
import { describe, expect, it } from 'vitest';
import type { ManagedAgent } from '@/renderer/utils/synonBiomed/runtime/runtimeTypes';
import { formatManagedAgentDiagnosticMessage } from '@/renderer/utils/synonBiomed/runtime/runtimeTypes';

const t = ((key: string, options?: Record<string, unknown>) => {
  switch (key) {
    case 'settings.synonBiomedRuntime.errorCodes.command_not_found':
      return `Install ${String(options?.command)} and retry the connection test.`;
    case 'settings.synonBiomedRuntime.errorCodes.bridge_missing':
      return `Install ${String(options?.command)} and retry the connection test.`;
    case 'settings.synonBiomedRuntime.errorCodes.unknown':
      return `The health check for ${String(options?.name)} failed. Review the runtime logs and retry.`;
    default:
      return String(options?.defaultValue ?? key);
  }
}) as unknown as TFunction;

function managedAgent(overrides: Partial<ManagedAgent>): ManagedAgent {
  return {
    id: 'agent-1',
    name: 'AIDD Expert',
    agent_type: 'synonbiomed',
    agent_source: 'builtin',
    enabled: true,
    installed: true,
    status: 'unavailable',
    sort_order: 1,
    args: [],
    env: [],
    behavior_policy: {},
    ...overrides,
  } as ManagedAgent;
}

describe('formatManagedAgentDiagnosticMessage', () => {
  it('formats localized diagnostics from error code and details', () => {
    const message = formatManagedAgentDiagnosticMessage(
      t,
      managedAgent({
        last_check_error_code: 'command_not_found',
        last_check_error_details: { command: 'synonbiomed-runtime' },
        last_check_error_message: 'spawn failed',
      })
    );

    expect(message).toBe('Install synonbiomed-runtime and retry the connection test.');
  });

  it('uses a localized generic diagnostic when the code is unknown', () => {
    const message = formatManagedAgentDiagnosticMessage(
      t,
      managedAgent({
        last_check_error_code: 'unknown_error_code',
        last_check_error_message: 'raw backend message',
      })
    );

    expect(message).toBe('The health check for AIDD Expert failed. Review the runtime logs and retry.');
  });
});
