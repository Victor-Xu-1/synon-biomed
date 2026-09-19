import type { NormalizedToolCall } from '@/common/chat/normalizeToolCall';
import { normalizeToolCall } from '@/common/chat/normalizeToolCall';
import type { IMessageToolCall } from '@/common/chat/chatLib';
import {
  buildToolStepGroupSummary,
  buildToolStepLabel,
  buildToolStepPublicPresentation,
  buildToolStepResultSummary,
} from '@/renderer/pages/conversation/Messages/components/toolStepSummaryModel';
import { buildToolFailurePresentation } from '@/renderer/pages/conversation/Messages/components/toolFailurePresentation';
import { buildToolProgressPublicPresentation } from '@/renderer/pages/conversation/Messages/components/toolProgressPresentation';
import { isPublicToolActivity } from '@/renderer/pages/conversation/Messages/toolActivityPresentationRegistry';
import { describe, expect, it } from 'vitest';

const tool = (name: string, status: NormalizedToolCall['status'] = 'completed'): NormalizedToolCall => ({
  key: `${name}-${status}`,
  name,
  status,
});

describe('tool step summary model', () => {
  it('does not label settled or interrupted background operations as processing', () => {
    for (const [phase, label] of Object.entries({
      operation_completed: '已完成',
      operation_failed: '执行失败',
      operation_cancelled: '已取消',
      operation_interrupted: '运行中断',
      queued: '等待执行',
    })) {
      const presentation = buildToolProgressPublicPresentation({ phase }, 'zh-CN');
      expect(presentation.phaseLabel).toBe(label);
      expect(presentation.phasePercent).toBeNull();
    }
  });
  it('keeps waiting, blocked, interrupted, and unknown operation states distinct', () => {
    expect(buildToolStepResultSummary(tool('python', 'waiting'), 'en-US')).toBe('Waiting');
    expect(buildToolStepResultSummary(tool('python', 'blocked'), 'en-US')).toBe('Blocked');
    expect(buildToolStepResultSummary(tool('python', 'interrupted'), 'en-US')).toBe('Interrupted');
    expect(buildToolStepResultSummary(tool('python', 'unknown'), 'en-US')).toBe('Status unknown');
  });

  it('summarizes a mixed work trace with meaningful activities in Chinese', () => {
    expect(
      buildToolStepGroupSummary(
        [tool('python'), tool('bash'), tool('read_file'), tool('web_search'), tool('web_search', 'error')],
        'zh-CN'
      )
    ).toEqual({
      headline: '进行了 2 项分析、执行了 2 次检索、查看了 1 项资料',
      meta: '5 步 · 1 项未成功',
    });
  });

  it('uses the latest running human description for the visible activity while retaining counts', () => {
    const described = {
      ...tool('web_search', 'running'),
      humanDescription: '正在核对最新的人源靶点结构',
      description: 'python -c hidden-technical-command',
    };

    expect(buildToolStepLabel(described, 'zh-CN')).toBe('正在核对最新的人源靶点结构');
    expect(buildToolStepGroupSummary([tool('skill'), described], 'zh-CN')).toEqual({
      headline: '正在核对最新的人源靶点结构',
      meta: '2 步',
    });
    expect(buildToolStepLabel({ ...tool('repl'), description: 'raw command' }, 'en-US')).toBe('Analyze data');
  });

  it('falls back to localized action copy when the model description uses the other language', () => {
    const englishInChinese = {
      ...tool('python', 'running'),
      humanDescription: 'Generating a molecule library',
    };
    expect(buildToolStepLabel(englishInChinese, 'zh-CN')).toBe('开展分析');
    expect(buildToolStepGroupSummary([englishInChinese], 'zh-CN')).toEqual({
      headline: '进行了 1 项分析',
      meta: '1 步',
    });

    const chineseInEnglish = {
      ...tool('web_search', 'running'),
      humanDescription: '搜索最新结构',
    };
    expect(buildToolStepLabel(chineseInEnglish, 'en-US')).toBe('Search sources');
  });

  it('summarizes live and completed output without exposing raw content', () => {
    expect(buildToolStepResultSummary({ ...tool('python', 'running'), output: 'line one\nline two\n' }, 'zh-CN')).toBe(
      '进行中'
    );
    expect(buildToolStepResultSummary({ ...tool('read_file'), output: 'single result' }, 'en-US')).toBe(
      '1 line of output'
    );
    expect(buildToolStepResultSummary(tool('read_file', 'canceled'), 'en-US')).toBe('Stopped');
  });

  it('shows real environment progress on the stable running row in both languages', () => {
    const running = {
      ...tool('manage_environments', 'running'),
      humanDescription: '正在配置分子生成环境',
      progress: {
        phase: 'downloading_packages',
        phasePercent: 64,
        bytesPerSecond: 224_320,
        completedItems: 1,
        totalItems: 8,
        elapsedMs: 90_000,
        indeterminate: false,
      },
    };

    expect(buildToolStepPublicPresentation(running, 'zh-CN')).toMatchObject({
      label: '正在配置分子生成环境',
      detail: '下载依赖包 · 当前阶段 64% · 224 KB/s · 本步骤 1:30',
      resultSummary: '1 / 8 步',
    });
    expect(buildToolStepPublicPresentation({ ...running, humanDescription: undefined }, 'en-US')).toMatchObject({
      detail: 'Downloading packages · phase 64% · 224 KB/s · Step elapsed 1:30',
      resultSummary: '1 / 8 steps',
    });
  });

  it('shows real resumable-download percentage, transfer rate and elapsed time', () => {
    const running = {
      ...tool('download_public_scientific_file', 'running'),
      humanDescription: '正在下载公开数据文件',
      progress: {
        phase: 'downloading_file',
        phasePercent: 50,
        bytesCompleted: 50_000_000,
        bytesTotal: 100_000_000,
        bytesPerSecond: 224_320,
        elapsedMs: 90_000,
        indeterminate: false,
      },
    };

    expect(buildToolStepPublicPresentation(running, 'zh-CN')).toMatchObject({
      detail: '下载文件 · 已下载 50% · 50 MB / 100 MB · 224 KB/s · 剩余 50 MB · 本步骤 1:30',
      resultSummary: '下载 50%',
    });
    expect(
      buildToolStepPublicPresentation({ ...running, status: 'completed' }, 'zh-CN')
    ).toMatchObject({
      detail: '下载文件 · 已下载 50% · 50 MB / 100 MB · 224 KB/s · 剩余 50 MB · 本步骤 1:30',
      resultSummary: '已完成',
    });
  });

  it('uses domain result counts for saved artifacts and web searches', () => {
    expect(
      buildToolStepResultSummary(
        {
          ...tool('save_artifacts'),
          output: JSON.stringify({
            artifacts: [{ artifact_id: 'a' }, { artifact_id: 'b' }],
          }),
        },
        'en-US'
      )
    ).toBe('2 results');
    expect(
      buildToolStepResultSummary(
        {
          ...tool('web_search'),
          output: JSON.stringify({ results: [{ url: 'https://example.com' }] }),
        },
        'en-US'
      )
    ).toBe('1 result');

    expect(
      buildToolStepResultSummary(
        {
          ...tool('web_search'),
          output: JSON.stringify({
            result: { diagnostics: { returnedResults: 10 }, results: [] },
          }),
        },
        'en-US'
      )
    ).toBe('No usable sources');

    expect(
      buildToolStepResultSummary(
        {
          ...tool('web_search'),
          truncated: true,
          compactResultCount: 10,
          output:
            '{"result":{"diagnostics":{"httpBackends":[{"returnedResults":0},{"returnedResults":10}],"returnedResults":10},"results":[…',
        },
        'zh-CN'
      )
    ).toBe('10 条结果');

    expect(
      buildToolStepResultSummary(
        {
          ...tool('web_search'),
          input: JSON.stringify({ query: 'public cohort' }),
          output: 'https://example.org/one\nhttps://example.org/two',
        },
        'en-US'
      )
    ).toBe('2 results');
  });

  it('keeps internal environment identifiers out of the collapsed row', () => {
    expect(
      buildToolStepPublicPresentation(
        {
          ...tool('manage_environments'),
          input: JSON.stringify({
            mode: 'preflight',
            name: 'scrnaseq-immunotherapy-analysis',
          }),
        },
        'en-US'
      )
    ).toMatchObject({
      label: 'Prepare the analysis environment',
      detail: null,
    });
  });

  it('turns structured traceback output into a short redacted failure summary', () => {
    const rawOutput = JSON.stringify({
      error_code: 'provider_timeout',
      stderr: [
        'Traceback (most recent call last):',
        '  File "/home/victor_1/.synon-biomed-v0.1.0/run.py", line 42, in <module>',
        'RuntimeError: upstream request failed (api_key=sk-1234567890abcdef)',
      ].join('\n'),
    });

    const presentation = buildToolFailurePresentation(rawOutput);

    expect(presentation.summary).toContain('RuntimeError: upstream request failed');
    expect(presentation.summary).not.toContain('Traceback');
    expect(presentation.summary).not.toContain('sk-1234567890abcdef');
    expect(presentation.summary).not.toContain('/home/victor_1');
    expect(presentation.errorCode).toBe('provider_timeout');
    expect(buildToolStepResultSummary({ ...tool('python', 'error'), output: rawOutput }, 'en-US')).toBe('Failed');
    expect(buildToolStepPublicPresentation({ ...tool('python', 'error'), output: rawOutput }, 'en-US')).toMatchObject({
      resultSummary: 'Failed',
      failureSummary: 'This step did not complete; the task can retry or use another approach.',
    });
  });

  it('preserves the canonical tool error field for complete failure detail', () => {
    const normalized = normalizeToolCall({
      id: 'message-error',
      conversation_id: 'conversation-1',
      type: 'tool_call',
      content: {
        call_id: 'call-error',
        name: 'python',
        args: {},
        status: 'error',
        error: 'RuntimeError: failed safely',
      },
    } as IMessageToolCall);
    expect(normalized).toMatchObject({
      status: 'error',
      output: 'RuntimeError: failed safely',
    });
  });

  it('uses an externalized large-result preview instead of exposing the storage envelope', () => {
    const envelope = JSON.stringify({
      artifact_id: 'large-tool-result-628f5ebb7d8ed3ad60208c5cbc06d279',
      content_type: 'application/json',
      content_url: '/api/artifacts/internal/versions/internal',
      outcome: 'failed',
      preview: '{"code":"software_output_validation_failed","ok":false,"stderr":"SMILES Parse Error:',
      truncated: true,
    });

    const presentation = buildToolFailurePresentation(envelope);

    expect(presentation.summary).toBe('software_output_validation_failed');
    expect(presentation.summary).not.toContain('artifact');
    expect(buildToolStepResultSummary({ ...tool('python', 'error'), output: envelope }, 'zh-CN')).toBe('未完成');
  });

  it('redacts secrets passed through command-line arguments', () => {
    const presentation = buildToolFailurePresentation(
      'command failed: provider --api-key synthetic-secret --token "synthetic token" --mode safe'
    );

    expect(presentation.summary).toBe('command failed: provider --api-key [REDACTED] --token [REDACTED] --mode safe');
    expect(presentation.summary).not.toContain('synthetic-secret');
    expect(presentation.summary).not.toContain('synthetic token');
    expect(buildToolFailurePresentation({ code: 'sk-1234567890abcdef' }).errorCode).toBeNull();
    expect(buildToolFailurePresentation({ code: 'provider_timeout' }).errorCode).toBe('provider_timeout');
    expect(buildToolFailurePresentation({ code: 'sk-1234567890abcdef' }).summary).not.toContain('sk-');
  });

  it('keeps internal-only progress operations out of the public timeline', () => {
    for (const name of [
      'update_step_status',
      'wait_for_notification',
      'boundary',
      'ask_user',
      'submit_output',
      'verdict',
      'record_summary',
      'summarize_conversation',
      'emit_memories',
      'report_input_files',
      'select_relevant_inputs',
      'select_skills',
      'create_work_item',
      'read_onboarding_attachment',
    ]) {
      expect(isPublicToolActivity(name), name).toBe(false);
    }
    expect(isPublicToolActivity('generate_plan')).toBe(true);
  });

  it('turns namespaced provider tools into user-facing actions without exposing internal names', () => {
    const namespacedSearch = tool('mcp_chemistry_pubchem_search_compounds');

    expect(buildToolStepGroupSummary([namespacedSearch], 'en-US')).toEqual({
      headline: 'Ran a search',
      meta: '1 step',
    });
    expect(buildToolStepLabel(namespacedSearch, 'en-US')).toBe('Search sources');

    const namespacedSelection = {
      ...tool('mcp__structures-interactions__pdb_select_latest_liganded_structure'),
      input: JSON.stringify({ uniprot_accession: 'Q9Y2W8' }),
      output: JSON.stringify({ selected: false, inspected_candidate_count: 4 }),
    };
    expect(buildToolStepPublicPresentation(namespacedSelection, 'zh-CN')).toMatchObject({
      label: '查看资料',
      detail: 'Q9Y2W8',
    });
  });

  it('rejects internal construction narration at the public projection boundary', () => {
    const internalDescription = {
      ...tool('skill', 'running'),
      humanDescription: '加载 medicinal-chemistry skill 并写入 source_evidence.json',
      input: '{"path":"/home/private/source_evidence.json"}',
      output: 'raw stdout',
    };

    expect(buildToolStepLabel(internalDescription, 'zh-CN')).toBe('准备分析');
    expect(buildToolStepPublicPresentation(internalDescription, 'zh-CN')).toMatchObject({
      label: '准备分析',
      detail: null,
      resultSummary: '进行中',
      expandable: true,
      failureSummary: null,
    });
  });

  it('uses stable public action labels instead of model-authored file construction narration', () => {
    expect(
      buildToolStepLabel(
        {
          ...tool('save_artifacts'),
          humanDescription: '重新保存修正引用后的报告并添加 overall_pass 标志',
        },
        'zh-CN'
      )
    ).toBe('保存结果');
    expect(
      buildToolStepLabel(
        {
          ...tool('read_file'),
          humanDescription: '读取当前报告文件，准备修改引用',
        },
        'zh-CN'
      )
    ).toBe('读取当前报告文件，准备修改引用');
    expect(
      buildToolStepLabel(
        {
          ...tool('read_file'),
          humanDescription: '再次检查报告中是否还有未解析的artifact引用',
        },
        'zh-CN'
      )
    ).toBe('查看资料');
    expect(
      buildToolStepLabel(
        {
          ...tool('repl'),
          humanDescription: '查看GEO搜索结果的原始结构',
        },
        'zh-CN'
      )
    ).toBe('开展分析');
  });

  it('uses safe model-authored labels and public query context for tool rows', () => {
    const described = {
      ...tool('repl'),
      humanDescription: '分析治疗前后细胞群变化',
      input: '{"code":"private implementation"}',
    };
    expect(buildToolStepPublicPresentation(described, 'zh-CN')).toMatchObject({
      label: '分析治疗前后细胞群变化',
      detail: null,
    });

    const search = {
      ...tool('mcp_omics_geo_search_series'),
      input: '{"term":"tumor immunotherapy single-cell response"}',
      output: '{"retrieved":8,"records":[]}',
    };
    expect(buildToolStepPublicPresentation(search, 'en-US')).toMatchObject({
      label: 'Search sources',
      detail: 'tumor immunotherapy single-cell response',
      resultSummary: '8 results',
      showResearchSources: true,
    });
  });

  it('counts scientific MCP records nested inside a generic result envelope', () => {
    expect(
      buildToolStepResultSummary(
        {
          ...tool('mcp__chembl__compound_search'),
          output: JSON.stringify({
            ok: true,
            result: JSON.stringify({
              api_total: 181,
              n_records_returned: 20,
              compounds: Array.from({ length: 20 }, (_, index) => ({
                id: index,
              })),
            }),
          }),
        },
        'zh-CN'
      )
    ).toBe('20 条结果');

    expect(
      buildToolStepResultSummary(
        {
          ...tool('mcp__literature__openalex_search_works'),
          output: JSON.stringify({
            api_total: 4821,
            records: [{ id: 'one' }, { id: 'two' }],
          }),
        },
        'en-US'
      )
    ).toBe('2 results');
  });

  it('counts nested plan steps like the reference timeline', () => {
    const plan = {
      ...tool('generate_plan'),
      humanDescription: '规划单细胞免疫治疗响应分析',
      input: JSON.stringify({
        plan: {
          phases: [
            { delegations: [{ steps: [{ title: 'one' }, { title: 'two' }] }] },
            {
              delegations: [{ steps: [{ title: 'three' }] }, { steps: [{ title: 'four' }] }],
            },
          ],
        },
      }),
      output: '{"ok":true}',
    };

    expect(buildToolStepPublicPresentation(plan, 'zh-CN')).toMatchObject({
      label: '规划单细胞免疫治疗响应分析',
      resultSummary: '4 个步骤',
    });
  });

  it('counts the production plan envelope without flattening phase and delegation detail', () => {
    const plan = {
      ...tool('generate_plan'),
      input: JSON.stringify({
        phases: [
          {
            delegations: [{ steps: [{ title: 'one' }, { title: 'two' }] }],
            steps: [{ title: 'three' }],
          },
          { delegations: [{ steps: [{ title: 'four' }, { title: 'five' }] }] },
        ],
      }),
    };

    expect(buildToolStepResultSummary(plan, 'zh-CN')).toBe('5 个步骤');
    expect(buildToolStepPublicPresentation({ ...plan, truncated: true }, 'zh-CN')).toMatchObject({
      shouldLoadFull: true,
      expandable: true,
    });
  });
});
describe('tool progress process identity', () => {
  it('exposes the running installer process in detail and rows', () => {
    const presentation = buildToolProgressPublicPresentation(
      { phase: 'installing_packages', process: 'pip', indeterminate: true },
      'zh-CN'
    );
    expect(presentation.rows[0]).toEqual({ label: '当前阶段', value: '写入依赖包' });
    expect(presentation.rows[1]).toEqual({ label: '运行进程', value: 'pip' });
    expect(presentation.compactDetail).toContain('pip');
    const english = buildToolProgressPublicPresentation(
      { phase: 'downloading_packages', process: 'micromamba', indeterminate: true },
      'en-US'
    );
    expect(english.rows[1]).toEqual({ label: 'Running process', value: 'micromamba' });
  });
  it('omits the process row when the installer identity is unknown', () => {
    const presentation = buildToolProgressPublicPresentation({ phase: 'queued', indeterminate: true }, 'zh-CN');
    expect(presentation.rows.some((row) => row.label === '运行进程')).toBe(false);
  });
});
