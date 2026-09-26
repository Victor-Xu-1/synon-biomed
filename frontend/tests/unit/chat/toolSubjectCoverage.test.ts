import { describe, expect, it } from 'vitest';
import type { NormalizedToolCall } from '@/common/chat/normalizeToolCall';
import { buildToolStepPublicPresentation } from '@/renderer/pages/conversation/Messages/components/toolStepSummaryModel';
import { buildToolProgressPublicPresentation } from '@/renderer/pages/conversation/Messages/components/toolProgressPresentation';
import {
  toolOperationAction,
  toolOperationSubject,
} from '@/renderer/pages/conversation/Messages/components/toolOperationSubject';

const present = (name: string, input: unknown, extra: Partial<NormalizedToolCall> = {}) =>
  buildToolStepPublicPresentation(
    { key: 'operation', name, input: JSON.stringify(input), status: 'completed', ...extra },
    'zh-CN'
  );

describe('all public operation subjects', () => {
  it.each([
    ['read_file', { file_path: '/home/person/project/structure_1.cif' }, 'structure_1.cif'],
    ['edit_file', { file_path: 'output/study_plan.md' }, 'study_plan.md'],
    ['save_artifacts', { files: ['output/study_plan.md', 'data/structure.cif'] }, 'study_plan.md'],
    ['manage_packages', { action: 'install', packages: ['numpy>=2', 'scipy'], environment: 'analysis' }, 'numpy>=2'],
    ['manage_environments', { action: 'create', name: 'analysis', packages: ['numpy'] }, 'numpy'],
    ['manage_environments', { mode: 'create', pip_phases: [['packaging==24.2']] }, 'packaging==24.2'],
    ['python', { code: 'x = 1\nprint(x)' }, '2'],
    ['web_search', { query: 'CRBN structures' }, 'CRBN structures'],
    ['web_fetch', { url: 'https://example.org/study?a=secret' }, 'example.org'],
    ['search_skills', { query: 'structural analysis' }, 'structural analysis'],
    ['generate_plan', { task_summary: '比较实验结果' }, '比较实验结果'],
    ['submit_job', { job_name: 'binding analysis' }, 'binding analysis'],
    ['read_memory', { entity: 'CRBN' }, 'CRBN'],
    ['request_network_access', { domain: 'example.org' }, 'example.org'],
    ['mcp__science__gene_variants', { input: { gene_symbol: 'BRCA1' } }, 'BRCA1'],
    ['custom_operation', { subject: '实验数据' }, '实验数据'],
  ])('%s retains its available public subject', (name, input, expected) => {
    expect(present(name, input).detail).toContain(expected);
  });

  it('keeps software actions and packages visible during progress', () => {
    const result = present(
      'manage_packages',
      { mode: 'install', packages: ['numpy'] },
      {
        status: 'running',
        progress: { phase: 'resolving_dependencies', indeterminate: true },
      }
    );
    expect(result.label).toContain('安装');
    expect(result.detail).toContain('numpy');
    expect(result.detail).toContain('解析');
  });

  it('bounds and sanitizes untrusted subjects without exposing credentials or host paths', () => {
    expect(present('read_file', { file_path: 'C:\\Users\\person\\work\\result.csv' }).detail).toBe('result.csv');
    expect(present('web_fetch', { url: 'https://user:password@example.org/file?token=secret' }).detail).toBe(
      'example.org'
    );
    expect(present('read_file', { file_path: '/home/user/.env' }).detail).toBeNull();
    expect(present('web_search', { query: 'api_key=secret' }).detail).toBeNull();
    expect(present('custom_operation', { subject: '<script>alert(1)</script>' }).detail).toBeNull();
  });
});

describe('truthful phase progress', () => {
  it('distinguishes environment reuse and background acceptance from finished work', () => {
    expect(
      present(
        'manage_environments',
        { mode: 'create' },
        { output: JSON.stringify({ mode: 'reuse', environment: { status: 'ready' } }) }
      ).resultSummary
    ).toBe('复用已有环境');
    expect(
      present(
        'manage_packages',
        { mode: 'install' },
        { output: JSON.stringify({ status: 'running', operation_id: 'opaque' }) }
      ).resultSummary
    ).toBe('后台执行中');
    expect(
      present(
        'manage_environments',
        { mode: 'list' },
        { output: JSON.stringify({ environments: [{}, {}], mode: 'list' }) }
      ).resultSummary
    ).toBe('2 个环境');
  });
  it('does not call unknown progress environment setup', () => {
    expect(
      buildToolProgressPublicPresentation({ phase: 'unknown_future_phase', indeterminate: true }, 'zh-CN').phaseLabel
    ).toBe('处理中');
  });
  it('covers the actual installer verification phase', () => {
    expect(
      buildToolProgressPublicPresentation({ phase: 'verifying_environment', indeterminate: true }, 'zh-CN').phaseLabel
    ).toBe('验证环境');
  });
  it('keeps a terminal installer phase readable without showing a percentage', () => {
    const presentation = buildToolProgressPublicPresentation(
      { phase: 'installer_process_completed', phasePercent: 100, indeterminate: false },
      'zh-CN'
    );
    expect(presentation.compactDetail).toBe('依赖安装完成');
    expect(JSON.stringify(presentation.rows)).not.toContain('%');
  });
  it('shows observed bytes when the transfer size is unknown', () => {
    const presentation = buildToolProgressPublicPresentation(
      { phase: 'downloading_file', bytesCompleted: 2048, bytesPerSecond: 1024, indeterminate: true },
      'zh-CN'
    );
    expect(presentation.rows).toContainEqual({ label: '已下载', value: '2.05 KB' });
    expect(presentation.rows).toContainEqual({ label: '传输速度', value: '1.02 KB/s' });
    expect(presentation.rows).not.toContainEqual(expect.objectContaining({ label: '剩余' }));
  });
});

describe('localized operation copy', () => {
  it.each([
    ['install', '安装软件包', 'Install packages'],
    ['remove', '移除软件包', 'Remove packages'],
    ['uninstall', '移除软件包', 'Remove packages'],
    ['update', '更新软件包', 'Update packages'],
    ['create', '创建分析环境', 'Create environment'],
    ['list', '查看可用环境与软件', 'List environments and packages'],
    ['inspect', '检查分析环境', 'Inspect environment'],
    ['delete', '删除分析环境', 'Delete environment'],
  ])('localizes %s through the shared catalog', (mode, chinese, english) => {
    const tool: NormalizedToolCall = {
      key: 'locale',
      name: 'manage_packages',
      input: JSON.stringify({ mode }),
      status: 'running',
    };
    expect(toolOperationAction(tool, true)).toBe(chinese);
    expect(toolOperationAction(tool, false)).toBe(english);
  });

  it.each([
    ['queued', '等待执行', 'Queued'],
    ['operation_completed', '已完成', 'Completed'],
    ['operation_failed', '执行失败', 'Failed'],
    ['operation_cancelled', '已取消', 'Cancelled'],
    ['operation_interrupted', '运行中断', 'Interrupted'],
    ['unknown_future_phase', '处理中', 'Processing'],
    ['constructor', '处理中', 'Processing'],
  ])('preserves the localized lifecycle state %s', (phase, chinese, english) => {
    expect(buildToolProgressPublicPresentation({ phase }, 'zh-CN').phaseLabel).toBe(chinese);
    expect(buildToolProgressPublicPresentation({ phase }, 'en-US').phaseLabel).toBe(english);
  });

  it.each([
    ['zh-CN', '已用时 1:05', '剩余 750 B', '2 行任务代码', 'a, b, c 等'],
    ['en-US', 'Elapsed 1:05', '750 B remaining', '2 lines of task code', 'a, b, c, …'],
  ])('interpolates real observations without changing units in %s', (language, elapsed, remaining, code, subjects) => {
    const chinese = language.startsWith('zh');
    const milestones = buildToolProgressPublicPresentation(
      { phase: 'queued', elapsedMs: 65000, completedItems: 1, totalItems: 4 },
      language
    );
    expect(milestones.compactDetail).toContain(elapsed);
    expect(JSON.stringify(milestones)).not.toContain('1 / 4');
    const transfer = buildToolProgressPublicPresentation(
      { phase: 'downloading_file', bytesCompleted: 250, bytesTotal: 1000 },
      language
    );
    expect(transfer.compactDetail).toContain('250 B / 1 KB');
    expect(transfer.compactDetail).toContain(remaining);
    expect(transfer.compactDetail).not.toContain('%');
    const phase = buildToolProgressPublicPresentation({ phase: 'installing_packages', phasePercent: 50 }, language);
    expect(phase.compactDetail).not.toContain('%');
    expect(
      toolOperationSubject(
        { key: 'code', name: 'python', status: 'running', input: JSON.stringify({ code: 'x = 1\nprint(x)' }) },
        chinese
      )
    ).toBe(`python · ${code}`);
    expect(
      toolOperationSubject(
        {
          key: 'list',
          name: 'manage_packages',
          status: 'running',
          input: JSON.stringify({ packages: ['a', 'b', 'c', 'd'] }),
        },
        chinese
      )
    ).toBe(subjects);
    expect(
      toolOperationAction(
        { key: 'unknown', name: 'manage_packages', status: 'running', input: JSON.stringify({ mode: '__proto__' }) },
        chinese
      )
    ).toBeUndefined();
  });
});
