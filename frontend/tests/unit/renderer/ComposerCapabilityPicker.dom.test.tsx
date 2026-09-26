/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { act, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { MemoryRouter } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const capabilityMocks = vi.hoisted(() => ({
  loadSkills: vi.fn(),
  loadMcpServers: vi.fn(),
}));

vi.mock('@arco-design/web-react', () => ({
  Input: ({
    value,
    onChange,
    placeholder,
  }: {
    value: string;
    onChange: (value: string) => void;
    placeholder: string;
  }) => <input aria-label={placeholder} value={value} onChange={(event) => onChange(event.target.value)} />,
  Trigger: ({ children, popup }: { children: React.ReactNode; popup: React.ReactNode | (() => React.ReactNode) }) => (
    <>
      {children}
      {typeof popup === 'function' ? popup() : popup}
    </>
  ),
}));

vi.mock('@/renderer/services/synonBiomedCapabilities', () => ({
  loadSynonBiomedSkills: (...args: unknown[]) => capabilityMocks.loadSkills(...args),
  loadSynonBiomedMcpServers: (...args: unknown[]) => capabilityMocks.loadMcpServers(...args),
}));

import ComposerCapabilityPicker from '@/renderer/components/media/ComposerCapabilityPicker';

describe('ComposerCapabilityPicker', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    capabilityMocks.loadSkills.mockResolvedValue([]);
    capabilityMocks.loadMcpServers.mockResolvedValue([
      {
        id: 'pubmed',
        name: 'pubmed',
        displayName: 'PubMed',
        description: 'PubMed biomedical literature search.',
      },
    ]);
  });

  it('reloads localized catalog descriptions when the active language changes', async () => {
    let view!: Awaited<ReturnType<typeof renderWithI18n>>;
    await act(async () => {
      view = await renderWithI18n(
        <ComposerCapabilityPicker
          skillNames={[]}
          mcpStatuses={[{ id: 'pubmed', name: 'pubmed', status: 'loaded' }]}
          selectedSkillNames={[]}
          selectedMcpServerIds={[]}
          onSelectSkill={vi.fn()}
          onSelectMcpServer={vi.fn()}
          onOpenMcpSettings={vi.fn()}
        />,
        'zh-CN',
        { wrapper: ({ children }) => <MemoryRouter>{children}</MemoryRouter> }
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    await waitFor(() =>
      expect(screen.getByRole('menuitemcheckbox', { name: 'PubMed' })).toHaveAttribute(
        'title',
        expect.stringContaining('检索')
      )
    );

    await act(async () => {
      await view.i18n.changeLanguage('en-US');
      await Promise.resolve();
      await Promise.resolve();
    });

    await waitFor(() =>
      expect(screen.getByRole('menuitemcheckbox', { name: 'PubMed' })).toHaveAttribute(
        'title',
        expect.stringContaining('search')
      )
    );
    expect(capabilityMocks.loadMcpServers).toHaveBeenCalledTimes(2);
  });
});
