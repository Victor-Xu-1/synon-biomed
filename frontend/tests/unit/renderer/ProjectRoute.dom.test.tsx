import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { SynonBiomedProjectBench } from '@/renderer/services/synonBiomedGateway';
import { renderWithI18n } from '../i18nTestUtils';

const { loadBenchesMock, navigateMock } = vi.hoisted(() => ({
  loadBenchesMock: vi.fn(),
  navigateMock: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedGateway', () => ({
  loadSynonBiomedProjectBenches: (...args: unknown[]) => loadBenchesMock(...args),
}));

vi.mock('react-router', () => ({
  useNavigate: () => navigateMock,
  useParams: () => ({ projectId: 'project-a' }),
}));

vi.mock('@arco-design/web-react', () => ({
  Spin: () => <span>loading</span>,
  Button: ({ children, onClick }: { children: React.ReactNode; onClick?: () => void }) => (
    <button type='button' onClick={onClick}>
      {children}
    </button>
  ),
  Result: ({
    title,
    subTitle,
    extra,
  }: {
    title: React.ReactNode;
    subTitle: React.ReactNode;
    extra: React.ReactNode;
  }) => (
    <div>
      <h1>{title}</h1>
      <p>{subTitle}</p>
      {extra}
    </div>
  ),
}));

import ProjectRoute from '@/renderer/pages/project/ProjectRoute';

const bench = (frameId: string, updatedAt: string): SynonBiomedProjectBench => ({
  frameId,
  rootFrameId: frameId,
  parentFrameId: null,
  projectId: 'project-a',
  name: frameId,
  taskSummary: null,
  agentName: null,
  status: null,
  statusDescription: null,
  createdAt: updatedAt,
  updatedAt,
  completedAt: null,
  lastActivityAt: updatedAt,
  hasImageOutput: false,
});

describe('ProjectRoute', () => {
  beforeEach(() => {
    loadBenchesMock.mockReset();
    navigateMock.mockReset();
  });

  it('opens the most recently active conversation instead of the artifact workbench', async () => {
    loadBenchesMock.mockResolvedValue([
      bench('frame-older', '2026-07-10T00:00:00Z'),
      bench('frame-latest', '2026-07-12T00:00:00Z'),
    ]);

    await renderWithI18n(<ProjectRoute />, 'zh-CN');

    await waitFor(() =>
      expect(navigateMock).toHaveBeenCalledWith('/conversation/frame-latest', {
        replace: true,
      })
    );
  });

  it('opens a project-bound new task when the project has no conversations', async () => {
    loadBenchesMock.mockResolvedValue([]);

    await renderWithI18n(<ProjectRoute />, 'zh-CN');

    await waitFor(() =>
      expect(navigateMock).toHaveBeenCalledWith('/guid', {
        replace: true,
        state: {
          resetAssistant: true,
          workspace: 'synonbiomed://project/project-a',
        },
      })
    );
  });

  it('shows a recoverable error and retries the project lookup', async () => {
    loadBenchesMock.mockRejectedValueOnce(new Error('backend unavailable')).mockResolvedValueOnce([]);

    await renderWithI18n(<ProjectRoute />, 'zh-CN');

    const errorView = await screen.findByTestId('project-route-error');
    expect(errorView).toHaveTextContent('项目数据暂时不可用，请重试后继续。');
    expect(errorView).not.toHaveTextContent('backend unavailable');
    fireEvent.click(screen.getByRole('button', { name: '重试' }));

    await waitFor(() => expect(loadBenchesMock).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(navigateMock).toHaveBeenCalledWith('/guid', expect.any(Object)));
  });
});
