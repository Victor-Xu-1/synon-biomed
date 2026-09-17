import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import React, { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}));

import SettingsPagination from '@/renderer/pages/settings/components/SettingsPagination';

describe('SettingsPagination', () => {
  it('returns the settings content to the top when changing catalog pages', async () => {
    render(<PaginationHarness />);
    const content = screen.getByTestId('settings-page-content');
    content.scrollTop = 296;

    fireEvent.click(screen.getByRole('button', { name: 'MCP 工具分页 2' }));

    expect(screen.getByRole('button', { name: 'MCP 工具分页 2' })).toHaveAttribute('aria-current', 'page');
    await waitFor(() => expect(content.scrollTop).toBe(0));
  });

  it('returns the settings content to the top when the current page is selected again', () => {
    render(<PaginationHarness />);
    const content = screen.getByTestId('settings-page-content');
    content.scrollTop = 263;

    fireEvent.click(screen.getByRole('button', { name: 'MCP 工具分页 1' }));

    expect(content.scrollTop).toBe(0);
  });
});

function PaginationHarness() {
  const [page, setPage] = useState(1);
  return (
    <div className='settings-page-content' data-testid='settings-page-content'>
      <SettingsPagination page={page} totalPages={3} onChange={setPage} label='MCP 工具分页' />
    </div>
  );
}
