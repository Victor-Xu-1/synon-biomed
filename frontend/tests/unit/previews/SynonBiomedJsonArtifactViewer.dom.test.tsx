import SynonBiomedJsonViewer from '@/renderer/pages/conversation/Preview/components/viewers/SynonBiomedJsonViewer';
import { fireEvent, screen } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@/renderer/pages/conversation/Preview/components/editors/CodeEditor', () => ({
  default: ({ value }: { value: string }) => <pre data-testid='json-source'>{value}</pre>,
}));

describe('SynonBiomedJsonViewer', () => {
  it('uses the structured tree by default and expands nested values', async () => {
    await renderWithI18n(
      <SynonBiomedJsonViewer
        filename='plan.json'
        content={'{"version":3,"phases":[{"title":"Plan"}]}'}
        onCodeSelection={() => undefined}
      />,
      'en-US'
    );

    expect(screen.getByRole('tab', { name: 'Tree' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByText('version')).toBeInTheDocument();
    fireEvent.click(screen.getByText('phases'));
    fireEvent.click(screen.getByText('[0]'));
    expect(screen.getByText('Plan')).toBeInTheDocument();
  });

  it('searches nested values and switches to exact source', async () => {
    await renderWithI18n(
      <SynonBiomedJsonViewer
        filename='plan.json'
        content={'{"phases":[{"title":"Download matrix"}],"owner":"OPERON"}'}
        onCodeSelection={() => undefined}
      />,
      'en-US'
    );

    fireEvent.change(screen.getByRole('textbox', { name: 'Search JSON' }), { target: { value: 'download' } });
    expect(screen.getByText('Download matrix')).toBeInTheDocument();
    expect(screen.queryByText('OPERON')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('tab', { name: 'Source' }));
    expect(screen.getByTestId('json-source')).toHaveTextContent('"Download matrix"');
  });

  it('falls back to source with a stable diagnostic for invalid JSON', async () => {
    await renderWithI18n(
      <SynonBiomedJsonViewer filename='broken.json' content='{broken' onCodeSelection={() => undefined} />,
      'en-US'
    );

    expect(screen.getByRole('tab', { name: 'Tree' })).toBeDisabled();
    expect(screen.getByTestId('json-source')).toHaveTextContent('{broken');
    expect(screen.getByText('Invalid JSON. Source view is shown instead.')).toBeInTheDocument();
  });
});
