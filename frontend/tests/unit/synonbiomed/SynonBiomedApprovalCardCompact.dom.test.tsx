import SynonBiomedApprovalCard from '@/renderer/components/synonBiomed/runtime/SynonBiomedApprovalCard';
import { fireEvent, screen } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const softwareRuntimeRequest = {
  requestId: 'approval-software-runtime',
  toolId: 'software-runtime-call',
  kind: 'local_exec',
  tool: 'software_runtime',
  code: 'python analysis.py',
  description: '运行分析脚本并生成报告',
  environment: 'swr-0cbb0c50952bd5ebca648c41',
  mode: 'live',
  questions: [],
};

const pythonRequest = {
  ...softwareRuntimeRequest,
  requestId: 'approval-python',
  toolId: 'python-call',
  tool: 'python',
};

describe('SynonBiomedApprovalCard compact software runtime presentation', () => {
  it('shows the chosen scope and submits only one decision while its request is outstanding', async () => {
    const onAllow = vi.fn();
    const onDeny = vi.fn();
    await renderWithI18n(
      <SynonBiomedApprovalCard
        request={pythonRequest}
        queueIndex={1}
        queueTotal={1}
        busy={false}
        onAllow={onAllow}
        onDeny={onDeny}
      />,
      'zh-CN'
    );
    const button = screen.getByRole('button', { name: '允许 本次' });
    expect(button).toHaveTextContent('本次');
    fireEvent.click(button);
    fireEvent.click(button);
    fireEvent.click(screen.getByRole('button', { name: '拒绝' }));
    expect(onAllow).toHaveBeenCalledTimes(1);
    expect(onDeny).not.toHaveBeenCalled();
  });
  it('keeps the initial decision surface minimal and reveals details on demand', async () => {
    const onAllow = vi.fn();
    const onDeny = vi.fn();
    await renderWithI18n(
      <SynonBiomedApprovalCard
        request={softwareRuntimeRequest}
        queueIndex={1}
        queueTotal={1}
        busy={false}
        onAllow={onAllow}
        onDeny={onDeny}
      />,
      'zh-CN'
    );

    const card = screen.getByTestId('synon-approval-card');
    expect(card).toHaveClass('synon-approval-card--compact');
    expect(screen.getByText('运行 software runtime？')).toBeVisible();
    expect(screen.queryByRole('button', { name: '更改允许范围' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '允许 本次' })).toHaveTextContent('允许');
    expect(screen.getByRole('button', { name: '拒绝' })).toBeVisible();
    expect(screen.getByText('运行分析脚本并生成报告')).toBeVisible();
    expect(screen.getByText('swr-0cbb0c50952bd5ebca648c41')).not.toBeVisible();
    expect(screen.getByText('python analysis.py')).not.toBeVisible();
    expect(screen.getByText('在隔离环境中运行。')).not.toBeVisible();

    fireEvent.click(screen.getByText('更多'));
    expect(screen.getByText('swr-0cbb0c50952bd5ebca648c41')).toBeVisible();
    expect(screen.getByText('python analysis.py')).toBeVisible();
    expect(screen.getByText('在隔离环境中运行。')).toBeVisible();

    fireEvent.click(screen.getByRole('button', { name: '允许 本次' }));
    expect(onAllow).toHaveBeenCalledWith('once');
    expect(onDeny).not.toHaveBeenCalled();
  });

  it('uses the same compact surface for Python local execution requests', async () => {
    await renderWithI18n(
      <SynonBiomedApprovalCard
        request={pythonRequest}
        queueIndex={1}
        queueTotal={1}
        busy={false}
        onAllow={vi.fn()}
        onDeny={vi.fn()}
      />,
      'zh-CN'
    );

    const card = screen.getByTestId('synon-approval-card');
    expect(card).toHaveClass('synon-approval-card--compact');
    expect(screen.getByText('运行 Python 代码？')).toBeVisible();
    expect(screen.getByText('python analysis.py')).not.toBeVisible();
    expect(screen.getByText('更多')).toBeVisible();
  });
});
