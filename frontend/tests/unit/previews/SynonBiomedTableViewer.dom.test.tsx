import { screen, waitFor } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

type MockTableVirtuosoProps = {
  data: string[][];
  fixedHeaderContent: () => React.ReactNode;
  itemContent: (rowIndex: number, row: string[]) => React.ReactNode;
};

vi.mock('react-virtuoso', () => ({
  TableVirtuoso: ({ data, fixedHeaderContent, itemContent }: MockTableVirtuosoProps) => (
    <table>
      <thead>{fixedHeaderContent()}</thead>
      <tbody>
        {data.map((row: string[], rowIndex: number) => (
          <tr key={rowIndex}>{itemContent(rowIndex, row)}</tr>
        ))}
      </tbody>
    </table>
  ),
}));

import SynonBiomedTableViewer from '@/renderer/pages/conversation/Preview/components/viewers/SynonBiomedTableViewer';

describe('SynonBiomedTableViewer', () => {
  it('renders parsed remote scientific data as a scrollable table rather than source code', async () => {
    await renderWithI18n(
      <SynonBiomedTableViewer filename='scores.csv' content={'gene,score\nSTAT6,0.91\nCRBN,0.83'} />,
      'en-US'
    );

    expect(await screen.findByRole('region', { name: 'Data table preview' })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText('2 rows · 2 columns')).toBeInTheDocument());
    expect(screen.getByRole('columnheader', { name: 'gene' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'score' })).toBeInTheDocument();
    expect(screen.getByText('STAT6')).toBeInTheDocument();
    expect(screen.getByText('0.91')).toBeInTheDocument();
  });

  it('renders UTF-8 scientific symbols and multilingual text without mojibake', async () => {
    await renderWithI18n(
      <SynonBiomedTableViewer
        filename='evidence.csv'
        content={'unit,claim\nÅngström,mechanism — confirmed\n中文,NEK7–NLRP3'}
      />,
      'en-US'
    );

    expect(await screen.findByText('Ångström')).toBeInTheDocument();
    expect(screen.getByText('mechanism — confirmed')).toBeInTheDocument();
    expect(screen.getByText('中文')).toBeInTheDocument();
    expect(screen.getByText('NEK7–NLRP3')).toBeInTheDocument();
  });

  it('labels a bounded table preview instead of implying that sampled data is complete', async () => {
    const header = Array.from({ length: 140 }, (_, index) => `sample_${index + 1}`).join(',');
    const row = Array.from({ length: 140 }, (_, index) => String(index + 1)).join(',');

    await renderWithI18n(<SynonBiomedTableViewer filename='matrix.csv' content={`${header}\n${row}`} />, 'en-US');

    expect(await screen.findByText('Previewing 1 rows · 120 columns')).toBeInTheDocument();
  });
});
