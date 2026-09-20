import React from 'react';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ipcBridge } from '@/common';
import { BackendHttpError } from '@/common/adapter/httpBridge';
import type { TMessage } from '@/common/chat/chatLib';
import type { NormalizedToolCall, ToolMessage } from '@/common/chat/normalizeToolCall';
import MessageToolGroupSummary from '@/renderer/pages/conversation/Messages/components/MessageToolGroupSummary';
import { TranscriptActivityContext } from '@/renderer/pages/conversation/Messages/components/TranscriptActivity';
import { buildToolStepResultSummary } from '@/renderer/pages/conversation/Messages/components/toolStepSummaryModel';
import { conversationDisclosureStore } from '@/renderer/services/runtime/conversationDisclosureStore';
import { resetRendererAccountScopeForTest, setRendererAccountOwner } from '@/renderer/services/rendererAccountScope';

vi.mock('@/common', () => ({
  ipcBridge: {
    database: {
      getConversationMessage: {
        invoke: vi.fn(),
      },
    },
  },
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) =>
      (
        ({
          'tools.labels.failureSummary': 'Failure summary',
        }) as Record<string, string>
      )[key] ?? key,
    i18n: { language: 'en-US' },
  }),
}));

describe('MessageToolGroupSummary', () => {
  it('shows recoverable source unavailability from the native receipt without a completed-evidence claim', () => {
    const messages = [
      {
        id: 'unavailable-source',
        conversation_id: 'retrieval-task',
        type: 'tool_call',
        content: {
          call_id: 'unavailable-source',
          name: 'web_fetch',
          status: 'completed',
          args: { url: 'https://example.org/source' },
          output: JSON.stringify({
            ok: true,
            result: { sourceUnavailable: true, statusCode: 403, bytesRead: 146, complete: true },
          }),
        },
      },
    ] as ToolMessage[];
    render(<MessageToolGroupSummary messages={messages} />);
    expect(screen.getByTestId('tool-chip')).toHaveTextContent('Source unavailable');
    expect(screen.getByTestId('tool-chip')).not.toHaveTextContent('Completed');
    fireEvent.click(screen.getByTestId('tool-chip'));
    expect(
      screen.getByText('This response is not usable source evidence. The task can retry or use another source.')
    ).toBeVisible();
  });
  it('retains file subjects in collapsed groups and never opens evidence on progress', () => {
    const messages = ['data.csv', 'study.md'].map((path, index) => ({
      id: `subject-${index}`,
      conversation_id: 'subject-task',
      type: 'tool_call',
      content: {
        call_id: `subject-${index}`,
        name: 'read_file',
        status: 'completed',
        args: { file_path: path },
        output: 'one line',
      },
    })) as ToolMessage[];
    render(<MessageToolGroupSummary messages={messages} />);
    expect(screen.getAllByTestId('tool-chip')[0]).toHaveTextContent('data.csv');
    expect(screen.getAllByTestId('tool-chip')[1]).toHaveTextContent('study.md');
    expect(document.querySelector('.tool-public-detail')).toBeNull();
    fireEvent.click(screen.getByTestId('tool-group-header'));
    expect(screen.getByTestId('tool-group-header')).toHaveTextContent('data.csv · study.md');
    expect(screen.queryAllByTestId('tool-chip')).toHaveLength(0);
  });
  it('keeps the continuation spinner inside only the last operation and moves it to the next row', () => {
    const messages = (count: number) =>
      Array.from({ length: count }, (_, index) => ({
        id: `inline-${index}`,
        conversation_id: 'inline-task',
        type: 'tool_call',
        content: {
          call_id: `inline-${index}`,
          name: 'read_file',
          status: 'completed',
          args: { path: `data-${index}.csv` },
          output: 'done',
        },
      })) as ToolMessage[];
    const view = (count: number) => (
      <TranscriptActivityContext.Provider value={{ labelKey: 'messages.processing', spinning: true }}>
        <MessageToolGroupSummary messages={messages(count)} />
      </TranscriptActivityContext.Provider>
    );
    const { rerender } = render(view(2));
    let rows = screen.getAllByTestId('tool-chip');
    expect(rows[0].querySelector('[data-testid="transcript-activity-spinner"]')).toBeNull();
    expect(rows[1].querySelector('[data-testid="transcript-activity-spinner"]')).not.toBeNull();
    expect(rows[1].closest('.tool-step')).toHaveClass('tool-step--completed');
    rerender(view(3));
    rows = screen.getAllByTestId('tool-chip');
    expect(rows[1].querySelector('[data-testid="transcript-activity-spinner"]')).toBeNull();
    expect(rows[2].querySelector('[data-testid="transcript-activity-spinner"]')).not.toBeNull();
    expect(screen.getAllByTestId('transcript-activity-spinner')).toHaveLength(1);
    fireEvent.click(screen.getByTestId('tool-group-header'));
    expect(
      screen.getByTestId('tool-group-header').querySelector('[data-testid="transcript-activity-spinner"]')
    ).not.toBeNull();
    expect(screen.getAllByTestId('transcript-activity-spinner')).toHaveLength(1);
  });
  beforeEach(() => {
    conversationDisclosureStore.resetForTests();
    resetRendererAccountScopeForTest();
  });
  it('updates environment progress in place without replacing the running row', () => {
    const message = (completedItems: number, phasePercent: number, elapsedMs: number) => [
      {
        id: 'message-environment',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        content: {
          call_id: 'tool-environment',
          name: 'manage_environments',
          args: {
            mode: 'create',
            human_description: 'Preparing the molecular design environment',
          },
          input: {
            mode: 'create',
            human_description: 'Preparing the molecular design environment',
          },
          status: 'running',
          progress: {
            phase: 'downloading_packages',
            phasePercent,
            completedItems,
            totalItems: 8,
            elapsedMs,
            indeterminate: false,
          },
        },
      } as unknown as ToolMessage,
    ];

    const { rerender } = render(<MessageToolGroupSummary messages={message(1, 20, 30_000)} />);
    const originalRow = screen.getByTestId('tool-chip');
    expect(originalRow).toHaveTextContent('Preparing the molecular design environment');
    expect(originalRow).toHaveTextContent('Downloading packages · phase 20% · Step elapsed 0:30');
    expect(originalRow).toHaveTextContent('1 / 8 steps');
    expect(originalRow).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByTestId('tool-public-detail')).not.toBeInTheDocument();

    rerender(<MessageToolGroupSummary messages={message(2, 65, 60_000)} />);

    const updatedRow = screen.getByTestId('tool-chip');
    expect(updatedRow).toBe(originalRow);
    expect(updatedRow).toHaveAttribute('aria-expanded', 'false');
    expect(updatedRow).toHaveTextContent('Downloading packages · phase 65% · Step elapsed 1:00');
    expect(updatedRow).toHaveTextContent('2 / 8 steps');
    fireEvent.click(updatedRow);
    expect(screen.getByTestId('show-output-toggle')).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(screen.getByTestId('show-output-toggle'));
    expect(screen.getByTestId('tool-public-detail')).toHaveTextContent('Milestones completed25%');
  });

  it('shows completed history rows by default while keeping their detail cards collapsed', () => {
    const messages = [
      {
        id: 'message-search',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        content: {
          call_id: 'tool-search',
          name: 'web_search',
          description: 'Find a CRBN co-crystal structure',
          args: { query: 'CRBN lenalidomide PDB' },
          status: 'completed',
          output: '9FJX',
        },
      },
      {
        id: 'message-read',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        content: {
          call_id: 'tool-read',
          name: 'read_file',
          description: 'Read the selected structure',
          args: { path: '9FJX.pdb' },
          status: 'completed',
          output: 'ATOM',
        },
      },
    ] as unknown as ToolMessage[];

    render(<MessageToolGroupSummary messages={messages} />);

    expect(screen.queryByText(/public-source search.*is complete/i)).not.toBeInTheDocument();
    const summary = screen.getByRole('button', {
      name: /Ran a search, Inspected a source.*2 steps/,
    });
    const group = summary.closest('.tool-group-summary');
    const details = group?.querySelector('.tool-group-summary__details');
    expect(summary.querySelector('.tool-group-summary__status')).not.toBeInTheDocument();
    expect(summary).toHaveAttribute('aria-expanded', 'true');
    expect(group).toHaveClass('tool-group-summary--expanded');
    expect(details).toHaveAttribute('aria-hidden', 'false');
    expect(details).toHaveAttribute('data-expanded', 'true');
    const searchRow = screen.getByRole('button', { name: /Search sources/ });
    const readRow = screen.getByRole('button', { name: /Inspect information/ });
    expect(searchRow).toHaveAttribute('aria-expanded', 'false');
    expect(readRow).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByTestId('tool-public-detail')).not.toBeInTheDocument();
    expect(group).toContainElement(searchRow.closest('.tool-step'));
    expect(searchRow).toBeInTheDocument();
    expect(readRow).toBeInTheDocument();
    expect(searchRow).not.toHaveTextContent('web_search');
    expect(readRow).not.toHaveTextContent('read_file');
    fireEvent.click(searchRow);
    const searchDetail = screen.getByRole('region', {
      name: 'messages.researchSources',
    });
    expect(within(searchDetail).getByText('RCSB PDB 9FJX')).toBeInTheDocument();
    expect(within(searchDetail).getByRole('link', { name: 'RCSB PDB 9FJX' })).toHaveAttribute(
      'href',
      'https://www.rcsb.org/structure/9FJX'
    );
    expect(screen.getByTestId('show-output-toggle')).toBeInTheDocument();
    fireEvent.click(readRow);
    expect(screen.queryByTestId('tool-technical-detail')).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent('web_search');
    expect(document.body).not.toHaveTextContent('read_file');
    expect(searchRow).toHaveTextContent('CRBN lenalidomide PDB');

    fireEvent.click(summary);
    expect(summary).toHaveAttribute('aria-expanded', 'false');
    expect(group).not.toHaveClass('tool-group-summary--expanded');
    expect(screen.queryByRole('button', { name: /Search sources/ })).not.toBeInTheDocument();
  });

  it('keeps a manually expanded group open when a running tool finishes', () => {
    const runningMessages = [
      {
        id: 'message-search',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        content: {
          call_id: 'tool-search',
          name: 'web_search',
          description: 'Find a CRBN co-crystal structure',
          args: { query: 'CRBN lenalidomide PDB' },
          status: 'completed',
          output: '9FJX',
        },
      },
      {
        id: 'message-read',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        content: {
          call_id: 'tool-read',
          name: 'read_file',
          description: 'Read the selected structure',
          args: { path: '9FJX.pdb' },
          status: 'running',
        },
      },
    ] as unknown as ToolMessage[];
    const completedMessages = runningMessages.map((message) => ({
      ...message,
      content: { ...message.content, status: 'completed' },
    })) as unknown as ToolMessage[];

    const { rerender } = render(<MessageToolGroupSummary messages={runningMessages} />);

    const summary = screen.getByRole('button', { name: /2 steps/ });
    const details = summary.closest('.tool-group-summary')?.querySelector('.tool-group-summary__details');
    expect(summary).toHaveAttribute('aria-expanded', 'true');
    fireEvent.click(summary);
    expect(summary).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(summary);
    expect(summary).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByRole('button', { name: /Search sources/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Inspect information/ })).not.toBeDisabled();

    rerender(<MessageToolGroupSummary messages={completedMessages} />);

    expect(summary).toHaveAttribute('aria-expanded', 'true');
    expect(details).toHaveAttribute('aria-hidden', 'false');
    expect(details).toHaveAttribute('data-expanded', 'true');
    expect(screen.getByRole('button', { name: /Search sources/ })).toBeInTheDocument();
  });

  it.each(['running', 'completed', 'error', 'interrupted'])(
    'keeps rows visible and their cards collapsed through %s updates',
    (status) => {
      const messages = (count: number, state: string) =>
        Array.from({ length: count }, (_, index) => ({
          id: `collapse-message-${index}`,
          conversation_id: 'collapse-conversation',
          type: 'tool_call',
          content: {
            call_id: `collapse-tool-${index}`,
            name: 'web_fetch',
            args: { url: `https://example.org/source-${index}` },
            status: state,
            output: JSON.stringify({ body: 'Retrieved evidence' }),
          },
        })) as ToolMessage[];
      const { rerender } = render(<MessageToolGroupSummary messages={messages(2, 'running')} />);
      const header = screen.getByTestId('tool-group-header');
      expect(header).toHaveAttribute('aria-expanded', 'true');
      rerender(<MessageToolGroupSummary messages={messages(3, status)} />);
      expect(header).toHaveAttribute('aria-expanded', 'true');
      expect(screen.getAllByTestId('tool-chip')).toHaveLength(3);
      for (const row of screen.getAllByTestId('tool-chip')) {
        expect(row).toHaveAttribute('aria-expanded', 'false');
      }
      fireEvent.click(header);
      rerender(<MessageToolGroupSummary messages={messages(4, 'running')} />);
      expect(header).toHaveAttribute('aria-expanded', 'false');
    }
  );

  it('uses an explicit result count from a compact structured preview instead of counting display lines', () => {
    const summary = buildToolStepResultSummary(
      {
        key: 'tool-compact-search',
        name: 'mcp__structures-interactions__pdb_search_structures',
        status: 'completed',
        output: '{\n  "total_count": 542,\n  "n_retrieved": 100,\n  "records": [\n    {"id":"first"}',
        truncated: true,
      } as NormalizedToolCall,
      'zh-CN'
    );

    expect(summary).toBe('100 条结果');
  });

  it('keeps live task output behind the running tool disclosure without leaking product internals', async () => {
    vi.useFakeTimers();
    try {
      render(
        <MessageToolGroupSummary
          messages={[
            {
              id: 'message-live-output',
              conversation_id: 'conversation-1',
              type: 'tool_call',
              content: {
                call_id: 'tool-live-output',
                name: 'python',
                args: { code: 'print(1)' },
                status: 'running',
                output: JSON.stringify({
                  stdout: 'processed 12 of 30 ligands\nHarness prompt projection detail',
                }),
              },
            } as unknown as ToolMessage,
          ]}
        />
      );
      await act(async () => vi.advanceTimersByTimeAsync(2000));
      expect(document.body).not.toHaveTextContent('partial output');
      const row = screen.getByRole('button', { name: /Analyze data/ });
      expect(row).not.toBeDisabled();
      expect(row).toHaveAttribute('aria-expanded', 'false');
      expect(screen.queryByTestId('tool-public-detail')).not.toBeInTheDocument();
      fireEvent.click(row);
      expect(row).toHaveAttribute('aria-expanded', 'true');
      expect(screen.getByTestId('tool-public-detail')).toHaveTextContent('Running');
      expect(screen.getByTestId('tool-public-input-blocks')).toHaveTextContent('print(1)');
      const outputToggle = screen.getByTestId('show-output-toggle');
      expect(outputToggle).toHaveAttribute('aria-expanded', 'false');
      expect(screen.queryByTestId('tool-public-output-blocks')).not.toBeInTheDocument();
      fireEvent.click(outputToggle);
      expect(screen.getByTestId('tool-public-output-blocks')).toHaveTextContent('processed 12 of 30 ligands');
      expect(document.body).not.toHaveTextContent('Harness prompt projection detail');
    } finally {
      vi.useRealTimers();
    }
  });

  it('opens a detailed task panel with auditable computation input and output', () => {
    render(
      <MessageToolGroupSummary
        messages={[
          {
            id: 'message-toggle',
            conversation_id: 'conversation-1',
            type: 'tool_call',
            content: {
              call_id: 'tool-toggle',
              name: 'python',
              description: 'Compute signature scores',
              args: { code: 'print(1)' },
              status: 'completed',
              output: '1',
            },
          } as unknown as ToolMessage,
        ]}
      />
    );

    expect(screen.queryByTestId('tool-group-header')).not.toBeInTheDocument();
    const row = screen.getByRole('button', { name: /Analyze data/ });
    expect(row).not.toBeDisabled();
    expect(row).toHaveTextContent('1 line of output');
    expect(row.closest('.tool-group-summary')).toHaveClass('tool-group-summary--single');
    fireEvent.click(row);
    expect(screen.getByTestId('tool-public-detail')).toHaveTextContent('1 line of output');
    expect(screen.getByTestId('tool-public-detail')).toHaveTextContent('PYTHON');
    expect(screen.getByTestId('tool-public-input-blocks')).toHaveTextContent('print(1)');
    const outputToggle = screen.getByTestId('show-output-toggle');
    fireEvent.click(outputToggle);
    expect(screen.getByTestId('tool-public-output-blocks')).toHaveTextContent('1');
  });

  it('keeps long output blocks fully reachable through bounded DOM pages', () => {
    const longOutput = `${'x'.repeat(96 * 1024 + 10)}TAIL-MARKER`;
    render(
      <MessageToolGroupSummary
        messages={[
          {
            id: 'message-long-output',
            conversation_id: 'conversation-1',
            type: 'tool_call',
            content: {
              call_id: 'tool-long-output',
              name: 'python',
              args: { code: 'print(result)' },
              status: 'completed',
              output: JSON.stringify({ stdout: longOutput }),
            },
          } as unknown as ToolMessage,
        ]}
      />
    );

    fireEvent.click(screen.getByRole('button', { name: /Analyze data/ }));
    fireEvent.click(screen.getByTestId('show-output-toggle'));
    const output = screen.getByTestId('tool-public-output-blocks');
    expect(output).not.toHaveTextContent('TAIL-MARKER');
    fireEvent.click(screen.getByRole('button', { name: 'Show more' }));
    expect(output).toHaveTextContent('TAIL-MARKER');
  });

  it('shows detailed plan descriptions directly when the plan is expanded', () => {
    render(
      <MessageToolGroupSummary
        messages={[
          {
            id: 'message-plan-detail',
            conversation_id: 'conversation-1',
            type: 'tool_call',
            content: {
              call_id: 'tool-plan-detail',
              name: 'generate_plan',
              args: {
                task_summary: 'Analyze a public single-cell cohort',
                steps: [
                  {
                    title: 'Validate sample metadata',
                    description:
                      'Confirm patient, timepoint and response labels before constructing the analysis matrix.',
                  },
                ],
              },
              status: 'completed',
              output: JSON.stringify({ phases: 1 }),
            },
          } as unknown as ToolMessage,
        ]}
      />
    );

    fireEvent.click(screen.getByRole('button', { name: /Plan the work/ }));
    const description = screen.getByText(/Confirm patient, timepoint and response labels/);
    expect(screen.getByText('Validate sample metadata')).toBeInTheDocument();
    expect(description).toBeVisible();
    expect(screen.queryByRole('button', { name: /Validate sample metadata/ })).not.toBeInTheDocument();
  });

  it('opens structured task records through the tool, output, and collection disclosure levels', () => {
    render(
      <MessageToolGroupSummary
        messages={[
          {
            id: 'message-structured-records',
            conversation_id: 'conversation-1',
            type: 'tool_call',
            content: {
              call_id: 'tool-structured-records',
              name: 'repl',
              args: { background: false },
              status: 'completed',
              output: JSON.stringify({
                kernel_id: 'kernel-private',
                stdout:
                  "{'n_retrieved': 2, 'records': [{'pdb_id': '12XE', 'score': 1.0}, {'pdb_id': '9XAM', 'score': 1.0}]}",
              }),
            },
          } as unknown as ToolMessage,
        ]}
      />
    );

    fireEvent.click(screen.getByRole('button', { name: /Analyze data.*2 results/i }));
    const detail = screen.getByTestId('tool-public-detail');
    const outputToggle = within(detail).getByRole('button', {
      name: /Show output.*2 results/i,
    });
    expect(outputToggle).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(outputToggle);

    const records = within(detail).getByRole('button', {
      name: 'Records, 2 items',
    });
    expect(records).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(records);
    expect(records).toHaveAttribute('aria-expanded', 'true');
    expect(within(detail).getByText(/PDB ID: 12XE/)).toBeInTheDocument();
    expect(within(detail).getByText(/PDB ID: 9XAM/)).toBeInTheDocument();
    expect(detail).not.toHaveTextContent('kernel-private');
  });

  it('shows long activity rows without mounting their large detail cards', () => {
    const messages = Array.from({ length: 6 }, (_, index) => ({
      id: `message-${index}`,
      conversation_id: 'conversation-1',
      type: 'tool_call',
      content: {
        call_id: `tool-${index}`,
        name: 'python',
        args: { human_description: `Analyze cohort ${index + 1}` },
        status: 'completed',
        output: 'completed',
      },
    })) as unknown as ToolMessage[];

    render(<MessageToolGroupSummary messages={messages} />);

    const summary = screen.getByTestId('tool-group-header');
    expect(summary).toHaveAttribute('aria-expanded', 'true');
    expect(summary).toHaveTextContent('6 steps');
    expect(screen.getAllByTestId('tool-chip')).toHaveLength(6);
    expect(screen.queryByTestId('tool-public-detail')).not.toBeInTheDocument();
  });

  it('distinguishes a rejected preflight from a tool execution failure', () => {
    render(
      <MessageToolGroupSummary
        messages={[
          {
            id: 'message-preflight-rejected',
            conversation_id: 'conversation-1',
            type: 'tool_call',
            content: {
              call_id: 'tool-preflight-rejected',
              name: 'repl',
              status: 'error',
              output: JSON.stringify({
                code: 'inspection_required',
                executed: false,
                failure_fingerprint: 'recovery-family-a',
                ok: false,
                preflight: true,
                status: 'inspection_required',
                tool: 'repl',
              }),
            },
          } as unknown as ToolMessage,
        ]}
      />
    );

    const row = screen.getByRole('button', { name: /Review execution plan.*Not executed/i });
    expect(row.closest('.tool-step')).toHaveClass('tool-step--not-executed');
    expect(row).not.toHaveTextContent('Failed');
    fireEvent.click(row);
    expect(screen.queryByTestId('tool-failure-safe-detail')).not.toBeInTheDocument();
  });

  it('does not classify a nested executed operation as an unexecuted wrapper', () => {
    expect(
      buildToolStepResultSummary(
        {
          key: 'tool-executed-nested',
          name: 'python',
          status: 'completed',
          output: JSON.stringify({
            executed: false,
            result: {
              executed: true,
              results: [{ value: 1 }],
            },
          }),
        } as NormalizedToolCall,
        'en-US'
      )
    ).toBe('1 result');
  });

  it('groups repeated preflight rejections by recovery family while preserving distinct causes and real failures', () => {
    const preflightMessage = (index: number, family: string) =>
      ({
        id: `message-preflight-${index}`,
        conversation_id: 'conversation-1',
        type: 'tool_call',
        content: {
          call_id: `tool-preflight-${index}`,
          name: 'repl',
          status: 'error',
          output: JSON.stringify({
            code: 'inspection_required',
            executed: false,
            failure_fingerprint: family,
            ok: false,
            preflight: true,
            status: 'inspection_required',
            tool: 'repl',
          }),
        },
      }) as unknown as ToolMessage;
    const messages = [
      preflightMessage(0, 'recovery-family-a'),
      ...Array.from({ length: 16 }, (_, index) => preflightMessage(index + 1, 'recovery-family-b')),
      {
        id: 'message-real-failure',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        content: {
          call_id: 'tool-real-failure',
          name: 'python',
          status: 'error',
          output: 'RuntimeError: actual execution failed',
        },
      } as unknown as ToolMessage,
    ];

    render(<MessageToolGroupSummary messages={messages} />);

    const summary = screen.getByTestId('tool-group-header');
    expect(summary).toHaveTextContent('Reviewed 17 execution attempts');
    expect(summary).toHaveTextContent('1 step · 17 not executed · 1 failed');
    expect(summary).not.toHaveTextContent('18 failed');
    expect(summary).toHaveAttribute('aria-expanded', 'true');

    const rows = screen.getAllByTestId('tool-chip');
    expect(rows).toHaveLength(3);
    expect(screen.getAllByRole('button', { name: /Review execution plan/ })).toHaveLength(2);
    expect(screen.getByRole('button', { name: /Review execution plan.*Not executed ×16/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Analyze data.*Failed/i })).toBeInTheDocument();
  });

  it('renders medium traces directly under the group without a competing phase layer', () => {
    const messages = ['manage_environments', 'python', 'save_artifacts', 'edit_file', 'read_file'].map(
      (name, index) => ({
        id: `message-medium-${index}`,
        conversation_id: 'conversation-1',
        type: 'tool_call',
        content: {
          call_id: `tool-medium-${index}`,
          name,
          status: 'completed',
          output: 'done',
        },
      })
    ) as unknown as ToolMessage[];

    render(<MessageToolGroupSummary messages={messages} />);

    const summary = screen.getByTestId('tool-group-header');
    expect(summary).toHaveAttribute('aria-expanded', 'true');
    expect(screen.queryByTestId('tool-activity-section-header')).not.toBeInTheDocument();
    expect(screen.getAllByTestId('tool-chip')).toHaveLength(5);
  });

  it('keeps a very long completed trace in one three-level interaction chain', () => {
    const names = [
      'generate_plan',
      'manage_environments',
      'skill',
      'python',
      'bash',
      'repl',
      'web_search',
      'read_file',
      'web_fetch',
      'python',
      'save_artifacts',
      'edit_file',
    ];
    const messages = names.map((name, index) => ({
      id: `message-dense-${index}`,
      conversation_id: 'conversation-1',
      type: 'tool_call',
      content: {
        call_id: `tool-dense-${index}`,
        name,
        status: 'completed',
        output: 'done',
      },
    })) as unknown as ToolMessage[];

    render(<MessageToolGroupSummary messages={messages} />);

    const summary = screen.getByTestId('tool-group-header');
    expect(summary).toHaveAttribute('aria-expanded', 'true');
    expect(screen.queryByTestId('tool-activity-section-header')).not.toBeInTheDocument();
    const rows = screen.getAllByTestId('tool-chip');
    expect(rows).toHaveLength(names.length);
    const analysisRow = screen.getAllByRole('button', {
      name: /Analyze data/,
    })[0];
    fireEvent.click(analysisRow);
    expect(analysisRow).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByTestId('tool-public-detail')).toBeInTheDocument();
    fireEvent.click(analysisRow);
    expect(analysisRow).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(analysisRow);
    expect(analysisRow).toHaveAttribute('aria-expanded', 'true');
  });

  it('loads full tool content when expanding a compact history item', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessage.invoke);
    invoke.mockResolvedValue({
      id: 'message-1',
      conversation_id: 'conversation-1',
      type: 'acp_tool_call',
      content: {
        update: {
          session_update: 'tool_call',
          tool_call_id: 'tool-1',
          status: 'completed',
          title: 'web_search',
          kind: 'search',
          raw_input: { pattern: 'needle', path: '.' },
          content: [
            {
              type: 'content',
              content: {
                type: 'text',
                text: 'https://example.org/full-result',
              },
            },
          ],
        },
      },
    } as unknown as TMessage);

    render(
      <MessageToolGroupSummary
        messages={[
          {
            id: 'message-1',
            conversation_id: 'conversation-1',
            type: 'acp_tool_call',
            content: {
              _compact: {
                truncated: true,
                original_size: 90000,
                preview_chars: 4096,
              },
              update: {
                session_update: 'tool_call',
                tool_call_id: 'tool-1',
                status: 'completed',
                title: 'web_search',
                kind: 'search',
                raw_input: { pattern: 'needle', path: '.' },
                content: [
                  {
                    type: 'content',
                    content: {
                      type: 'text',
                      text: 'https://example.org/preview',
                    },
                  },
                ],
              },
            },
          } as unknown as ToolMessage,
        ]}
      />
    );

    fireEvent.click(screen.getByRole('button', { name: /Search sources/ }));

    await waitFor(() => {
      expect(invoke).toHaveBeenCalledWith({
        conversation_id: 'conversation-1',
        message_id: 'message-1',
      });
    });
    expect(await screen.findByRole('link', { name: /example.org\/full-result/ })).toHaveAttribute(
      'href',
      'https://example.org/full-result'
    );
    expect(screen.getByRole('button', { name: /Search sources/ })).toHaveTextContent('needle');
    expect(document.body).not.toHaveTextContent('path');
  });

  it('keeps the published timeline summary stable while full history enriches the detail', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessage.invoke);
    invoke.mockReset();
    invoke.mockResolvedValue({
      id: 'message-stable-summary',
      conversation_id: 'conversation-1',
      type: 'tool_call',
      content: {
        call_id: 'tool-stable-summary',
        name: 'mcp__pubmed__get_article_metadata',
        args: { pmids: ['41010003'] },
        status: 'completed',
        output: JSON.stringify({
          count: 1,
          articles: [
            {
              title: 'Fluconazole pharmacokinetics',
              abstract: 'Full public evidence.',
            },
          ],
        }),
      },
    } as unknown as TMessage);

    render(
      <MessageToolGroupSummary
        messages={[
          {
            id: 'message-stable-summary',
            conversation_id: 'conversation-1',
            type: 'tool_call',
            content: {
              call_id: 'tool-stable-summary',
              name: 'mcp__pubmed__get_article_metadata',
              args: {
                pmids: ['41010003'],
                human_description: 'Review the selected pharmacokinetics article',
              },
              status: 'completed',
              output: JSON.stringify({ stdout: 'line 1\nline 2\nline 3' }),
              _compact: {
                truncated: true,
                original_size: 90000,
                preview_chars: 4096,
              },
            },
          } as unknown as ToolMessage,
        ]}
      />
    );

    const row = screen.getByRole('button', {
      name: /Review the selected pharmacokinetics article/,
    });
    expect(row).toHaveTextContent('3 lines of output');
    fireEvent.click(row);
    await waitFor(() => expect(invoke).toHaveBeenCalled());

    expect(row).toHaveTextContent('3 lines of output');
    expect(row).not.toHaveTextContent('Completed');
    fireEvent.click(screen.getByTestId('show-output-toggle'));
    fireEvent.click(screen.getByRole('button', { name: /Articles, 1 item/ }));
    expect(await screen.findByText(/Full public evidence/)).toBeInTheDocument();
  });

  it('keeps the published human description while enriching compact history detail', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessage.invoke);
    invoke.mockReset();
    invoke.mockResolvedValue({
      id: 'message-description',
      conversation_id: 'conversation-1',
      type: 'tool_call',
      content: {
        call_id: 'tool-description',
        name: 'mcp__pubmed__get_article_details',
        args: { identifier: '41010003' },
        status: 'completed',
        output: JSON.stringify({ content: 'Full article abstract.', count: 1 }),
      },
    } as unknown as TMessage);

    render(
      <MessageToolGroupSummary
        messages={[
          {
            id: 'message-description',
            conversation_id: 'conversation-1',
            type: 'tool_call',
            content: {
              call_id: 'tool-description',
              name: 'mcp__pubmed__get_article_details',
              args: {
                identifier: '41010003',
                human_description: 'Retrieving the selected pharmacokinetic review',
              },
              status: 'completed',
              output: JSON.stringify({ stdout: 'Readable wrapper evidence.' }),
              _compact: {
                truncated: true,
                original_size: 90000,
                preview_chars: 4096,
              },
            },
          } as unknown as ToolMessage,
        ]}
      />
    );

    const row = screen.getByRole('button', {
      name: /Retrieving the selected pharmacokinetic review/,
    });
    fireEvent.click(row);
    await waitFor(() => expect(invoke).toHaveBeenCalled());

    expect(row).toHaveTextContent('Retrieving the selected pharmacokinetic review');
    expect(row).not.toHaveTextContent('Retrieve a source');
    fireEvent.click(screen.getByTestId('show-output-toggle'));
    expect(await screen.findByText('Full article abstract.')).toBeInTheDocument();
    expect(await screen.findByText('Readable wrapper evidence.')).toBeInTheDocument();
  });

  it('loads supplemental wrapper evidence for a compact collapsed connector row', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessage.invoke);
    invoke.mockReset();
    invoke.mockImplementation(async ({ message_id }) => {
      if (message_id === 'message-child') {
        return {
          id: 'message-child',
          conversation_id: 'conversation-1',
          type: 'tool_call',
          content: {
            call_id: 'tool-child',
            attempt: 1,
            operation_id: 'child-operation',
            parent_operation_id: 'wrapper-operation',
            name: 'mcp__pubmed__get_article_metadata',
            args: { pmids: ['41010003'] },
            status: 'completed',
            output: JSON.stringify({
              count: 1,
              articles: [{ title: 'Fluconazole pharmacokinetics' }],
            }),
          },
        } as unknown as TMessage;
      }
      return {
        id: 'message-wrapper',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        content: {
          call_id: 'tool-wrapper',
          attempt: 1,
          operation_id: 'wrapper-operation',
          name: 'repl',
          args: {
            code: 'result = host.mcp("pubmed", "get_article_metadata", pmids=["41010003"])',
            human_description: 'Retrieving the selected pharmacokinetic review',
          },
          status: 'completed',
          output: JSON.stringify({
            stdout: 'Title: Fluconazole pharmacokinetics\nFull public abstract evidence.',
          }),
        },
      } as unknown as TMessage;
    });

    render(
      <MessageToolGroupSummary
        messages={[
          {
            id: 'message-wrapper',
            conversation_id: 'conversation-1',
            type: 'tool_call',
            content: {
              call_id: 'tool-wrapper',
              attempt: 1,
              operation_id: 'wrapper-operation',
              name: 'repl',
              args: {
                code: 'result = host.mcp("pubmed", "get_article_metadata", pmids=["41010003"])',
                human_description: 'Retrieving the selected pharmacokinetic review',
              },
              status: 'completed',
              _compact: { truncated: true, original_size: 12000 },
            },
          } as unknown as ToolMessage,
          {
            id: 'message-child',
            conversation_id: 'conversation-1',
            type: 'tool_call',
            content: {
              call_id: 'tool-child',
              attempt: 1,
              operation_id: 'child-operation',
              parent_operation_id: 'wrapper-operation',
              name: 'mcp__pubmed__get_article_metadata',
              args: { pmids: ['41010003'] },
              status: 'completed',
              output: JSON.stringify({ count: 1 }),
              _compact: { truncated: true, original_size: 90000 },
            },
          } as unknown as ToolMessage,
        ]}
      />
    );

    const row = screen.getByRole('button', {
      name: /Retrieving the selected pharmacokinetic review/,
    });
    fireEvent.click(row);
    await waitFor(() => expect(invoke).toHaveBeenCalledTimes(2));
    fireEvent.click(screen.getByTestId('show-output-toggle'));

    expect(await screen.findByText(/Full public abstract evidence/)).toBeInTheDocument();
    expect(screen.getAllByTestId('tool-chip')).toHaveLength(1);
    expect(document.body.textContent).not.toContain('host.mcp');
  });

  it('keeps the durable operation settlement authoritative after loading a stale full item', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessage.invoke);
    invoke.mockReset();
    invoke.mockResolvedValue({
      id: 'message-terminal',
      conversation_id: 'conversation-1',
      type: 'tool_call',
      content: {
        call_id: 'tool-terminal',
        name: 'python',
        status: 'running',
        output: 'partial internal output',
      },
    } as unknown as TMessage);

    render(
      <MessageToolGroupSummary
        messages={[
          {
            id: 'message-terminal',
            conversation_id: 'conversation-1',
            type: 'tool_call',
            content: {
              call_id: 'tool-terminal',
              name: 'python',
              status: 'interrupted',
              _compact: {
                truncated: true,
                original_size: 90000,
                preview_chars: 4096,
              },
            },
          } as unknown as ToolMessage,
        ]}
      />
    );

    const row = screen.getByRole('button', { name: /Analyze data.*Interrupted/ });
    fireEvent.click(row);

    await waitFor(() => expect(invoke).toHaveBeenCalled());
    expect(row).toHaveTextContent('Interrupted');
    expect(row).not.toHaveTextContent('Running');
    expect(screen.getByTestId('tool-public-detail')).toHaveTextContent('Interrupted');
    fireEvent.click(screen.getByTestId('show-output-toggle'));
    expect(document.body).toHaveTextContent('partial internal output');
  });

  it('keeps loading compact tool detail through a transient history projection gap', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessage.invoke);
    invoke.mockReset();
    invoke
      .mockRejectedValueOnce(
        new BackendHttpError({
          method: 'GET',
          path: '/api/conversations/conversation-1/messages/message-retry',
          status: 503,
          body: {
            code: 'HISTORY_NOT_READY',
            message: 'Transcript history is converging',
          },
        })
      )
      .mockResolvedValue({
        id: 'message-retry',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        content: {
          call_id: 'tool-retry',
          name: 'web_search',
          status: 'completed',
          output: 'https://example.org/full-after-retry',
        },
      } as unknown as TMessage);

    render(
      <MessageToolGroupSummary
        messages={[
          {
            id: 'message-retry',
            conversation_id: 'conversation-1',
            type: 'tool_call',
            content: {
              call_id: 'tool-retry',
              name: 'web_search',
              status: 'completed',
              output: 'https://example.org/preview',
              _compact: {
                truncated: true,
                original_size: 90000,
                preview_chars: 4096,
              },
            },
          } as unknown as ToolMessage,
        ]}
      />
    );

    fireEvent.click(screen.getByRole('button', { name: /Search sources/ }));

    expect(await screen.findByRole('link', { name: /example.org\/full-after-retry/ })).toBeInTheDocument();
    expect(invoke).toHaveBeenCalledTimes(2);
    expect(screen.queryByText('tools.labels.loadFullOutputFailed')).not.toBeInTheDocument();
  });

  it('does not display tool detail that resolves after the signed-in account changes', async () => {
    let resolveMessage!: (message: TMessage) => void;
    const invoke = vi.mocked(ipcBridge.database.getConversationMessage.invoke);
    invoke.mockReset();
    invoke.mockImplementation(
      () =>
        new Promise<TMessage>((resolve) => {
          resolveMessage = resolve;
        })
    );
    setRendererAccountOwner('owner-a');

    render(
      <MessageToolGroupSummary
        messages={[
          {
            id: 'message-account-scope',
            conversation_id: 'conversation-1',
            type: 'tool_call',
            content: {
              call_id: 'tool-account-scope',
              name: 'python',
              status: 'completed',
              output: '{"preview":"safe"}',
              _compact: { truncated: true, original_size: 90000 },
            },
          } as unknown as ToolMessage,
        ]}
      />
    );

    fireEvent.click(screen.getByRole('button', { name: /Analyze data/ }));
    await waitFor(() => expect(invoke).toHaveBeenCalledTimes(1));
    await act(async () => {
      setRendererAccountOwner('owner-b');
      resolveMessage({
        id: 'message-account-scope',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        content: {
          call_id: 'tool-account-scope',
          name: 'python',
          status: 'completed',
          output: 'old owner private evidence',
        },
      } as unknown as TMessage);
      await Promise.resolve();
    });

    expect(screen.getByRole('button', { name: /Analyze data/ })).toHaveAttribute('aria-expanded', 'false');
    expect(document.body).not.toHaveTextContent('old owner private evidence');
  });

  it.each([true, false])(
    'hydrates a durable large search result (history compact marker: %s)',
    async (historyCompact) => {
      const contentUrl =
        '/api/artifacts/large-tool-result-b99bbabc3daa06ffac144155ec2d9118/versions/ltr-acfa9b05-f3ed-45ec-b8da-9d6f8f45ee83';
      const hydratedOutput = JSON.stringify({
        ok: true,
        result: {
          query: 'sotorasib brain metastasis',
          sources: [
            { title: 'Primary study', url: 'https://example.org/primary' },
            {
              title: 'Independent study',
              url: 'https://example.org/independent',
            },
          ],
        },
      });
      const descriptor = JSON.stringify({
        artifact_id: 'large-tool-result-b99bbabc3daa06ffac144155ec2d9118',
        version_id: 'ltr-acfa9b05-f3ed-45ec-b8da-9d6f8f45ee83',
        content_url: contentUrl,
        truncated: true,
        outcome: 'succeeded',
        preview: JSON.stringify({
          view_format: 'search-results-display-lines',
          source_version_id: 'ltr-acfa9b05-f3ed-45ec-b8da-9d6f8f45ee83',
          source_count: 2,
          content: '1\tQuery: public evidence',
        }),
      });
      const fetchMock = vi.fn().mockResolvedValue(
        new Response(hydratedOutput, {
          status: 200,
          headers: {
            'content-length': String(new TextEncoder().encode(hydratedOutput).byteLength),
          },
        })
      );
      vi.stubGlobal('fetch', fetchMock);

      try {
        const invoke = vi.mocked(ipcBridge.database.getConversationMessage.invoke);
        invoke.mockReset();
        invoke.mockResolvedValue({
          id: 'message-large-search',
          conversation_id: 'conversation-1',
          type: 'tool_call',
          content: {
            call_id: 'tool-large-search',
            name: 'web_search',
            status: 'completed',
            output: descriptor,
          },
        } as unknown as TMessage);

        render(
          <MessageToolGroupSummary
            messages={[
              {
                id: 'message-large-search',
                conversation_id: 'conversation-1',
                type: 'tool_call',
                content: {
                  call_id: 'tool-large-search',
                  name: 'web_search',
                  status: 'completed',
                  output: historyCompact ? '{"preview":"partial"}' : descriptor,
                  ...(historyCompact
                    ? {
                        _compact: {
                          truncated: true,
                          original_size: 60000,
                          result_count: 2,
                        },
                      }
                    : {}),
                },
              } as unknown as ToolMessage,
            ]}
          />
        );

        expect(invoke).not.toHaveBeenCalled();
        expect(fetchMock).not.toHaveBeenCalled();
        fireEvent.click(screen.getByRole('button', { name: /Search sources.*2 results/ }));
        expect(document.querySelector('.tool-research-sources__empty')).toBeNull();

        expect(await screen.findByRole('link', { name: 'Primary study' })).toHaveAttribute(
          'href',
          'https://example.org/primary'
        );
        expect(screen.getByRole('link', { name: 'Independent study' })).toBeInTheDocument();
        expect(fetchMock).toHaveBeenNthCalledWith(
          1,
          contentUrl,
          expect.objectContaining({
            method: 'HEAD',
            credentials: 'include',
            signal: expect.anything(),
          })
        );
        expect(fetchMock).toHaveBeenNthCalledWith(
          2,
          contentUrl,
          expect.objectContaining({
            credentials: 'include',
            signal: expect.anything(),
          })
        );
        expect(
          screen.queryByText('No usable sources were found. Refine the query or use another public database.')
        ).not.toBeInTheDocument();
      } finally {
        vi.unstubAllGlobals();
      }
    }
  );

  it('switches an oversized durable result to the authenticated paged detail tree', async () => {
    const contentUrl =
      '/api/artifacts/large-tool-result-b99bbabc3daa06ffac144155ec2d9118/versions/ltr-acfa9b05-f3ed-45ec-b8da-9d6f8f45ee83';
    const fetchMock = vi.fn().mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input) === contentUrl && init?.method === 'HEAD') {
        return new Response(null, {
          status: 200,
          headers: { 'content-length': String(9 * 1024 * 1024) },
        });
      }
      throw new Error(`unexpected request ${String(input)}`);
    });
    vi.stubGlobal('fetch', fetchMock);

    try {
      const invoke = vi.mocked(ipcBridge.database.getConversationMessage.invoke);
      invoke.mockReset();
      invoke.mockResolvedValue({
        id: 'message-remote-detail',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        content: {
          call_id: 'tool-remote-detail',
          name: 'web_search',
          status: 'completed',
          revision: 7,
          output: JSON.stringify({
            artifact_id: 'large-tool-result-b99bbabc3daa06ffac144155ec2d9118',
            version_id: 'ltr-acfa9b05-f3ed-45ec-b8da-9d6f8f45ee83',
            content_url: contentUrl,
            truncated: true,
          }),
        },
      } as unknown as TMessage);

      render(
        <MessageToolGroupSummary
          messages={[
            {
              id: 'message-remote-detail',
              conversation_id: 'conversation-1',
              type: 'tool_call',
              content: {
                call_id: 'tool-remote-detail',
                name: 'web_search',
                status: 'completed',
                output: '{"preview":"partial"}',
                _compact: { truncated: true, original_size: 9 * 1024 * 1024, result_count: 105 },
              },
            } as unknown as ToolMessage,
          ]}
        />
      );

      fireEvent.click(screen.getByRole('button', { name: /Search sources.*105 results/ }));
      await waitFor(() =>
        expect(fetchMock).toHaveBeenCalledWith(
          contentUrl,
          expect.objectContaining({
            method: 'HEAD',
            credentials: 'include',
            signal: expect.anything(),
          })
        )
      );
      fireEvent.click(screen.getByTestId('show-output-toggle'));
      expect(await screen.findByTestId('tool-remote-detail')).toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Full details' })).toHaveAttribute('aria-expanded', 'false');
      expect(screen.queryByText('tools.labels.loadFullOutputFailed')).not.toBeInTheDocument();
      expect(fetchMock.mock.calls.filter((call) => String(call[0]) === contentUrl)).toHaveLength(1);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('shows a safe recovery summary plus redacted auditable diagnostics', () => {
    const rawOutput = [
      'Traceback (most recent call last):',
      '  File "/home/victor_1/.synon-biomed-v0.1.0/runner.py", line 42, in run',
      'RuntimeError: upstream request failed (api_key=sk-1234567890abcdef)',
    ].join('\n');

    render(
      <MessageToolGroupSummary
        messages={[
          {
            id: 'message-error',
            conversation_id: 'conversation-1',
            type: 'tool_call',
            content: {
              call_id: 'tool-error',
              name: 'python',
              description: 'Run provider request',
              status: 'error',
              output: rawOutput,
            },
          } as unknown as ToolMessage,
        ]}
      />
    );

    const row = screen.getByRole('button', { name: /Analyze data/ });
    expect(row).toHaveTextContent('Failed');
    expect(row).not.toHaveTextContent('RuntimeError');
    expect(document.body).not.toHaveTextContent('Traceback');
    expect(document.body).not.toHaveTextContent('sk-1234567890abcdef');
    expect(document.body).not.toHaveTextContent('/home/victor_1');

    fireEvent.click(row);

    expect(screen.getByTestId('tool-failure-safe-detail')).toHaveTextContent(
      'This step did not complete; the task can retry or use another approach.'
    );
    expect(screen.getByTestId('tool-failure-safe-detail')).not.toHaveTextContent('Traceback');
    expect(screen.getByTestId('tool-failure-safe-detail')).not.toHaveTextContent('[local path]');
    expect(document.body).not.toHaveTextContent('sk-1234567890abcdef');
    expect(document.body).not.toHaveTextContent('/home/victor_1');
  });

  it('keeps raw tool identifiers out of collapsed failure summaries', () => {
    render(
      <MessageToolGroupSummary
        messages={[
          {
            id: 'message-disallowed-tool',
            conversation_id: 'conversation-1',
            type: 'tool_call',
            content: {
              call_id: 'tool-disallowed',
              name: 'shell_exec',
              status: 'error',
              output: 'tool Glob is not allowed for agent runtime',
            },
          } as unknown as ToolMessage,
        ]}
      />
    );

    expect(screen.queryByTestId('tool-group-header')).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent('Glob');

    const row = screen.getByRole('button', { name: /Continue the task/ });
    expect(row).toHaveTextContent('Failed');
    expect(row).not.toHaveTextContent('tool operation');
    fireEvent.click(row);
    expect(screen.getByTestId('tool-failure-safe-detail')).not.toHaveTextContent('Glob');
    expect(screen.queryByTestId('tool-technical-detail')).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent('shell_exec');
  });

  it('does not infer a tool terminal result from an unrelated task terminal state', () => {
    render(
      <MessageToolGroupSummary
        messages={[
          {
            id: 'message-terminal',
            conversation_id: 'conversation-1',
            type: 'tool_call',
            content: {
              call_id: 'tool-terminal',
              name: 'repl',
              status: 'running',
              args: { human_description: 'Analyze the selected study' },
            },
          } as unknown as ToolMessage,
        ]}
      />
    );

    const row = screen.getByRole('button', {
      name: /Analyze the selected study/,
    });
    expect(row).toHaveTextContent('Running');
    expect(row).not.toHaveTextContent('Failed');
  });

  it('restores a manually collapsed group after virtual-list unmount and remount', () => {
    const messages = ['web_search', 'read_file'].map((name, index) => ({
      id: `message-${index}`,
      conversation_id: 'conversation-1',
      type: 'tool_call',
      content: {
        call_id: `tool-${index}`,
        name,
        status: 'completed',
        output: 'done',
      },
    })) as unknown as ToolMessage[];

    const first = render(<MessageToolGroupSummary messages={messages} />);
    expect(screen.getByTestId('tool-group-header')).toHaveAttribute('aria-expanded', 'true');
    fireEvent.click(screen.getByTestId('tool-group-header'));
    expect(screen.getByTestId('tool-group-header')).toHaveAttribute('aria-expanded', 'false');
    first.unmount();

    render(<MessageToolGroupSummary messages={messages} />);
    expect(screen.getByTestId('tool-group-header')).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByTestId('tool-chip')).not.toBeInTheDocument();
  });
});
