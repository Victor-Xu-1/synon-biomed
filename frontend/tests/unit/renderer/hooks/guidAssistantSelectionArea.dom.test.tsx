/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { fireEvent, render, screen } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import type { Assistant } from '@/common/types/agent/assistantTypes';
import AssistantSelectionArea from '@/renderer/pages/guid/components/AssistantSelectionArea';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string }) => options?.defaultValue || key,
  }),
}));

vi.mock('react-router', () => ({
  useNavigate: () => vi.fn(),
}));

vi.mock('@arco-design/web-react', async () => {
  const actual = await vi.importActual<typeof import('@arco-design/web-react')>('@arco-design/web-react');
  return {
    ...actual,
    Message: {
      useMessage: () => [{ warning: vi.fn() }, <div key='message-holder' />],
    },
  };
});

describe('AssistantSelectionArea', () => {
  it('keeps the Synon Biomed expert picker visible after an assistant is selected', () => {
    render(
      <AssistantSelectionArea
        selectedAssistantId='synonbiomed:aidd-expert'
        assistants={assistants()}
        localeKey='en-US'
        onSelectAssistant={vi.fn()}
      />
    );

    expect(screen.getByTestId('preset-pill-synonbiomed:aidd-expert')).toBeInTheDocument();
    expect(screen.queryByTestId('btn-add-preset')).not.toBeInTheDocument();
    expect(screen.queryByText('Select an assistant to start a task')).not.toBeInTheDocument();
    expect(screen.queryByText('Try these example prompts:')).not.toBeInTheDocument();
    expect(screen.queryByText('Run target discovery')).not.toBeInTheDocument();
  });

  it('moves overflow Synon Biomed experts into a more dropdown', async () => {
    render(
      <AssistantSelectionArea
        selectedAssistantId='synonbiomed:aidd-expert'
        assistants={manyAssistants()}
        localeKey='en-US'
        onSelectAssistant={vi.fn()}
      />
    );

    expect(screen.getByTestId('preset-pill-synonbiomed:aidd-expert')).toBeInTheDocument();
    expect(screen.getByTestId('preset-pill-synonbiomed:oncology-expert')).toBeInTheDocument();
    expect(screen.getByTestId('preset-pill-synonbiomed:medchem-expert')).toBeInTheDocument();
    expect(screen.getByTestId('preset-pill-synonbiomed:dmpk-expert')).toBeInTheDocument();
    expect(screen.queryByTestId('preset-pill-synonbiomed:immunology-expert')).not.toBeInTheDocument();

    fireEvent.click(screen.getByTestId('assistant-more-btn'));

    expect(await screen.findByTestId('assistant-overflow-synonbiomed:immunology-expert')).toBeInTheDocument();
    expect(screen.getByTestId('assistant-overflow-synonbiomed:genomics-bioinfo-expert')).toBeInTheDocument();
    expect(screen.queryByTestId('assistant-overflow-synonbiomed:aidd-expert')).not.toBeInTheDocument();
    expect(screen.queryByTestId('assistant-overflow-synonbiomed:oncology-expert')).not.toBeInTheDocument();
  });

  it('reports the real Synon Biomed assistant id when a pill is selected', () => {
    const onSelectAssistant = vi.fn();

    render(
      <AssistantSelectionArea
        selectedAssistantId='synonbiomed:aidd-expert'
        assistants={assistants()}
        localeKey='en-US'
        onSelectAssistant={onSelectAssistant}
      />
    );

    fireEvent.click(screen.getByTestId('preset-pill-synonbiomed:oncology-expert'));

    expect(onSelectAssistant).toHaveBeenCalledWith('synonbiomed:oncology-expert');
  });

  it('orders Synon Biomed expert pills by sort_order before applying overflow', () => {
    render(
      <AssistantSelectionArea
        selectedAssistantId='synonbiomed:aidd-expert'
        assistants={[
          mkSynonAssistant('synonbiomed:late-expert', 'Late Expert', 90),
          mkSynonAssistant('synonbiomed:early-expert', 'Early Expert', 5),
          ...assistants(),
          mkSynonAssistant('synonbiomed:mid-expert', 'Mid Expert', 15),
        ]}
        localeKey='en-US'
        onSelectAssistant={vi.fn()}
      />
    );

    expect(
      screen
        .getAllByRole('button')
        .slice(0, 4)
        .map((node) => node.textContent?.trim())
    ).toEqual(['Early Expert', 'AIDD Expert', 'Mid Expert', 'Oncology Expert']);
  });

  it('keeps a selected overflow Synon Biomed expert visible in the top pill row', () => {
    render(
      <AssistantSelectionArea
        selectedAssistantId='synonbiomed:genomics-bioinfo-expert'
        assistants={manyAssistants()}
        localeKey='en-US'
        onSelectAssistant={vi.fn()}
      />
    );

    expect(screen.getByTestId('preset-pill-synonbiomed:genomics-bioinfo-expert')).toBeInTheDocument();
    expect(screen.queryByTestId('preset-pill-synonbiomed:dmpk-expert')).not.toBeInTheDocument();
  });

  it('can re-render from an empty assistant catalog without breaking hook order', () => {
    const { rerender } = render(
      <AssistantSelectionArea
        selectedAssistantId={null}
        assistants={[]}
        localeKey='en-US'
        onSelectAssistant={vi.fn()}
      />
    );

    expect(() =>
      rerender(
        <AssistantSelectionArea
          selectedAssistantId='synonbiomed:aidd-expert'
          assistants={assistants()}
          localeKey='en-US'
          onSelectAssistant={vi.fn()}
        />
      )
    ).not.toThrow();

    expect(screen.getByTestId('preset-pill-synonbiomed:aidd-expert')).toBeInTheDocument();
  });
});

function assistants(): Assistant[] {
  return [
    mkSynonAssistant('synonbiomed:aidd-expert', 'AIDD Expert', 10, ['Run target discovery']),
    mkSynonAssistant('synonbiomed:oncology-expert', 'Oncology Expert', 20, ['Prioritize oncology targets']),
  ];
}

function manyAssistants(): Assistant[] {
  return [
    ...assistants(),
    mkSynonAssistant('synonbiomed:medchem-expert', 'MedChem Expert', 30),
    mkSynonAssistant('synonbiomed:dmpk-expert', 'DMPK Expert', 40),
    mkSynonAssistant('synonbiomed:immunology-expert', 'Immunology Expert', 50),
    mkSynonAssistant('synonbiomed:genomics-bioinfo-expert', 'Genomics Bioinfo Expert', 60),
  ];
}

function mkSynonAssistant(id: string, name: string, sort_order: number, prompts: string[] = []): Assistant {
  const agentId = id
    .replace(/^synonbiomed:/, '')
    .replace(/-/g, '_')
    .toUpperCase();
  return {
    id,
    source: 'builtin',
    name,
    name_i18n: {},
    description_i18n: {},
    enabled: true,
    sort_order,
    agent_id: agentId,
    agent: { type: 'synonbiomed', source: 'builtin', acp_backend: 'synonbiomed' },
    enabled_skills: [],
    custom_skill_names: [],
    disabled_builtin_skills: [],
    context_i18n: {},
    prompts,
    prompts_i18n: {},
    models: [],
    agent_status: 'online',
    deletable: false,
  };
}
