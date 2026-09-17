import { fireEvent, render, screen } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import type { GuidLocalFile } from '@/renderer/pages/guid/hooks/useGuidInput';
import type { ComposerContextItem } from '@/renderer/components/chat/SendBox/composerCompositionModel';

vi.mock('@/renderer/hooks/context/LayoutContext', () => ({
  useLayoutContext: () => ({ isMobile: false }),
}));

vi.mock('@/renderer/components/media/FilePreview', () => ({ default: () => null }));
vi.mock('@/renderer/components/media/UploadProgressBar', () => ({ default: () => null }));
vi.mock('@arco-design/web-react', () => ({
  Input: {
    TextArea: ({
      autoSize: _autoSize,
      onChange,
      ...props
    }: Omit<React.TextareaHTMLAttributes<HTMLTextAreaElement>, 'onChange'> & {
      autoSize?: unknown;
      onChange?: (value: string) => void;
    }) => <textarea {...props} onChange={(event) => onChange?.(event.target.value)} />,
  },
}));

import GuidInputCard from '@/renderer/pages/guid/components/GuidInputCard';

const renderComposer = (
  isFileDragging = false,
  localFiles: GuidLocalFile[] = [],
  onRemoveLocalFile = vi.fn(),
  contextItems: ComposerContextItem[] = [],
  onRemoveContextItem = vi.fn()
) => {
  const onFocus = vi.fn();
  const onInputChange = vi.fn();
  render(
    <GuidInputCard
      input=''
      onInputChange={onInputChange}
      onKeyDown={vi.fn()}
      onPaste={vi.fn()}
      onFocus={onFocus}
      onBlur={vi.fn()}
      placeholder='Send a message'
      isInputActive={false}
      isFileDragging={isFileDragging}
      activeBorderColor='var(--color-border-3)'
      inactiveBorderColor='var(--color-border-2)'
      activeShadow='active-shadow'
      inactiveShadow='inactive-shadow'
      surfaceBackgroundColor='surface-background'
      dragHandlers={{}}
      files={[]}
      localFiles={localFiles}
      contextItems={contextItems}
      onRemoveFile={vi.fn()}
      onRemoveLocalFile={onRemoveLocalFile}
      onRemoveContextItem={onRemoveContextItem}
      actionRow={<div>actions</div>}
    />
  );
  return { onFocus, onInputChange, onRemoveLocalFile, onRemoveContextItem };
};

describe('GuidInputCard shared composer surface', () => {
  it('keeps shared border, background, and shadow tokens on the outer shell only', () => {
    const { onFocus } = renderComposer();
    const composer = screen.getByTestId('guid-composer');
    const inner = composer.firstElementChild as HTMLElement;

    expect(composer.style.getPropertyValue('--guid-composer-border-color')).toBe('var(--color-border-2)');
    expect(composer.style.getPropertyValue('--guid-composer-active-border-color')).toBe('var(--color-border-3)');
    expect(composer.style.getPropertyValue('--guid-composer-active-shadow')).toBe('active-shadow');
    expect(composer.style.getPropertyValue('--guid-composer-shadow')).toBe('inactive-shadow');
    expect(composer.style.getPropertyValue('--guid-composer-surface')).toBe('surface-background');
    expect(inner.style.borderColor).toBe('');
    expect(inner.style.boxShadow).toBe('');

    fireEvent.focus(screen.getByTestId('guid-input'));
    expect(onFocus).toHaveBeenCalledTimes(1);
  });

  it('uses the same shell for the dashed drag state instead of adding another border layer', () => {
    renderComposer(true);
    const composer = screen.getByTestId('guid-composer');
    expect(composer).toHaveClass('guid-input-card-shell--dragging');
    expect(composer.style.borderWidth).toBe('');
    expect(composer.style.borderColor).toBe('');
  });

  it('shows a selected browser file beside an editable message and supports removing the draft file', () => {
    const file = new File(['name,value\nexample,1\n'], 'results.csv', { type: 'text/csv' });
    const onRemoveLocalFile = vi.fn();
    const { onInputChange: onInputChangeHandler } = renderComposer(false, [{ id: 'local-1', file }], onRemoveLocalFile);

    expect(screen.getByTestId('guid-local-file')).toHaveAttribute('data-file-name', 'results.csv');
    expect(screen.getByTestId('guid-local-file-icon')).toHaveClass('bg-fill-2', 'text-t-secondary');
    expect(screen.getByText('CSV')).toBeInTheDocument();
    const input = screen.getByTestId('guid-input') as HTMLTextAreaElement;
    fireEvent.change(input, { target: { value: '请分析这个文件' } });
    expect(onInputChangeHandler).toHaveBeenCalledWith('请分析这个文件');

    fireEvent.click(screen.getByRole('button', { name: 'Remove results.csv' }));
    expect(onRemoveLocalFile).toHaveBeenCalledTimes(1);
  });

  it('renders exact artifacts, fixed Skills, and fixed MCP servers as removable shared context chips', () => {
    const onRemoveContextItem = vi.fn();
    renderComposer(
      false,
      [],
      vi.fn(),
      [
        { kind: 'artifact', artifactId: 'artifact-1', versionId: 'version-1', label: 'report.md' },
        { kind: 'skill', name: 'autodock-vina', label: 'autodock-vina' },
        { kind: 'mcp', serverId: 'bundled:pubmed', label: 'PubMed' },
      ],
      onRemoveContextItem
    );

    expect(screen.getByTestId('composer-context-chips')).toHaveTextContent('report.mdautodock-vinaPubMed');
    fireEvent.click(screen.getByRole('button', { name: 'Remove report.md' }));
    expect(onRemoveContextItem).toHaveBeenCalledWith('artifact:artifact-1:version-1');
  });
});
