import { fireEvent, screen } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import ComposerReferenceMenu from '@/renderer/components/chat/SendBox/ComposerReferenceMenu';
import type { ArtifactComposerReference } from '@/renderer/components/chat/SendBox/composerReferenceModel';
import { renderWithI18n } from '../../i18nTestUtils';

const item: ArtifactComposerReference = {
  kind: 'artifact',
  key: 'artifact:a:v',
  label: 'result.csv',
  detail: '当前项目',
  projectId: 'project-a',
  projectName: '当前项目',
  isCurrentProject: true,
  artifactId: 'artifact-a',
  versionId: 'version-a',
};

describe('ComposerReferenceMenu', () => {
  it('renders Chinese labels and selects a real reference item', async () => {
    const onSelect = vi.fn();
    await renderWithI18n(
      <ComposerReferenceMenu
        trigger='@'
        activeIndex={0}
        items={[item]}
        loading={false}
        error={false}
        onHoverItem={vi.fn()}
        onSelectItem={onSelect}
      />
    );
    expect(screen.getByRole('listbox', { name: '引用产物或文件' })).toBeInTheDocument();
    expect(screen.getByText('当前项目产物')).toBeInTheDocument();
    fireEvent.mouseDown(screen.getByRole('option'));
    expect(onSelect).toHaveBeenCalledWith(item);
  });

  it('shows loading, failure and empty states without overflowing text containers', async () => {
    const { rerender } = await renderWithI18n(
      <ComposerReferenceMenu
        trigger='#'
        activeIndex={0}
        items={[]}
        loading
        error={false}
        onHoverItem={vi.fn()}
        onSelectItem={vi.fn()}
      />
    );
    expect(screen.getByRole('status')).toHaveTextContent('正在加载会话');
    rerender(
      <ComposerReferenceMenu
        trigger='#'
        activeIndex={0}
        items={[]}
        loading={false}
        error
        onHoverItem={vi.fn()}
        onSelectItem={vi.fn()}
      />
    );
    expect(screen.getByRole('alert')).toHaveTextContent('会话加载失败');
    rerender(
      <ComposerReferenceMenu
        trigger='#'
        activeIndex={0}
        items={[]}
        loading={false}
        error={false}
        onHoverItem={vi.fn()}
        onSelectItem={vi.fn()}
      />
    );
    expect(screen.getByText('没有可引用的会话')).toBeInTheDocument();
  });

  it('renders the reference menu in English', async () => {
    await renderWithI18n(
      <ComposerReferenceMenu
        trigger='@'
        activeIndex={0}
        items={[item]}
        loading={false}
        error={false}
        onHoverItem={vi.fn()}
        onSelectItem={vi.fn()}
      />,
      'en-US'
    );

    expect(screen.getByRole('listbox', { name: 'Reference artifacts or files' })).toBeInTheDocument();
    expect(screen.getByText('Current project artifact')).toBeInTheDocument();
  });
});
