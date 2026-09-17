import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { GovernanceSettingsContent } from '@/renderer/pages/settings/GovernanceSettings';
import { renderWithI18n } from '../i18nTestUtils';

describe('GovernanceSettingsContent', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('is a memory-only workspace and does not request unrelated governance data', async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const path =
        typeof input === 'string'
          ? new URL(input, 'http://localhost').pathname
          : input instanceof URL
            ? input.pathname
            : new URL(input.url).pathname;
      if (path === '/api/memory/enabled') return Response.json({ enabled: false });
      if (path === '/api/memory/auto-enabled') return Response.json({ enabled: false });
      if (path === '/api/memory/context') {
        return Response.json({
          entities: [{ entity_key: 'profile', label: 'About you', project_id: null, rows: [] }],
          sessions: [],
          categories: [],
          memory_enabled: false,
          total_user_rows: 0,
        });
      }
      if (path === '/api/projects') {
        return Response.json({
          projects: [
            {
              project_id: 'project-1',
              name: 'Project One',
              description: 'Rules for the selected project.',
            },
          ],
        });
      }
      return Response.json({ detail: 'not found' }, { status: 404 });
    });
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(<GovernanceSettingsContent />, 'en-US');

    await waitFor(() => expect(screen.getByTestId('memory-manager')).toBeInTheDocument());
    expect(screen.getByTestId('memory-header')).toHaveTextContent('Memory');
    expect(screen.getByTestId('memory-layer-global')).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByTestId('memory-layer-project')).toHaveAttribute('aria-selected', 'false');
    expect(screen.getByTestId('memory-workspace')).toBeInTheDocument();
    expect(document.querySelectorAll('[data-memory-module]')).toHaveLength(3);
    expect(document.querySelector('.memory-manager__editor-kicker')).not.toBeInTheDocument();
    expect(document.querySelector('.memory-manager__empty-artwork')).not.toBeInTheDocument();
    expect(screen.queryByTestId('memory-scope-select')).not.toBeInTheDocument();
    expect(screen.getByTestId('memory-editor')).toBeInTheDocument();
    fireEvent.click(screen.getByTestId('memory-layer-project'));
    await waitFor(() => expect(screen.getByRole('heading', { level: 4 })).toHaveTextContent('Project One'));
    expect(document.querySelector('.memory-manager__panel-kicker')).not.toBeInTheDocument();
    expect(screen.getByTestId('memory-workspace')).toHaveClass('memory-manager__workspace--project');
    expect(screen.queryByTestId('memory-project-picker')).not.toBeInTheDocument();
    expect(screen.queryByTestId('memory-project-select')).not.toBeInTheDocument();
    expect(screen.queryByText('Permission grants')).toBeNull();
    expect(screen.queryByText('Provenance')).toBeNull();
    expect(screen.queryByText('Runtime environment')).toBeNull();
    expect(fetchMock).toHaveBeenCalledWith('/api/memory/context', expect.anything());
    expect(fetchMock).not.toHaveBeenCalledWith('/api/approvals/grants', expect.anything());
    expect(fetchMock).not.toHaveBeenCalledWith('/api/system/provenance-census', expect.anything());
    expect(fetchMock).not.toHaveBeenCalledWith('/api/health', expect.anything());
  });
});
