import { act, fireEvent, screen } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import SynonBiomedApprovalCard from '@/renderer/components/synonBiomed/runtime/SynonBiomedApprovalCard';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@/renderer/hooks/context/LayoutContext', () => ({
  useLayoutContext: () => ({ isMobile: false }),
}));

const request = {
  requestId: 'request-exec-1',
  toolId: 'toolu_exec_1',
  kind: 'local_exec',
  tool: 'python',
  code: 'print("approval required")',
  description: null,
  environment: 'python',
  mode: 'live',
  questions: [],
};

describe('SynonBiomedApprovalCard', () => {
  it('never auto-allows and requires an explicit once-only click', async () => {
    vi.useFakeTimers();
    try {
      const onAllow = vi.fn();
      await renderWithI18n(
        <SynonBiomedApprovalCard
          request={request}
          queueIndex={1}
          queueTotal={1}
          busy={false}
          onAllow={onAllow}
          onDeny={vi.fn()}
        />
      );

      await act(async () => vi.advanceTimersByTimeAsync(60_000));
      expect(onAllow).not.toHaveBeenCalled();

      fireEvent.click(screen.getByRole('button', { name: '允许 本次' }));
      expect(onAllow).toHaveBeenCalledOnce();
      expect(onAllow).toHaveBeenCalledWith('once');
    } finally {
      vi.useRealTimers();
    }
  });

  it('resets a broader scope before a consecutive approval request', async () => {
    const onAllow = vi.fn();
    const view = await renderWithI18n(
      <SynonBiomedApprovalCard
        request={request}
        queueIndex={1}
        queueTotal={1}
        busy={false}
        onAllow={onAllow}
        onDeny={vi.fn()}
      />
    );

    fireEvent.click(screen.getByRole('button', { name: '更改允许范围' }));
    fireEvent.click(screen.getByRole('menuitemradio', { name: '全局 跨所有项目记住' }));
    expect(screen.getByRole('button', { name: '允许 全局' })).toBeInTheDocument();

    view.rerender(
      <SynonBiomedApprovalCard
        request={{ ...request, requestId: 'request-exec-2', toolId: 'toolu_exec_2' }}
        queueIndex={1}
        queueTotal={1}
        busy={false}
        onAllow={onAllow}
        onDeny={vi.fn()}
      />
    );

    expect(await screen.findByRole('button', { name: '允许 本次' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '允许 本次' }));
    expect(onAllow).toHaveBeenCalledOnce();
    expect(onAllow).toHaveBeenCalledWith('once');
  });

  it('offers only backend-supported scopes for rememberable agent tools', async () => {
    await renderWithI18n(
      <SynonBiomedApprovalCard
        request={{
          ...request,
          kind: 'agent_tool',
          tool: 'edit_file',
          target: 'evidence.csv',
          rememberable: true,
        }}
        queueIndex={1}
        queueTotal={1}
        busy={false}
        onAllow={vi.fn()}
        onDeny={vi.fn()}
      />
    );

    fireEvent.click(screen.getByRole('button', { name: '更改允许范围' }));
    expect(screen.getAllByRole('menuitemradio')).toHaveLength(2);
    expect(screen.getByRole('menuitemradio', { name: '本次 仅本次调用' })).toBeInTheDocument();
    expect(screen.getByRole('menuitemradio', { name: '全局 跨所有项目记住' })).toBeInTheDocument();
    expect(screen.queryByRole('menuitemradio', { name: '本会话 直到本次会话结束' })).not.toBeInTheDocument();
    expect(screen.queryByRole('menuitemradio', { name: '本项目 在本项目中记住' })).not.toBeInTheDocument();
  });
});
