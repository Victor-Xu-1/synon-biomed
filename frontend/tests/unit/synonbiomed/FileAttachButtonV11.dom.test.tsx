import FileAttachButton from '@/renderer/components/media/FileAttachButton';
import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import React from 'react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const processDroppedFilesMock = vi.fn();

vi.mock('@/renderer/utils/platform', () => ({ isElectronDesktop: () => false }));
vi.mock('@/renderer/services/FileService', () => ({
  FileService: { processDroppedFiles: (...args: unknown[]) => processDroppedFilesMock(...args) },
}));

afterEach(() => {
  cleanup();
  processDroppedFilesMock.mockReset();
});

function renderMenu(
  options: {
    onViewPlan?: () => void;
    onRequestReview?: () => void;
    onSaveSkill?: () => void;
  } = {},
  language: 'zh-CN' | 'en-US' = 'zh-CN'
) {
  return renderWithI18n(
    <FileAttachButton synonBiomedV11 openFileSelector={vi.fn()} onLocalFilesAdded={vi.fn()} {...options} />,
    language,
    { wrapper: MemoryRouter }
  );
}

describe('v1.1 add-to-message menu', () => {
  it('hides unavailable plan, review, and save-skill operations instead of presenting no-op actions', async () => {
    await renderMenu();
    fireEvent.click(screen.getByRole('button', { name: '添加到消息' }));
    const menu = screen.getByRole('menu', { name: '添加到消息' });
    expect(menu).toHaveStyle({ minWidth: '210px' });
    expect(screen.getByText('添加文件')).toBeInTheDocument();
    expect(screen.getByText('你的文件')).toBeInTheDocument();
    expect(screen.queryByText('查看计划')).not.toBeInTheDocument();
    expect(screen.queryByText('审阅发现问题')).not.toBeInTheDocument();
    expect(screen.queryByText('保存为 Skill')).not.toBeInTheDocument();
  });

  it('routes Your files to the project artifact selector instead of the native file picker', async () => {
    const openFileSelector = vi.fn();
    const onSelectProjectFiles = vi.fn();
    await renderWithI18n(
      <FileAttachButton
        synonBiomedV11
        openFileSelector={openFileSelector}
        onSelectProjectFiles={onSelectProjectFiles}
        onLocalFilesAdded={vi.fn()}
      />,
      'en-US',
      { wrapper: MemoryRouter }
    );

    fireEvent.click(screen.getByRole('button', { name: 'Add to message' }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'Your files' }));

    expect(onSelectProjectFiles).toHaveBeenCalledTimes(1);
    expect(openFileSelector).not.toHaveBeenCalled();
  });

  it('opens the browser file chooser and uploads local files into the current draft', async () => {
    const openFileSelector = vi.fn();
    const onLocalFilesAdded = vi.fn();
    processDroppedFilesMock.mockResolvedValueOnce([
      { name: 'results.csv', path: '/uploads/results.csv', size: 8, type: 'text/csv', lastModified: 1 },
    ]);
    await renderWithI18n(
      <FileAttachButton synonBiomedV11 openFileSelector={openFileSelector} onLocalFilesAdded={onLocalFilesAdded} />,
      'zh-CN',
      { wrapper: MemoryRouter }
    );

    fireEvent.click(screen.getByRole('button', { name: '添加到消息' }));
    fireEvent.click(screen.getByRole('menuitem', { name: '添加文件' }));

    const file = new File(['a,b\n1,2\n'], 'results.csv', { type: 'text/csv' });
    fireEvent.change(screen.getByTestId('conversation-file-upload-input'), { target: { files: [file] } });
    await waitFor(() =>
      expect(onLocalFilesAdded).toHaveBeenCalledWith([expect.objectContaining({ path: '/uploads/results.csv' })])
    );
    expect(openFileSelector).not.toHaveBeenCalled();
  });

  it('keeps browser-selected files in the new-message draft instead of calling the host upload path', async () => {
    const openFileSelector = vi.fn();
    const onLocalFilesSelected = vi.fn();
    await renderWithI18n(
      <FileAttachButton
        synonBiomedV11
        openFileSelector={openFileSelector}
        onLocalFilesSelected={onLocalFilesSelected}
      />,
      'zh-CN',
      { wrapper: MemoryRouter }
    );

    fireEvent.click(screen.getByRole('button', { name: '添加到消息' }));
    fireEvent.click(screen.getByRole('menuitem', { name: '添加文件' }));

    const file = new File(['name,value\nexample,1\n'], 'results.csv', { type: 'text/csv' });
    fireEvent.change(screen.getByTestId('conversation-file-upload-input'), {
      target: { files: [file] },
    });

    await waitFor(() => expect(onLocalFilesSelected).toHaveBeenCalledWith([file]));
    await waitFor(() => expect(screen.getByRole('button', { name: '添加到消息' })).toBeEnabled());
    expect(openFileSelector).not.toHaveBeenCalled();
  });

  it('shows each operation only when a real handler is supplied', async () => {
    const onViewPlan = vi.fn();
    const onRequestReview = vi.fn();
    const onSaveSkill = vi.fn();
    await renderWithI18n(
      <FileAttachButton
        synonBiomedV11
        openFileSelector={vi.fn()}
        onLocalFilesAdded={vi.fn()}
        onViewPlan={onViewPlan}
        planEnabled
        onRequestReview={onRequestReview}
        reviewLabelMode='manual'
        onSaveSkill={onSaveSkill}
      />,
      'zh-CN',
      { wrapper: MemoryRouter }
    );
    fireEvent.click(screen.getByRole('button', { name: '添加到消息' }));
    const planItem = screen.getByRole('menuitemcheckbox', { name: '查看计划' });
    const reviewItem = screen.getByRole('menuitem', { name: '手动审阅' });
    expect(planItem).toHaveAttribute('aria-checked', 'true');
    expect(reviewItem).not.toHaveAttribute('aria-checked');
    expect(screen.queryByText('检查当前会话')).not.toBeInTheDocument();
    const reviewTooltipTrigger = reviewItem.parentElement;
    expect(reviewTooltipTrigger).not.toBeNull();
    fireEvent.mouseEnter(reviewTooltipTrigger!);
    await waitFor(() => expect(screen.getByText('检查当前会话')).toBeInTheDocument());
    fireEvent.mouseLeave(reviewTooltipTrigger!);
    await waitFor(() => expect(screen.queryByText('检查当前会话')).not.toBeInTheDocument());
    fireEvent.click(planItem);
    fireEvent.click(screen.getByRole('button', { name: '添加到消息' }));
    fireEvent.click(screen.getByRole('menuitem', { name: '手动审阅' }));
    fireEvent.click(screen.getByRole('button', { name: '添加到消息' }));
    fireEvent.click(screen.getByRole('menuitem', { name: '保存为 Skill' }));
    expect(onViewPlan).toHaveBeenCalledTimes(1);
    expect(onRequestReview).toHaveBeenCalledTimes(1);
    expect(onSaveSkill).toHaveBeenCalledTimes(1);
  });

  it('adds loaded Skills and MCP connectors as typed turn context instead of plain text', async () => {
    const onSelectSkill = vi.fn();
    const onSelectMcpServer = vi.fn();
    await renderWithI18n(
      <FileAttachButton
        synonBiomedV11
        openFileSelector={vi.fn()}
        onLocalFilesAdded={vi.fn()}
        loadedSkills={['literature-review']}
        loadedMcpStatuses={[
          { id: 'pubmed', name: 'PubMed', status: 'loaded' },
          { id: 'broken', name: 'Broken MCP', status: 'failed', reason: 'unavailable' },
        ]}
        selectedSkillNames={[]}
        selectedMcpServerIds={[]}
        onSelectSkill={onSelectSkill}
        onSelectMcpServer={onSelectMcpServer}
      />,
      'en-US',
      { wrapper: MemoryRouter }
    );

    fireEvent.click(screen.getByRole('button', { name: 'Add to message' }));
    fireEvent.mouseEnter(screen.getByText('Loaded Skills · 1'));
    await waitFor(() => expect(screen.getByRole('menuitemcheckbox', { name: 'literature-review' })).toBeVisible());
    fireEvent.click(screen.getByRole('menuitemcheckbox', { name: 'literature-review' }));
    expect(onSelectSkill).toHaveBeenCalledWith('literature-review');

    fireEvent.click(screen.getByRole('button', { name: 'Add to message' }));
    fireEvent.mouseEnter(screen.getByText('Loaded MCP · 2'));
    await waitFor(() => expect(screen.getByRole('menuitemcheckbox', { name: 'PubMed' })).toBeVisible());
    fireEvent.click(screen.getByRole('menuitemcheckbox', { name: 'PubMed' }));
    expect(onSelectMcpServer).toHaveBeenCalledWith(expect.objectContaining({ id: 'pubmed' }));
    expect(screen.getByRole('menuitem', { name: 'Broken MCP' })).toBeDisabled();
  });

  it('closes with Escape and restores focus to the v1.1 trigger', async () => {
    const user = userEvent.setup();
    await renderMenu({ onViewPlan: vi.fn() });
    const trigger = screen.getByRole('button', { name: '添加到消息' });
    fireEvent.click(trigger);
    screen.getByRole('menuitem', { name: '查看计划' }).focus();
    await user.keyboard('{Escape}');

    await waitFor(() => expect(screen.queryByRole('menu', { name: '添加到消息' })).not.toBeInTheDocument());
    expect(trigger).toHaveFocus();
  });

  it('renders the complete menu in English', async () => {
    await renderMenu({ onViewPlan: vi.fn(), onRequestReview: vi.fn(), onSaveSkill: vi.fn() }, 'en-US');
    fireEvent.click(screen.getByRole('button', { name: 'Add to message' }));

    expect(screen.getByRole('menu', { name: 'Add to message' })).toBeInTheDocument();
    expect(screen.getByText('Your files')).toBeInTheDocument();
    expect(screen.getByText('Review findings')).toBeInTheDocument();
    expect(screen.queryByText('Manual review')).not.toBeInTheDocument();
  });

  it('does not append a manual-review status label to the review action', async () => {
    await renderMenu({ onRequestReview: vi.fn() });
    fireEvent.click(screen.getByRole('button', { name: '添加到消息' }));

    expect(screen.getByRole('menuitem', { name: '审阅发现问题' })).toBeInTheDocument();
    expect(screen.queryByText('手动审阅')).not.toBeInTheDocument();
  });

  it('shows authoritative in-flight states and prevents duplicate review or skill actions', async () => {
    const onRequestReview = vi.fn();
    const onSaveSkill = vi.fn();
    await renderWithI18n(
      <FileAttachButton
        synonBiomedV11
        openFileSelector={vi.fn()}
        onLocalFilesAdded={vi.fn()}
        onRequestReview={onRequestReview}
        reviewLabelMode='manual'
        reviewInFlight
        onSaveSkill={onSaveSkill}
        saveAsSkillDisabled
      />,
      'zh-CN',
      { wrapper: MemoryRouter }
    );

    fireEvent.click(screen.getByRole('button', { name: '添加到消息' }));
    const reviewItem = screen.getByRole('menuitem', { name: '审阅中...' });
    const saveSkillItem = screen.getByRole('menuitem', { name: '保存为 Skill' });
    expect(reviewItem).toBeDisabled();
    expect(saveSkillItem).toBeDisabled();
    expect(screen.queryByText('专家正在审阅')).not.toBeInTheDocument();
    expect(screen.queryByText('任务空闲后可用')).not.toBeInTheDocument();
    fireEvent.click(reviewItem);
    fireEvent.click(saveSkillItem);
    expect(onRequestReview).not.toHaveBeenCalled();
    expect(onSaveSkill).not.toHaveBeenCalled();
  });

  it('prevents manual review while the main task is still running', async () => {
    const onRequestReview = vi.fn();
    await renderWithI18n(
      <FileAttachButton
        synonBiomedV11
        openFileSelector={vi.fn()}
        onLocalFilesAdded={vi.fn()}
        onRequestReview={onRequestReview}
        reviewLabelMode='manual'
        reviewDisabled
      />,
      'zh-CN',
      { wrapper: MemoryRouter }
    );

    fireEvent.click(screen.getByRole('button', { name: '添加到消息' }));
    const review = screen.getByRole('menuitem', { name: '手动审阅' });
    expect(review).toBeDisabled();
    expect(screen.queryByText('任务完成后可用')).not.toBeInTheDocument();
    const reviewTooltipTrigger = review.parentElement;
    expect(reviewTooltipTrigger).not.toBeNull();
    fireEvent.mouseEnter(reviewTooltipTrigger!);
    await waitFor(() => expect(screen.getByText('任务完成后可用')).toBeInTheDocument());
    fireEvent.click(review);
    expect(onRequestReview).not.toHaveBeenCalled();
  });
});
