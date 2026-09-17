import { fireEvent, screen } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';
import SynonBiomedExecutionLogContent from '@/renderer/components/synonBiomed/runtime/SynonBiomedExecutionLogContent';
import type { SynonBiomedExecutionRecord } from '@/renderer/components/synonBiomed/runtime/runtimeOperationsModel';

const record = (cellIndex: number): SynonBiomedExecutionRecord => ({
  kind: 'cell',
  id: `cell-${cellIndex}`,
  cellIndex,
  language: 'python',
  source: `print(${cellIndex})`,
  stdout: '',
  stderr: '',
  exitStatus: 'ok',
  errorLine: null,
  kernelKind: 'analysis',
  environment: 'python',
  filesWritten: [],
  at: null,
});

describe('SynonBiomedExecutionLogContent', () => {
  it('renders one bounded 200-record server page and delegates keyset navigation', async () => {
    const onEarlier = vi.fn();
    const onLater = vi.fn();
    await renderWithI18n(
      <SynonBiomedExecutionLogContent
        records={Array.from({ length: 200 }, (_, index) => record(index + 201))}
        loading={false}
        error={null}
        pageFromLatest={0}
        total={400}
        hasEarlier
        hasLater={false}
        onEarlier={onEarlier}
        onLater={onLater}
      />,
      'en-US'
    );

    expect(screen.getAllByTestId('execution-log-record')).toHaveLength(200);
    expect(screen.getByTestId('execution-log-range')).toHaveTextContent('201–400 / 400');
    fireEvent.click(screen.getByRole('button', { name: 'View earlier execution records' }));
    expect(onEarlier).toHaveBeenCalledOnce();
    expect(screen.getByRole('button', { name: 'View newer execution records' })).toBeDisabled();
  });
});
