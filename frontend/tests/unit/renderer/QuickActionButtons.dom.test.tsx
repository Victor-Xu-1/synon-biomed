import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const PROJECTS = '\u9879\u76ee';
const SKILL_WORKBENCH = 'Skill \u6280\u80fd';
const MCP_WORKBENCH = 'MCP \u5de5\u5177';
const RUNTIME_HEALTH = '\u8fd0\u884c\u72b6\u6001 \u00b7 \u5065\u5eb7';
const FEEDBACK = '\u53cd\u9988\u95ee\u9898';
const STAR = '\u559c\u6b22\u6211\u4eec\uff1f\u70b9\u4e2a\u661f\u5427';

const {
  navigateMock,
  loadSynonBiomedCatalogSummaryMock,
  loadSynonBiomedProjectsMock,
  createSynonBiomedProjectMock,
  webuiGetStatusMock,
} = vi.hoisted(() => ({
  navigateMock: vi.fn(),
  loadSynonBiomedCatalogSummaryMock: vi.fn(),
  loadSynonBiomedProjectsMock: vi.fn(),
  createSynonBiomedProjectMock: vi.fn(),
  webuiGetStatusMock: vi.fn(),
}));

vi.mock('react-router', () => ({
  useNavigate: () => navigateMock,
}));

vi.mock('@/common/adapter/ipcBridge', () => ({
  systemSettings: {
    changeLanguage: { invoke: vi.fn().mockResolvedValue(undefined) },
    languageChanged: { on: vi.fn(() => vi.fn()) },
  },
  webui: {
    getStatus: { invoke: webuiGetStatusMock },
    statusChanged: { on: vi.fn(() => vi.fn()) },
  },
}));

vi.mock('@/renderer/services/synonBiomedCatalog', () => ({
  loadSynonBiomedCatalogSummary: loadSynonBiomedCatalogSummaryMock,
}));

vi.mock('@/renderer/services/synonBiomedGateway', () => ({
  loadSynonBiomedProjects: loadSynonBiomedProjectsMock,
  createSynonBiomedProject: createSynonBiomedProjectMock,
}));

import QuickActionButtons from '@/renderer/pages/guid/components/QuickActionButtons';

describe('QuickActionButtons', () => {
  beforeEach(() => {
    navigateMock.mockReset();
    loadSynonBiomedCatalogSummaryMock.mockReset();
    loadSynonBiomedProjectsMock.mockReset();
    createSynonBiomedProjectMock.mockReset();
    webuiGetStatusMock.mockReset();
  });

  it('renders Synon Biomed shortcuts instead of product feedback and GitHub actions', async () => {
    const onOpenLink = vi.fn();
    const onOpenBugReport = vi.fn();
    loadSynonBiomedCatalogSummaryMock.mockResolvedValue({
      product: 'Synon Biomed',
      backendStatus: 'healthy',
      counts: {
        backendAgents: 14,
      },
    });
    loadSynonBiomedProjectsMock.mockResolvedValue([
      {
        projectId: 'proj_example',
        name: 'Example project',
      },
    ]);

    await renderWithI18n(
      <QuickActionButtons
        onOpenLink={onOpenLink}
        onOpenBugReport={onOpenBugReport}
        inactiveBorderColor='transparent'
        activeShadow='none'
      />,
      'zh-CN'
    );

    await waitFor(() => {
      expect(screen.getByText(PROJECTS)).toBeInTheDocument();
      expect(screen.getByText(SKILL_WORKBENCH)).toBeInTheDocument();
      expect(screen.getByText(MCP_WORKBENCH)).toBeInTheDocument();
      expect(screen.getByText(RUNTIME_HEALTH)).toBeInTheDocument();
    });

    expect(screen.queryByText(FEEDBACK)).not.toBeInTheDocument();
    expect(screen.queryByText(STAR)).not.toBeInTheDocument();
    expect(webuiGetStatusMock).not.toHaveBeenCalled();

    fireEvent.click(screen.getByText(PROJECTS));
    await waitFor(() => {
      expect(navigateMock).toHaveBeenNthCalledWith(1, '/projects/proj_example');
    });

    fireEvent.click(screen.getByText(SKILL_WORKBENCH));
    fireEvent.click(screen.getByText(MCP_WORKBENCH));
    fireEvent.click(screen.getByText(RUNTIME_HEALTH));

    expect(navigateMock).toHaveBeenNthCalledWith(2, '/settings/skills');
    expect(navigateMock).toHaveBeenNthCalledWith(3, '/settings/tools');
    expect(navigateMock).toHaveBeenNthCalledWith(4, '/settings/compute');
    expect(onOpenLink).not.toHaveBeenCalled();
    expect(onOpenBugReport).not.toHaveBeenCalled();
  });

  it('opens the real project editor when no project exists and creates the first project', async () => {
    loadSynonBiomedCatalogSummaryMock.mockResolvedValue({ product: 'Synon Biomed', backendStatus: 'healthy' });
    loadSynonBiomedProjectsMock.mockResolvedValue([]);
    createSynonBiomedProjectMock.mockResolvedValue({
      projectId: 'proj_first',
      name: 'STAT6',
    });

    await renderWithI18n(<QuickActionButtons inactiveBorderColor='transparent' activeShadow='none' />, 'zh-CN');
    fireEvent.click(await screen.findByRole('button', { name: PROJECTS }));

    const dialog = await screen.findByRole('dialog', { name: '新建项目' });
    fireEvent.change(within(dialog).getByPlaceholderText('输入项目名称'), { target: { value: 'STAT6' } });
    fireEvent.change(within(dialog).getByPlaceholderText('简要说明项目目标、对象或阶段'), {
      target: { value: 'STAT6 discovery program' },
    });
    fireEvent.click(within(dialog).getByRole('button', { name: '创建' }));

    await waitFor(() => {
      expect(createSynonBiomedProjectMock).toHaveBeenCalledWith({
        name: 'STAT6',
        description: 'STAT6 discovery program',
        context: null,
      });
      expect(navigateMock).toHaveBeenCalledWith('/projects/proj_first');
    });
  });
});
