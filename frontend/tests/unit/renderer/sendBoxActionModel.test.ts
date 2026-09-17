import { resolveSendBoxPrimaryAction } from '@/renderer/components/chat/SendBox/actionModel';
import { describe, expect, it } from 'vitest';

const running = {
  loading: true,
  hasStopHandler: true,
  allowSendWhileLoading: true,
  compactActions: false,
  hasDraftToSend: false,
  disabled: false,
  uploading: false,
};

describe('SendBox action model', () => {
  it('never renders a dead stop action when cancellation is owned elsewhere', () => {
    expect(resolveSendBoxPrimaryAction({ ...running, hasStopHandler: false })).toBe('send');
  });

  it('shows stop for a running task only when this composer owns a stop handler', () => {
    expect(resolveSendBoxPrimaryAction(running)).toBe('stop');
    expect(resolveSendBoxPrimaryAction({ ...running, loading: false })).toBe('send');
  });

  it('keeps queued draft sending available while the task runs', () => {
    expect(resolveSendBoxPrimaryAction({ ...running, hasDraftToSend: true })).toBe('send');
    expect(resolveSendBoxPrimaryAction({ ...running, hasDraftToSend: true, compactActions: true })).toBe('stop');
  });
});
