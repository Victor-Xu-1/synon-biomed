/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React from 'react';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { IMessageAcpToolCall, IMessageToolCall } from '@/common/chat/chatLib';
import MessageToolGroupSummary from '@/renderer/pages/conversation/Messages/components/MessageToolGroupSummary';
import { conversationDisclosureStore } from '@/renderer/services/runtime/conversationDisclosureStore';

const mockDownloadFileFromPath = vi.fn().mockResolvedValue(undefined);
const mockMessageSuccess = vi.fn();
const mockMessageError = vi.fn();

vi.mock('@/renderer/components/media/LocalImageView', () => ({
  __esModule: true,
  default: ({ src, alt, className }: { src: string; alt: string; className?: string }) => (
    <img src={src} alt={alt} className={className} data-testid='local-image' />
  ),
}));

vi.mock('@/renderer/components/Markdown', () => ({
  __esModule: true,
  default: ({ children }: { children: React.ReactNode }) => <div data-testid='markdown-view'>{children}</div>,
}));

vi.mock('@/renderer/utils/file/download', () => ({
  downloadFileFromPath: (...args: unknown[]) => mockDownloadFileFromPath(...args),
}));

vi.mock('@/renderer/components/base/FileChangesPanel', () => ({
  __esModule: true,
  default: () => <div data-testid='file-changes-panel' />,
}));

vi.mock('@/renderer/hooks/file/useDiffPreviewHandlers', () => ({
  useDiffPreviewHandlers: () => ({
    handleFileClick: vi.fn(),
    handleDiffClick: vi.fn(),
  }),
}));

vi.mock('@/renderer/utils/file/diffUtils', () => ({
  parseDiff: () => ({ fileName: 'file.ts' }),
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}));

vi.mock('@arco-design/web-react', async () => {
  const actual = await vi.importActual<typeof import('@arco-design/web-react')>('@arco-design/web-react');

  return {
    ...actual,
    Message: {
      useMessage: () => [{ success: mockMessageSuccess, error: mockMessageError }, null],
    },
  };
});

describe('MessageToolGroupSummary ACP image output', () => {
  it('does not present an unexecuted environment request as a successful operation', () => {
    const message: IMessageToolCall = {
      id: 'environment-wait',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'environment-wait',
        name: 'manage_environments',
        status: 'completed',
        args: { mode: 'create', name: 'analysis' },
        output: JSON.stringify({
          ok: true,
          executed: false,
          decision_required: true,
          status: 'implementation_selection_required',
        }),
      },
    };
    render(<MessageToolGroupSummary messages={[message]} />);
    const row = screen.getByRole('button', { name: /Not executed/ });
    expect(row.querySelector('.tool-status-icon--not-executed')).not.toBeNull();
    expect(row.querySelector('.tool-status-icon--completed')).toBeNull();
    expect(row.querySelector('.tool-status-icon__pulse')).toBeNull();
  });

  beforeEach(() => {
    conversationDisclosureStore.resetForTests();
    mockDownloadFileFromPath.mockReset();
    mockDownloadFileFromPath.mockResolvedValue(undefined);
    mockMessageSuccess.mockClear();
    mockMessageError.mockClear();
  });

  const expandTool = () => {
    const group = screen.queryByTestId('tool-group-header');
    if (group?.getAttribute('aria-expanded') === 'false') fireEvent.click(group);
  };
  it('renders generated image preview when an ACP image tool call is expanded', () => {
    const message: IMessageAcpToolCall = {
      id: 'ig_test_image',
      conversation_id: 'conv-1',
      type: 'acp_tool_call',
      content: {
        sessionId: 'sess-1',
        update: {
          sessionUpdate: 'tool_call_update',
          tool_call_id: 'ig_test_image',
          status: 'completed',
          title: 'Image generation',
          kind: 'execute',
          raw_output: {
            image: {
              path: '/Users/test/.codex/generated_images/session/ig_test_image.png',
            },
          },
          content: [
            {
              type: 'content',
              content: {
                type: 'text',
                text: 'Revised prompt: 一张小猫照片',
              },
            },
          ],
        },
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    expandTool();
    fireEvent.click(screen.getByTestId('tool-chip'));

    const image = screen.getByTestId('local-image');
    expect(image).toHaveAttribute('src', '/Users/test/.codex/generated_images/session/ig_test_image.png');
    expect(image).toHaveAttribute('alt', 'ig_test_image.png');
  });

  it('downloads the generated image from its local path', () => {
    const imagePath = '/Users/test/.codex/generated_images/session/ig_test_image.png';
    const message: IMessageAcpToolCall = {
      id: 'ig_test_image',
      conversation_id: 'conv-1',
      type: 'acp_tool_call',
      content: {
        sessionId: 'sess-1',
        update: {
          sessionUpdate: 'tool_call_update',
          tool_call_id: 'ig_test_image',
          status: 'completed',
          title: 'Image generation',
          kind: 'execute',
          raw_output: {
            image: {
              path: imagePath,
            },
          },
        },
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    expandTool();
    fireEvent.click(screen.getByTestId('tool-chip'));
    fireEvent.click(screen.getByLabelText('acp.image.download_aria'));

    expect(mockDownloadFileFromPath).toHaveBeenCalledWith(imagePath, 'ig_test_image.png');
  });

  it('shows an error when generated image download fails', async () => {
    const imagePath = '/Users/test/.codex/generated_images/session/ig_test_image.png';
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
    mockDownloadFileFromPath.mockRejectedValueOnce(new Error('denied'));
    const message: IMessageAcpToolCall = {
      id: 'ig_test_image',
      conversation_id: 'conv-1',
      type: 'acp_tool_call',
      content: {
        sessionId: 'sess-1',
        update: {
          sessionUpdate: 'tool_call_update',
          tool_call_id: 'ig_test_image',
          status: 'completed',
          title: 'Image generation',
          kind: 'execute',
          raw_output: {
            image: {
              path: imagePath,
            },
          },
        },
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    expandTool();
    fireEvent.click(screen.getByTestId('tool-chip'));
    fireEvent.click(screen.getByLabelText('acp.image.download_aria'));

    await waitFor(() => {
      expect(mockMessageError).toHaveBeenCalledWith('acp.image.download_error');
    });
    expect(consoleError).toHaveBeenCalledWith('[ToolOperationDetail] Failed to download image:', expect.any(Error));
    expect(mockMessageSuccess).not.toHaveBeenCalled();
    consoleError.mockRestore();
  });

  it('uses i18n keys for the image download control', () => {
    const message: IMessageAcpToolCall = {
      id: 'ig_test_image',
      conversation_id: 'conv-1',
      type: 'acp_tool_call',
      content: {
        sessionId: 'sess-1',
        update: {
          sessionUpdate: 'tool_call_update',
          tool_call_id: 'ig_test_image',
          status: 'completed',
          title: 'Image generation',
          kind: 'execute',
          raw_output: {
            image: {
              path: '/Users/test/.codex/generated_images/session/ig_test_image.png',
            },
          },
        },
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    expandTool();
    fireEvent.click(screen.getByTestId('tool-chip'));

    expect(screen.getByLabelText('acp.image.download_aria')).toBeInTheDocument();
  });

  it('keeps ACP input, text output, and file diffs in the unified tool disclosure', () => {
    const message: IMessageAcpToolCall = {
      id: 'acp-edit-1',
      conversation_id: 'conv-1',
      type: 'acp_tool_call',
      content: {
        sessionId: 'sess-1',
        update: {
          sessionUpdate: 'tool_call_update',
          tool_call_id: 'acp-edit-1',
          status: 'completed',
          title: 'Update analysis file',
          kind: 'edit',
          rawInput: { prompt: 'Preserve the validated assay result' },
          content: [
            { type: 'content', content: { type: 'text', text: 'Updated the analysis file.' } },
            { type: 'diff', path: '/workspace/file.ts', old_text: 'old', new_text: 'new' },
          ],
        },
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    fireEvent.click(screen.getByRole('button', { name: /Update analysis file/ }));

    expect(screen.getByTestId('file-changes-panel')).toBeInTheDocument();
    expect(screen.getByText('Preserve the validated assay result')).toBeInTheDocument();
    fireEvent.click(screen.getByTestId('show-output-toggle'));
    expect(screen.getByText(/Updated the analysis file/)).toBeInTheDocument();
  });

  it('does not render image controls for tool calls without image output', () => {
    const message: IMessageToolCall = {
      id: 'tool-1',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'tool-1',
        name: 'Shell Command',
        args: {},
        status: 'completed',
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    expandTool();
    fireEvent.click(screen.getByTestId('tool-chip'));

    expect(screen.queryByTestId('local-image')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('acp.image.download_aria')).not.toBeInTheDocument();
  });

  it('renders validated, deduplicated links for a real-compatible WebSearch output', () => {
    const message: IMessageToolCall = {
      id: 'web-search-1',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'web-search-1',
        name: 'web_search',
        args: { query: 'CRBN ligand structure' },
        status: 'completed',
        output: [
          '{"results":[',
          '{"url":"https://www.rcsb.org/structure/8OIZ","title":"CRBN-DDB1"},',
          '{"url":"https://www.rcsb.org/structure/8OIZ","title":"duplicate"},',
          '{"url":"https://pubmed.ncbi.nlm.nih.gov/17187687/"}',
          '] }',
        ].join('\n'),
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    expandTool();
    fireEvent.click(screen.getByRole('button', { name: /Search sources/ }));

    const sources = screen.getByRole('region', { name: 'messages.researchSources' });
    const links = sources.querySelectorAll('a');
    expect(links).toHaveLength(2);
    expect(links[0]).toHaveAttribute('href', 'https://www.rcsb.org/structure/8OIZ');
    expect(links[0]).toHaveAttribute('rel', 'noopener noreferrer');
    expect(links[1]).toHaveAttribute('href', 'https://pubmed.ncbi.nlm.nih.gov/17187687/');
    // This suite deliberately mocks translations as keys, not English copy.
    expect(screen.getByText('messages.researchQuery')).toBeInTheDocument();
    expect(within(sources).getByText('CRBN ligand structure')).toBeInTheDocument();
    expect(within(sources).getByText('CRBN-DDB1')).toBeInTheDocument();
  });

  it('keeps an empty search in the dedicated public renderer without exposing backend diagnostics', () => {
    const message: IMessageToolCall = {
      id: 'web-search-empty-1',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'web-search-empty-1',
        name: 'web_search',
        args: { query: 'linezolid exposure thrombocytopenia' },
        status: 'completed',
        output: JSON.stringify({
          ok: true,
          results: 1,
          sources: 0,
          provider: 'synon-websearch',
          diagnostics: {
            GoHTTPBackend: true,
            GoHTTPError: 'all Go HTTP search backends returned no usable links',
            HttpBackends: [{ Name: 'bing-html', RawResults: 10, ReturnedResults: 0 }],
          },
        }),
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    const row = screen.getByRole('button', { name: /Search sources.*No usable sources/ });
    fireEvent.click(row);

    const sources = screen.getByRole('region', { name: 'messages.researchSources' });
    expect(within(sources).getByText('linezolid exposure thrombocytopenia')).toBeInTheDocument();
    expect(within(sources).getByText('messages.researchEmpty')).toBeInTheDocument();
    const outputToggle = screen.getByTestId('show-output-toggle');
    expect(outputToggle).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(outputToggle);
    expect(document.body.textContent).not.toMatch(/synon-websearch|GoHTTP|bing-html|backend/u);
  });

  it('renders namespaced scientific database results as a detailed public source card', () => {
    const message: IMessageToolCall = {
      id: 'geo-search-1',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'geo-search-1',
        name: 'mcp__omics-archives__geo_search_series',
        args: { term: 'immunotherapy single-cell response', retmax: 20 },
        status: 'completed',
        output: JSON.stringify({
          term: 'immunotherapy single-cell response',
          retrieved: 2,
          records: [
            {
              accession: 'GSE295600',
              title: 'Paired tumor biopsies before and after immunotherapy',
              summary: 'Human tumor single-cell profiles with treatment response annotations.',
            },
            {
              accession: 'GSE123813',
              title: 'T cell dynamics following checkpoint blockade',
            },
          ],
        }),
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    fireEvent.click(screen.getByRole('button', { name: /Search sources/ }));

    const sources = screen.getByRole('region', { name: 'messages.researchSources' });
    expect(within(sources).getByText('immunotherapy single-cell response')).toBeInTheDocument();
    expect(screen.getByText('Paired tumor biopsies before and after immunotherapy')).toBeInTheDocument();
    expect(
      screen.getByText('Human tumor single-cell profiles with treatment response annotations.')
    ).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Paired tumor biopsies before and after immunotherapy' })).toHaveAttribute(
      'href',
      'https://www.ncbi.nlm.nih.gov/geo/query/acc.cgi?acc=GSE295600'
    );
    expect(screen.queryByText(/mcp__|records|JSON|geo_search_series/u)).not.toBeInTheDocument();
  });

  it('turns identifier-only PubMed search results into auditable source links', () => {
    const message: IMessageToolCall = {
      id: 'pubmed-search-1',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'pubmed-search-1',
        name: 'mcp__pubmed__search_articles',
        args: { query: 'fluconazole pharmacokinetics review kidney excretion', max_results: 2 },
        status: 'completed',
        output: JSON.stringify({
          pmids: ['21635855', '16372823'],
          total_count: 4,
          returned_count: 2,
          query: 'fluconazole pharmacokinetics review kidney excretion',
        }),
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    fireEvent.click(screen.getByRole('button', { name: /Search sources/ }));

    const sources = screen.getByRole('region', { name: 'messages.researchSources' });
    expect(within(sources).getByText('fluconazole pharmacokinetics review kidney excretion')).toBeInTheDocument();
    expect(within(sources).getByRole('link', { name: 'PubMed PMID 21635855' })).toHaveAttribute(
      'href',
      'https://pubmed.ncbi.nlm.nih.gov/21635855/'
    );
    expect(within(sources).getByRole('link', { name: 'PubMed PMID 16372823' })).toHaveAttribute(
      'href',
      'https://pubmed.ncbi.nlm.nih.gov/16372823/'
    );
    expect(screen.queryByText(/mcp__|pmids|returned_count/u)).not.toBeInTheDocument();
  });

  it('renders plan steps with per-step detail and a feasibility assessment', () => {
    const message: IMessageToolCall = {
      id: 'plan-1',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'plan-1',
        name: 'generate_plan',
        args: {
          human_description: 'Planning a reproducible response analysis',
          task_summary: 'Compare treatment response before and after therapy',
          phases: [
            {
              title: 'Acquire evidence',
              steps: [
                {
                  title: 'Download the public cohort',
                  description: 'Retrieve the expression matrix and sample metadata, then verify their dimensions.',
                },
              ],
            },
          ],
          feasibility: {
            confidence: 'high',
            rationale: 'The cohort contains treatment timepoint and response annotations.',
          },
        },
        status: 'completed',
        output: JSON.stringify({ ok: true }),
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    const planRow = screen.getByRole('button', {
      name: /Planning a reproducible response analysis/,
    });
    expect(planRow).not.toHaveTextContent('Compare treatment response before and after therapy');
    fireEvent.click(planRow);

    expect(screen.getByTestId('tool-public-plan-summary')).toHaveTextContent(
      'Compare treatment response before and after therapy'
    );
    const plan = screen.getByTestId('tool-public-plan');
    expect(within(plan).queryByRole('button')).not.toBeInTheDocument();
    expect(within(plan).getByText('Download the public cohort')).toBeInTheDocument();
    expect(within(plan).getByText(/verify their dimensions/)).toBeInTheDocument();
    expect(screen.getByTestId('tool-public-plan-assessment')).toHaveTextContent('High confidence');
    expect(screen.getByTestId('tool-public-plan-assessment')).toHaveTextContent(/treatment timepoint/);
    expect(screen.queryByTestId('show-output-toggle')).not.toBeInTheDocument();
    expect(screen.queryByText('Output')).not.toBeInTheDocument();
    expect(screen.queryByText('PLAN')).not.toBeInTheDocument();
  });

  it('separates analysis code from its independently collapsible output', () => {
    const message: IMessageToolCall = {
      id: 'analysis-1',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'analysis-1',
        name: 'repl',
        args: {
          human_description: 'Checking cohort dimensions',
          language: 'python',
          background: false,
          code: 'print(matrix.shape)',
        },
        status: 'completed',
        output: JSON.stringify({ stdout: '(16291, 55738)\n', stderr: '', ok: true }),
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    fireEvent.click(screen.getByRole('button', { name: /Checking cohort dimensions/ }));

    expect(screen.getByText('Analysis code')).toBeInTheDocument();
    expect(screen.queryByText('Run in background')).not.toBeInTheDocument();
    expect(screen.getByTestId('tool-public-input-blocks').querySelector('code')).toHaveTextContent(
      'print(matrix.shape)'
    );
    const output = screen.getByTestId('show-output-toggle');
    expect(output).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(output);
    expect(output).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByTestId('tool-public-output-blocks').querySelector('code')).toHaveTextContent('(16291, 55738)');
    expect(screen.queryByText('(16291, 55738)', { selector: 'p' })).not.toBeInTheDocument();
  });

  it('shows public artifact metadata without storage identities', () => {
    const message: IMessageToolCall = {
      id: 'artifact-1',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'artifact-1',
        name: 'save_artifacts',
        args: { files: ['out/summary_report.md'] },
        status: 'completed',
        output: JSON.stringify({
          artifacts: [
            {
              artifact_id: 'private-artifact-id',
              version_id: 'private-version-id',
              root_frame_id: 'private-frame-id',
              storage_path: 'private/storage/path',
              filename: 'summary_report.md',
              content_type: 'text/markdown',
              size_bytes: 8192,
            },
          ],
        }),
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    fireEvent.click(screen.getByRole('button', { name: /Save results/ }));
    fireEvent.click(screen.getByTestId('show-output-toggle'));
    fireEvent.click(screen.getByRole('button', { name: /Deliverables, 1 item/ }));

    expect(screen.getByText('summary_report.md · text/markdown · 8.0 KB')).toBeInTheDocument();
    expect(
      screen.queryByText(/private-artifact|private-version|private-frame|private\/storage/u)
    ).not.toBeInTheDocument();
  });

  it('keeps file bodies as a third disclosure level while leaving the target visible', () => {
    const message: IMessageToolCall = {
      id: 'file-edit-1',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'file-edit-1',
        name: 'edit_file',
        args: {
          file_path: 'out/summary_report.md',
          old_string: 'Old public summary.',
          new_string: '# Updated summary\n\nValidated cohort statistics.',
        },
        status: 'completed',
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    fireEvent.click(screen.getByRole('button', { name: /Prepare content/ }));

    expect(screen.getAllByText('out/summary_report.md')).toHaveLength(2);
    const updated = screen.getByRole('button', { name: /Updated content/ });
    expect(updated).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByText(/Validated cohort statistics/)).not.toBeInTheDocument();
    fireEvent.click(updated);
    expect(updated).toHaveAttribute('aria-expanded', 'true');
    const markdown = screen.getByTestId('markdown-view');
    expect(markdown.closest('.tool-public-detail__block-body')).toHaveAttribute('aria-hidden', 'false');
  });

  it('shows method guidance directly inside the expanded method row', () => {
    const message: IMessageToolCall = {
      id: 'method-1',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'method-1',
        name: 'skill',
        args: { skill: 'scanpy', human_description: 'Loading single-cell analysis guidance' },
        status: 'completed',
        output: ['# Single-cell analysis', '', 'Use sample-level comparisons for treatment-response statistics.'].join(
          '\n'
        ),
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    fireEvent.click(screen.getByRole('button', { name: /Loading single-cell analysis guidance/ }));

    expect(screen.getByText('Analysis guide')).toBeInTheDocument();
    expect(screen.getByTestId('markdown-view')).toHaveTextContent('# Single-cell analysis');
    expect(screen.getByTestId('markdown-view')).toHaveTextContent(/sample-level comparisons/);
    expect(screen.queryByTestId('show-output-toggle')).not.toBeInTheDocument();
  });

  it('renders a layered, independently collapsible public detail for an environment step', () => {
    const message: IMessageToolCall = {
      id: 'environment-list-1',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'environment-list-1',
        name: 'manage_packages',
        args: {
          mode: 'list',
          language: 'python',
          environment: 'private-environment-name',
          human_description: 'Inspect analysis environment',
        },
        status: 'completed',
        output: JSON.stringify({
          environment: {
            name: 'private-environment-name',
            package_count: 6,
            packages: ['scanpy', 'anndata', 'harmony-py', 'leidenalg', 'numpy', 'pandas'],
          },
        }),
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    fireEvent.click(screen.getByRole('button', { name: /Inspect analysis environment/ }));

    const detail = screen.getByTestId('tool-public-detail');
    expect(within(detail).getByText('Mode')).toBeInTheDocument();
    expect(within(detail).getByText('List available')).toBeInTheDocument();
    expect(within(detail).getByText('Language')).toBeInTheDocument();
    expect(within(detail).getByText('Python')).toBeInTheDocument();
    const output = within(detail).getByRole('button', { name: /Output.*Completed.*Expand/i });
    expect(output).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(output);
    expect(output).toHaveAttribute('aria-expanded', 'true');

    const packages = within(detail).getByRole('button', { name: 'Available packages, 6 items' });
    expect(packages).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(packages);
    expect(packages).toHaveAttribute('aria-expanded', 'true');
    expect(within(detail).getByText('scanpy')).toBeInTheDocument();
    expect(within(detail).getByText('pandas')).toBeInTheDocument();

    fireEvent.click(within(detail).getByRole('button', { name: /Output.*Completed.*Collapse/i }));
    expect(output).toHaveAttribute('aria-expanded', 'false');
    expect(within(detail).queryByText('private-environment-name')).not.toBeInTheDocument();
    expect(detail.textContent).not.toContain('{"environment"');
  });

  it('keeps retrieval evidence behind the operation output instead of replacing it with a search card', () => {
    const message: IMessageToolCall = {
      id: 'retrieval-1',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'retrieval-1',
        name: 'web_fetch',
        args: {
          url: 'https://example.org/article/42',
          human_description: 'Retrieving the selected study abstract',
        },
        status: 'completed',
        output: JSON.stringify({ content: 'Public study abstract text.', returnedResults: 1 }),
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    fireEvent.click(screen.getByRole('button', { name: /Retrieving the selected study abstract/ }));

    expect(screen.queryByRole('region', { name: 'messages.researchSources' })).not.toBeInTheDocument();
    expect(screen.getByText('https://example.org/article/42')).toBeInTheDocument();
    fireEvent.click(screen.getByTestId('show-output-toggle'));
    expect(screen.getByText('Public study abstract text.')).toBeInTheDocument();
  });

  it('shows readable evidence from a collapsed connector dispatch without exposing the wrapper', () => {
    const wrapper: IMessageToolCall = {
      id: 'article-wrapper',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'article-wrapper',
        attempt: 1,
        operation_id: 'article-wrapper-operation',
        name: 'repl',
        args: {
          code: 'result = host.mcp("pubmed", "get_article_metadata", pmids=["41010003"])',
          human_description: 'Reviewing the selected pharmacokinetics article',
        },
        status: 'completed',
        output: JSON.stringify({
          stdout: 'Title: Fluconazole pharmacokinetics\nAbstract: Public evidence text.',
          kernel_id: 'private-kernel',
        }),
      },
    };
    const child: IMessageToolCall = {
      id: 'article-child',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'article-child',
        attempt: 1,
        operation_id: 'article-child-operation',
        parent_operation_id: 'article-wrapper-operation',
        name: 'mcp__pubmed__get_article_metadata',
        args: { pmids: ['41010003'] },
        status: 'completed',
        output: JSON.stringify({
          count: 1,
          articles: [{ title: 'Fluconazole pharmacokinetics', abstract: 'Public evidence text.' }],
        }),
      },
    };

    render(<MessageToolGroupSummary messages={[wrapper, child]} />);

    expect(screen.getAllByTestId('tool-chip')).toHaveLength(1);
    fireEvent.click(screen.getByRole('button', { name: /Reviewing the selected pharmacokinetics article/ }));
    fireEvent.click(screen.getByTestId('show-output-toggle'));
    fireEvent.click(screen.getByRole('button', { name: /Articles, 1 item/ }));

    expect(screen.getAllByText(/Title: Fluconazole pharmacokinetics/)).toHaveLength(1);
    expect(screen.getByText(/Abstract: Public evidence text/)).toBeInTheDocument();
    expect(document.body.textContent).not.toContain('host.mcp');
    expect(document.body.textContent).not.toContain('private-kernel');
  });

  it('renders compute providers as an independently expandable resource collection', () => {
    const message: IMessageToolCall = {
      id: 'compute-1',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'compute-1',
        name: 'list_compute',
        args: { human_description: 'Checking available compute resources' },
        status: 'completed',
        output: JSON.stringify({ providers: [{ name: 'gpu-lab', family: 'ssh' }] }),
      },
    };

    render(<MessageToolGroupSummary messages={[message]} />);
    fireEvent.click(screen.getByRole('button', { name: /Checking available compute resources/ }));
    expect(screen.getByText('COMPUTE')).toBeInTheDocument();
    fireEvent.click(screen.getByTestId('show-output-toggle'));
    const providers = screen.getByRole('button', { name: 'Available compute, 1 item' });
    expect(providers).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(providers);
    expect(providers).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByText('Name: gpu-lab · Family: ssh')).toBeInTheDocument();
  });

  it('renders memory and access evidence without public protocol identities', () => {
    const memory: IMessageToolCall = {
      id: 'memory-1',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'memory-1',
        name: 'search_memory',
        args: { query: 'assay preference', human_description: 'Reviewing relevant project memory' },
        status: 'completed',
        output: JSON.stringify({
          output: 'Use orthogonal confirmation [mem_private_123]',
          results_returned: 1,
        }),
      },
    };

    const { rerender } = render(<MessageToolGroupSummary messages={[memory]} />);
    fireEvent.click(screen.getByRole('button', { name: /Reviewing relevant project memory/ }));
    fireEvent.click(screen.getByTestId('show-output-toggle'));
    expect(screen.getByText('Use orthogonal confirmation [[memory record]]')).toBeInTheDocument();
    expect(document.body.textContent).not.toContain('mem_private_123');

    const access: IMessageToolCall = {
      id: 'access-1',
      conversation_id: 'conv-1',
      type: 'tool_call',
      content: {
        call_id: 'access-1',
        name: 'request_network_access',
        args: {
          domain: 'ftp.ncbi.nlm.nih.gov',
          reason: 'Download the selected cohort',
          human_description: 'Confirming access to the public data source',
        },
        status: 'completed',
        output: JSON.stringify({ granted: true, domain: 'ftp.ncbi.nlm.nih.gov' }),
      },
    };
    rerender(<MessageToolGroupSummary messages={[access]} />);
    fireEvent.click(screen.getByRole('button', { name: /Confirming access to the public data source/ }));
    fireEvent.click(screen.getByTestId('show-output-toggle'));
    expect(within(screen.getByTestId('tool-public-results')).getByText('Allowed')).toBeInTheDocument();
    expect(screen.getByText('Download the selected cohort')).toBeInTheDocument();
  });
});
