export type SendBoxPrimaryActionInput = {
  loading: boolean;
  hasStopHandler: boolean;
  allowSendWhileLoading: boolean;
  compactActions: boolean;
  hasDraftToSend: boolean;
  disabled: boolean;
  uploading: boolean;
};

export function resolveSendBoxPrimaryAction(input: SendBoxPrimaryActionInput): 'send' | 'stop' {
  if (!input.loading || !input.hasStopHandler) return 'send';
  if (!input.allowSendWhileLoading) return 'stop';
  return input.compactActions || !input.hasDraftToSend || input.disabled || input.uploading ? 'stop' : 'send';
}
