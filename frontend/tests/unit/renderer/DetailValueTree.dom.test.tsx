import React from 'react';
import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import DetailValueTree from '@/renderer/pages/conversation/Messages/toolDetails/DetailValueTree';
import type { ToolDetailValueNode } from '@/renderer/pages/conversation/Messages/toolDetails/detailTypes';

describe('DetailValueTree', () => {
  it('keeps large arrays DOM-bounded while allowing every item to be revealed', () => {
    const records: ToolDetailValueNode = {
      kind: 'array',
      path: '$/records',
      label: 'Records',
      total: 105,
      children: Array.from({ length: 105 }, (_, index) => ({
        kind: 'scalar' as const,
        path: `$/records/${index}`,
        label: `Item ${index + 1}`,
        value: `REC-${String(index + 1).padStart(3, '0')}`,
      })),
    };

    render(<DetailValueTree node={records} chinese={false} initiallyExpanded />);

    expect(screen.getByText('REC-100')).toBeInTheDocument();
    expect(screen.queryByText('REC-101')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Show 5 more' }));
    expect(screen.getByText('REC-105')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Show .* more/ })).not.toBeInTheDocument();
  });

  it('mounts nested values only after their disclosure opens', () => {
    const root: ToolDetailValueNode = {
      kind: 'object',
      path: '$',
      label: 'Details',
      total: 1,
      children: [
        {
          kind: 'object',
          path: '$/measurement',
          label: 'Measurement',
          total: 1,
          children: [{ kind: 'scalar', path: '$/measurement/value', label: 'Value', value: '42 nM' }],
        },
      ],
    };

    render(<DetailValueTree node={root} chinese={false} initiallyExpanded />);

    expect(screen.queryByText('42 nM')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Measurement, 1 field' }));
    expect(screen.getByText('42 nM')).toBeInTheDocument();
  });

  it('keeps a large scalar DOM-bounded while preserving a path to reveal all text', () => {
    const value = 'x'.repeat(100 * 1024);
    const scalar: ToolDetailValueNode = { kind: 'scalar', path: '$/sequence', label: 'Sequence', value };
    const { container } = render(<DetailValueTree node={scalar} chinese={false} />);

    expect(container.querySelector('.tool-detail-tree__value')?.textContent).toHaveLength(96 * 1024);
    fireEvent.click(screen.getByRole('button', { name: 'Show more' }));
    expect(container.querySelector('.tool-detail-tree__value')?.textContent).toBe(value);
  });
});
