import LightweightOfficeViewer from '@/renderer/pages/conversation/Preview/components/viewers/LightweightOfficeViewer';
import { cleanup, render, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const loadPreview = vi.hoisted(() => vi.fn());

vi.mock('@/renderer/services/lightweightOfficePreview', async (importOriginal) => {
  const original = await importOriginal<typeof import('@/renderer/services/lightweightOfficePreview')>();
  return { ...original, loadLightweightOfficePreview: loadPreview };
});

vi.mock('@/renderer/pages/conversation/Preview/components/viewers/MarkdownViewer', () => ({
  default: ({ content }: { content: string }) => <article data-testid='markdown-preview'>{content}</article>,
}));

describe('LightweightOfficeViewer', () => {
  beforeEach(() => loadPreview.mockReset());
  afterEach(cleanup);

  it('renders Word content from the project artifact conversion', async () => {
    loadPreview.mockResolvedValue('# Project report');
    render(<LightweightOfficeViewer docType='word' artifactId='artifact-1' versionId='version-1' />);

    expect(await screen.findByTestId('markdown-preview')).toHaveTextContent('# Project report');
    expect(loadPreview).toHaveBeenCalledWith(
      'word',
      expect.objectContaining({ artifactId: 'artifact-1', versionId: 'version-1' }),
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    );
  });

  it('renders workbook sheets and cells in an internal scrollable table', async () => {
    loadPreview.mockResolvedValue({
      sheets: [
        {
          name: 'Results',
          data: [
            ['Compound', 'IC50'],
            ['A-01', 12.5],
          ],
        },
      ],
    });
    render(<LightweightOfficeViewer docType='excel' artifactId='artifact-2' />);

    expect(await screen.findByTestId('lightweight-office-excel')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Results' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByText('A-01')).toBeInTheDocument();
    expect(screen.getByText('12.5')).toBeInTheDocument();
  });

  it('renders presentation pages as white slide canvases without an external Office runtime', async () => {
    loadPreview.mockResolvedValue({
      slides: [{ slideNumber: 1, content: { text: 'Discovery plan', paragraphs: ['Discovery plan', 'Evidence'] } }],
    });
    await renderWithI18n(<LightweightOfficeViewer docType='ppt' artifactId='artifact-3' />, 'zh-CN');

    const slide = await screen.findByRole('region', { name: '幻灯片 1' });
    expect(slide).toHaveTextContent('Discovery plan');
    expect(slide.querySelector('.bg-white')).toBeTruthy();
  });
});
