import { classifyConfigSetError } from '@/renderer/hooks/synonBiomed/runtime/useAcpConfigOptions';
import { parseError } from '@/common/utils';
import { redactErrorText } from './errorDiagnostics';

export const configErrorMessageKey = (error: unknown) => {
  const errorKind = classifyConfigSetError(error);
  if (errorKind === 'command_ack') return 'agent.config.commandAck';
  if (errorKind === 'confirmation_timeout') return 'agent.config.timeout';
  if (errorKind === 'config_update_in_progress') return 'agent.config.busy';
  return 'agent.config.failed';
};

export const safeErrorDiagnostic = (error: unknown): string => redactErrorText(parseError(error) || 'unknown_error');
