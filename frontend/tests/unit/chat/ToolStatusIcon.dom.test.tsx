import React from 'react';
import { render, cleanup } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import ToolStatusIcon from '@/renderer/pages/conversation/Messages/components/ToolStatusIcon';

afterEach(cleanup);
describe('tool execution status icon', () => {
  it('uses a neutral non-running marker for an unexecuted completed request', () => {
    const { container } = render(<ToolStatusIcon status='completed' disposition='not-executed' />);
    expect(container.querySelector('.tool-status-icon--completed')).toBeNull();
    expect(container.querySelector('.tool-status-icon__pulse')).toBeNull();
    expect(container.textContent).toBe('−');
  });
  it('retains success for a completed preflight explicitly labelled as such', () => {
    const { container } = render(<ToolStatusIcon status='completed' disposition='preflight-passed' />);
    expect(container.querySelector('.tool-status-icon--completed')).not.toBeNull();
  });
});
