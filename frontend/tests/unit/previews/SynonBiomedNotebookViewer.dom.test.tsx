import { screen } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@/renderer/components/Markdown', () => ({
  default: ({ children }: { children: string }) => <div data-testid='notebook-markdown'>{children}</div>,
}));

vi.mock('@/renderer/components/Markdown/CodeBlock', () => ({
  default: ({ children }: { children: string }) => <pre data-testid='notebook-code'>{children}</pre>,
}));

import SynonBiomedNotebookViewer from '@/renderer/pages/conversation/Preview/components/viewers/SynonBiomedNotebookViewer';

const notebookFixture = JSON.stringify({
  nbformat: 4,
  nbformat_minor: 5,
  metadata: {
    kernelspec: { display_name: 'Python 3 (Synon)', language: 'python', name: 'python3' },
    language_info: { name: 'python' },
  },
  cells: [
    { cell_type: 'markdown', source: ['# STAT6 analysis\n', 'Notebook result'] },
    {
      cell_type: 'code',
      execution_count: 7,
      source: ['print("ready")'],
      outputs: [
        { output_type: 'stream', name: 'stdout', text: ['\u001b[32mready\u001b[0m\n'] },
        {
          output_type: 'display_data',
          data: {
            'text/html': '<b data-result="safe">validated</b><script>window.__unsafe = true</script>',
            'text/plain': '<validated>',
          },
        },
      ],
    },
  ],
});

describe('SynonBiomedNotebookViewer', () => {
  it('renders notebook cells and sanitizes rich output without executing scripts', async () => {
    const { container } = await renderWithI18n(
      <SynonBiomedNotebookViewer filename='stat6.ipynb' content={notebookFixture} />,
      'en-US'
    );

    expect(await screen.findByRole('region', { name: 'Notebook preview' })).toBeInTheDocument();
    expect(await screen.findByText('Python 3 (Synon)')).toBeInTheDocument();
    expect(screen.getByText('2 cells · nbformat 4.5')).toBeInTheDocument();
    expect(screen.getByTestId('notebook-markdown')).toHaveTextContent('# STAT6 analysis');
    expect(screen.getByTestId('notebook-code')).toHaveTextContent('print("ready")');
    expect(screen.getByText('ready')).toBeInTheDocument();
    expect(container.querySelector('[data-result="safe"]')).toHaveTextContent('validated');
    expect(container.querySelector('script')).toBeNull();
  });
});
