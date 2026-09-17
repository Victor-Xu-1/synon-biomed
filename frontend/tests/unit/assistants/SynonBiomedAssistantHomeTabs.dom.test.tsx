/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ConfigProvider } from '@arco-design/web-react';
import { fireEvent, render, screen } from '@testing-library/react';
import type { i18n } from 'i18next';
import React from 'react';
import { I18nextProvider } from 'react-i18next';
import { beforeAll, describe, expect, it, vi } from 'vitest';
import AssistantHomeTabs from '@/renderer/pages/settings/SynonBiomedExpertsSettings/home/AssistantHomeTabs';
import type { AssistantListItem } from '@/renderer/pages/settings/SynonBiomedExpertsSettings/types';
import { createTestI18n } from '../i18nTestUtils';

vi.mock('@/renderer/hooks/context/LayoutContext', () => ({
  useLayoutContext: () => ({ isMobile: false }),
}));

vi.mock('@/renderer/utils/synonBiomed/runtime/runtimeLogo', () => ({
  resolveAgentLogo: () => null,
  useAgentLogos: () => ({}),
}));

let testI18n: i18n;

const assistant = (overrides: Partial<AssistantListItem>): AssistantListItem =>
  ({
    id: 'biomed-planner',
    source: 'builtin',
    name: 'Biomed Planner',
    name_i18n: { 'en-US': 'Biomed Planner' },
    description: 'Plans biomedical workflows',
    description_i18n: { 'en-US': 'Plans biomedical workflows' },
    enabled: true,
    sort_order: 1000,
    agent_id: 'biomed-planner',
    agent: { type: 'synonbiomed', source: 'builtin', acp_backend: 'synonbiomed' },
    enabled_skills: [],
    custom_skill_names: [],
    disabled_builtin_skills: [],
    context_i18n: {},
    prompts: [],
    prompts_i18n: {},
    models: [],
    agent_status: 'online',
    deletable: false,
    ...overrides,
  }) as AssistantListItem;

const renderHome = (assistants: AssistantListItem[], onCreate = vi.fn()) =>
  render(
    <I18nextProvider i18n={testI18n}>
      <ConfigProvider>
        <AssistantHomeTabs
          assistants={assistants}
          localeKey='en-US'
          onOpenSettings={vi.fn()}
          onToggleEnabled={vi.fn()}
          onReorder={vi.fn()}
          onStartChat={vi.fn()}
          onCreate={onCreate}
        />
      </ConfigProvider>
    </I18nextProvider>
  );

describe('Synon Biomed assistant home', () => {
  beforeAll(async () => {
    testI18n = await createTestI18n('en-US');
  });

  it('shows only Synon Biomed assistants and exposes only the Synon expert creation entrypoint', () => {
    renderHome([
      assistant({ id: 'biomed-planner', name: 'Biomed Planner' }),
      assistant({
        id: 'other-agent',
        name: 'Other Agent',
        agent: { type: 'openclaw', source: 'builtin', acp_backend: 'openclaw' },
      }),
    ]);

    expect(screen.getByTestId('assistant-home-shell')).toBeInTheDocument();
    expect(screen.getByTestId('assistant-card-biomed-planner')).toBeInTheDocument();
    expect(screen.queryByTestId('assistant-card-other-agent')).not.toBeInTheDocument();
    expect(screen.getByTestId('btn-create-synon-biomed-expert')).toBeInTheDocument();
    expect(screen.queryByTestId('btn-create-assistant')).not.toBeInTheDocument();
    expect(screen.queryByTestId('assistant-tab-official')).not.toBeInTheDocument();
    expect(screen.queryByTestId('my-assistants-pane')).not.toBeInTheDocument();
    expect(screen.queryByTestId('official-assistants-pane')).not.toBeInTheDocument();
  });

  it('searches expert names and descriptions and opens the Synon create flow', () => {
    const onCreate = vi.fn();
    renderHome(
      [
        assistant({ id: 'oncology', name: 'Oncology', description: 'Tumor biology' }),
        assistant({
          id: 'genomics',
          name: 'Genomics',
          name_i18n: { 'en-US': 'Genomics' },
          description: 'Sequence analysis',
          description_i18n: { 'en-US': 'Sequence analysis' },
        }),
      ],
      onCreate
    );

    fireEvent.change(screen.getByRole('textbox', { name: 'Search assistants by name or description' }), {
      target: { value: 'sequence' },
    });
    expect(screen.queryByTestId('assistant-card-oncology')).not.toBeInTheDocument();
    expect(screen.getByTestId('assistant-card-genomics')).toBeInTheDocument();

    fireEvent.click(screen.getByTestId('btn-create-synon-biomed-expert'));
    expect(onCreate).toHaveBeenCalledTimes(1);
  });

  it('keeps bundled experts immutable while user experts can be toggled', () => {
    renderHome([
      assistant({ id: 'builtin', source: 'builtin' }),
      assistant({
        id: 'user-profile',
        source: 'user',
        agent: { type: 'synonbiomed', source: 'user', acp_backend: 'synonbiomed' },
        deletable: true,
      }),
    ]);

    expect(screen.getByTestId('switch-enabled-builtin')).toBeDisabled();
    expect(screen.getByTestId('switch-enabled-user-profile')).not.toBeDisabled();
  });

  it('labels the runtime chip as Synon Biomed instead of a generic agent runtime', () => {
    renderHome([assistant({ id: 'biomed-planner' })]);

    expect(screen.getByTestId('assistant-runtime-biomed-planner')).toHaveTextContent('Synon Biomed');
    expect(screen.getByTestId('assistant-runtime-biomed-planner')).not.toHaveTextContent('runtime:');
  });

  it('does not expose delete or duplicate actions from the row menu', async () => {
    renderHome([assistant({ id: 'biomed-planner' })]);

    fireEvent.click(screen.getByTestId('btn-assistant-more-biomed-planner'));

    expect(await screen.findByTestId('menu-edit-biomed-planner')).toBeInTheDocument();
    expect(screen.queryByTestId('menu-delete-biomed-planner')).not.toBeInTheDocument();
    expect(screen.queryByTestId('menu-duplicate-biomed-planner')).not.toBeInTheDocument();
  });
});
