import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ httpRequest: vi.fn() }));

vi.mock('@/common/adapter/httpBridge', () => ({
  BackendHttpError: class BackendHttpError extends Error {
    status = 500;
    code = '';
  },
  httpRequest: mocks.httpRequest,
  isBackendHttpError: () => false,
}));

import {
  loadMessageChannelStatuses,
  pollMessageChannelQr,
  startMessageChannelQr,
  unpairMessageChannel,
} from '@/renderer/services/messageChannels';

describe('message channel service', () => {
  beforeEach(() => vi.clearAllMocks());

  it('maps status and QR start envelopes without retaining credentials', async () => {
    mocks.httpRequest
      .mockResolvedValueOnce({
        channels: {
          feishu: { configured: true, paired: true },
          wechat: { configured: false, paired: false },
        },
      })
      .mockResolvedValueOnce({
        ok: true,
        result: {
          sessionKey: 'session-1',
          qrCodeUrl: 'https://accounts.feishu.cn/device/qr.png',
          clientSecret: 'test-secret-must-not-be-returned',
        },
      });

    await expect(loadMessageChannelStatuses()).resolves.toEqual({
      feishu: { configured: true, paired: true },
      wechat: { configured: false, paired: false },
    });
    await expect(startMessageChannelQr('feishu', 'session-1')).resolves.toEqual({
      sessionKey: 'session-1',
      qrCodeUrl: 'https://accounts.feishu.cn/device/qr.png',
    });
  });

  it('normalizes provider scan states and rejects executable QR URLs', async () => {
    mocks.httpRequest.mockResolvedValueOnce({ result: { status: 'scaned', connected: false } });
    await expect(pollMessageChannelQr('wechat', 'session-1')).resolves.toEqual({
      status: 'scanned',
      connected: false,
    });

    mocks.httpRequest.mockResolvedValueOnce({ result: { sessionKey: 'session-1', qrCodeUrl: 'javascript:alert(1)' } });
    await expect(startMessageChannelQr('wechat', 'session-1')).rejects.toThrow('QR payload is incomplete');
  });

  it('validates the owner-scoped unpair response and keeps only safe fields', async () => {
    mocks.httpRequest.mockResolvedValueOnce({
      ok: true,
      result: { unpaired: true, pairedUsersRevoked: 1, restartScheduled: false, userId: 'must-not-return' },
    });

    await expect(unpairMessageChannel('feishu')).resolves.toEqual({
      unpaired: true,
      pairedUsersRevoked: 1,
      restartScheduled: false,
    });
    expect(mocks.httpRequest).toHaveBeenCalledWith(
      'POST',
      '/api/adapters/message-channels/unpair',
      { channel: 'feishu' },
      { timeoutMs: 40_000 }
    );
  });
});
