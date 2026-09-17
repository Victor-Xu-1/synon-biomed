import { act, renderHook, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const askAside = vi.fn();
vi.mock('@/renderer/services/synonBiomedAside', () => ({
  askSynonBiomedAsideQuestion: (...args: unknown[]) => askAside(...args),
}));
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock('@arco-design/web-react', () => ({
  Message: { info: vi.fn(), success: vi.fn(), warning: vi.fn(), error: vi.fn() },
}));

import { useBtwCommand } from '@/renderer/components/chat/BtwOverlay/useBtwCommand';

describe('useBtwCommand', () => {
  beforeEach(() => askAside.mockReset());

  it('uses a hidden Synon Biomed aside and aborts an in-flight question when dismissed', async () => {
    askAside.mockResolvedValueOnce({ status: 'ok', answer: 'Evidence answer', frameId: 'aside-1' });
    const { result } = renderHook(() => useBtwCommand('root-1'));

    await act(async () => result.current.ask('Check evidence'));
    await waitFor(() => expect(result.current.answer).toBe('Evidence answer'));
    expect(askAside).toHaveBeenCalledWith('root-1', 'Check evidence', {
      signal: expect.any(AbortSignal),
    });

    let capturedSignal: AbortSignal | undefined;
    askAside.mockImplementationOnce((_frameId, _question, options) => {
      capturedSignal = options.signal;
      return new Promise(() => undefined);
    });
    act(() => void result.current.ask('Long question'));
    await waitFor(() => expect(capturedSignal).toBeDefined());
    act(() => result.current.dismiss());
    expect(capturedSignal?.aborted).toBe(true);
    expect(result.current.isOpen).toBe(false);
  });
});
