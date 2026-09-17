import type { NormalizedToolCall } from '@/common/chat/normalizeToolCall';
import { describe, expect, it } from 'vitest';
import { buildToolPublicDetailPresentation } from '@/renderer/pages/conversation/Messages/toolDetails/detailProjection';

const tool = (overrides: Partial<NormalizedToolCall> = {}): NormalizedToolCall => ({
  key: 'tool-1',
  name: 'manage_environments',
  status: 'completed',
  input: JSON.stringify({
    mode: 'list',
    dependencies: ['scanpy', 'anndata', 'leidenalg'],
  }),
  output: JSON.stringify({
    count: 2,
    environments: [{ name: 'python' }, { name: 'r' }],
  }),
  ...overrides,
});

describe('tool detail projection', () => {
  it('uses one private-field policy for summary rows and nested evidence', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'python',
        output: JSON.stringify({
          environment_generation: 'private-generation',
          metadata: { runtimeGeneration: 'private-runtime', pdb_id: '4ABC' },
          stdout: 'verified',
        }),
      }),
      'zh-CN',
      '1 行输出'
    );
    const serialized = JSON.stringify(detail);
    expect(serialized).not.toContain('private-generation');
    expect(serialized).not.toContain('private-runtime');
    expect(serialized).toContain('4ABC');
  });
  it('localizes known expanded field labels and booleans through the shared formatter', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({ name: 'python', input: JSON.stringify({ background: false }), output: '{}' }),
      'zh-CN',
      null
    );
    const tree = JSON.stringify(detail.inputTree);
    expect(tree).toContain('后台运行');
    expect(tree).toContain('否');
  });
  it('projects observed setup progress as readable detail without raw installer output', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        status: 'running',
        output: undefined,
        progress: {
          phase: 'extracting_packages',
          phasePercent: 72,
          bytesPerSecond: 1_500_000,
          completedItems: 2,
          totalItems: 8,
          elapsedMs: 125_000,
          indeterminate: false,
        },
      }),
      'zh-CN',
      '25%'
    );

    expect(detail.resultRows).toEqual([
      { label: '当前阶段', value: '解压依赖包' },
      { label: '步骤完成比例', value: '25%' },
      { label: '阶段进度', value: '72%' },
      { label: '传输速度', value: '1.5 MB/s' },
      { label: '配置步骤', value: '2 / 8' },
      { label: '本步骤已用时', value: '2:05' },
    ]);
  });

  it('projects download progress with real bytes and transfer speed', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'download_public_scientific_file',
        status: 'running',
        output: undefined,
        progress: {
          phase: 'downloading_file',
          phasePercent: 25,
          bytesCompleted: 25_000_000,
          bytesTotal: 100_000_000,
          bytesPerSecond: 500_000,
          elapsedMs: 60_000,
          indeterminate: false,
        },
      }),
      'zh-CN',
      '25%'
    );

    expect(detail.resultRows).toEqual([
      { label: '当前阶段', value: '下载文件' },
      { label: '下载进度', value: '25%' },
      { label: '已下载', value: '25 MB / 100 MB' },
      { label: '传输速度', value: '500 KB/s' },
      { label: '剩余', value: '75 MB' },
      { label: '本步骤已用时', value: '1:00' },
    ]);
  });

  it('projects public inputs and structured counts without rendering raw serialization', () => {
    const detail = buildToolPublicDetailPresentation(tool(), 'en-US', 'Completed');

    expect(detail.toolLabel).toBe('ENVIRONMENT');
    expect(detail.detailKind).toBe('environment');
    expect(detail.toolContext).toBeNull();
    expect(detail.resultSummary).toBe('Completed');
    expect(detail.inputRows).toEqual([{ label: 'Mode', value: 'List available' }]);
    expect(detail.inputCollections).toEqual([
      {
        label: 'Dependencies',
        items: ['scanpy', 'anndata', 'leidenalg'],
        total: 3,
      },
    ]);
    expect(detail.resultRows).not.toContainEqual({
      label: 'Status',
      value: 'Completed',
    });
    expect(detail.resultRows).not.toContainEqual({
      label: 'Count',
      value: '2 items',
    });
    expect(detail.resultRows).toContainEqual({
      label: 'Environments',
      value: '2 items',
    });
    expect(detail.resultCollections).toContainEqual({
      label: 'Available environments',
      items: ['python', 'r'],
      total: 2,
    });
    expect(JSON.stringify(detail)).not.toContain('{"environments"');
  });

  it('projects nested environment results into detailed safe rows and expandable collections', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        input: JSON.stringify({
          mode: 'create',
          language: 'python',
          python_version: '3.11',
          packages: ['scanpy', 'anndata', 'harmony-py'],
          resource_requirements: { cpu_cores: 4, memory_gb: 12, disk_gb: 30 },
          name: 'private-internal-environment',
          channels: ['conda-forge'],
        }),
        output: JSON.stringify({
          environment: {
            name: 'private-internal-environment',
            package_count: 198,
            packages: ['scanpy', 'anndata', 'harmony-py', 'leidenalg', 'numpy', 'pandas'],
          },
          requested_packages: ['scanpy', 'anndata', 'harmony-py'],
        }),
      }),
      'zh-CN',
      '已完成'
    );

    expect(detail.inputRows).toEqual([
      { label: '方式', value: '准备分析环境' },
      { label: '运行语言', value: 'Python' },
      { label: '语言版本', value: '3.11' },
      { label: '处理器', value: '4 核' },
      { label: '内存', value: '12 GB' },
      { label: '存储空间', value: '30 GB' },
    ]);
    expect(detail.inputCollections).toContainEqual({
      label: '计划使用的组件',
      items: ['scanpy', 'anndata', 'harmony-py'],
      total: 3,
    });
    expect(detail.resultRows).toContainEqual({
      label: '分析组件',
      value: '198 项',
    });
    expect(detail.resultCollections).toContainEqual({
      label: '可用分析组件',
      items: ['scanpy', 'anndata', 'harmony-py', 'leidenalg', 'numpy', 'pandas'],
      total: 6,
    });
    const rendered = JSON.stringify(detail);
    expect(rendered).not.toContain('private-internal-environment');
    expect(rendered).not.toContain('conda-forge');
  });

  it('omits generated environment identities and counts only public environment records', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        output: JSON.stringify({
          count: 5,
          environments: [
            { name: 'single-cell-analysis', language: 'python' },
            { name: 'swr-00fc4427146ffbdef4ad454e', language: 'python' },
            { name: 'chem_runtime_acceptance_20260813', language: 'python' },
            { name: 'synon-pkg-99236c85d317c5d7e7ca88a5', language: 'python' },
            { name: 'single-cell-analysis', language: 'python' },
          ],
        }),
      }),
      'en-US',
      'Completed'
    );

    expect(detail.resultRows).toContainEqual({
      label: 'Environments',
      value: '1 item',
    });
    expect(detail.resultRows).not.toContainEqual({
      label: 'Count',
      value: '5 items',
    });
    expect(detail.resultCollections).toContainEqual({
      label: 'Available environments',
      items: ['single-cell-analysis · Python'],
      total: 1,
    });
    expect(JSON.stringify(detail)).not.toMatch(/swr-|acceptance|synon-pkg/u);
  });

  it('keeps environment preflight details useful without leaking generated runtime identities', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        input: JSON.stringify({
          language: 'python',
          create: false,
          force: false,
          packages: ['numpy', 'pandas', 'matplotlib', 'scipy'],
        }),
        output: JSON.stringify({
          compatible_environment_count: 110,
          feasible: true,
          mode: 'preflight',
          omitted_compatible_environment_count: 102,
          operation: 'create',
          recommendation: 'reuse',
          compatible_environments: ['swr-86bf88c025fa81d27d3771bf', 'swr-acaca0ac58127f1fc5d7ce61'],
          mutation_blockers: ['insufficient_disk'],
        }),
      }),
      'zh-CN',
      '已完成'
    );

    expect(detail.inputRows).toEqual([{ label: '运行语言', value: 'Python' }]);
    expect(detail.resultRows).toEqual(
      expect.arrayContaining([
        { label: '可用环境', value: '110' },
        { label: '执行条件', value: '可执行' },
        { label: '检查方式', value: '环境预检' },
        { label: '建议操作', value: '准备分析环境' },
        { label: '建议', value: '复用已有环境' },
      ])
    );
    expect(detail.resultCollections).toContainEqual({
      label: '新建环境限制',
      items: ['存储空间不足'],
      total: 1,
    });
    const rendered = JSON.stringify(detail);
    expect(rendered).not.toMatch(/swr-|omitted|Create|Force/u);
  });

  it('keeps task code auditable while redacting host paths, secrets and product implementation text', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'python',
        input: JSON.stringify({
          code: 'print(1)',
          path: '/home/victor/private/input.csv',
          human_description: 'Run internal Python tool',
        }),
        output: JSON.stringify({
          stdout: 'calculation complete\napi_key=sk-secret\n1',
          message: 'Harness prompt projection repaired',
        }),
      }),
      'en-US',
      '4 lines of output'
    );

    const rendered = JSON.stringify(detail);
    expect(rendered).toContain('4 lines of output');
    expect(rendered).toContain('print(1)');
    expect(rendered).not.toContain('/home/victor');
    expect(rendered).not.toContain('sk-secret');
    expect(rendered).toContain('[redacted]');
    expect(rendered).not.toContain('Harness prompt projection');
    expect(rendered).not.toContain('internal Python tool');
  });

  it('keeps execution infrastructure fields out while retaining scientific result fields', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'python',
        input: JSON.stringify({ code: 'print(result)', language: 'python' }),
        output: JSON.stringify({
          cell_index: 1,
          exit_status: 'ok',
          kernel_kind: 'analysis',
          ok: true,
          reused: false,
          files_written: ['exposure.csv'],
          selected_count: 6,
          score: 0.87,
          stdout: 'selected=6\nscore=0.87',
        }),
      }),
      'en-US',
      '2 lines of output'
    );

    const rendered = JSON.stringify(detail);
    expect(detail.resultRows).toEqual(
      expect.arrayContaining([
        { label: 'Generated results', value: '1 item' },
        { label: 'Selected', value: '6 items' },
        { label: 'Score', value: '0.87' },
      ])
    );
    expect(rendered).not.toMatch(/Cell Index|Exit Status|Kernel Kind|Reused|"Ok"/u);
  });

  it('keeps all public scalar and nested record fields accessible while redacting credentials', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'mcp__evidence__lookup',
        output: JSON.stringify({
          record: {
            id: '7df72f3a-9f43-4f4d-9897-fdc67e346acc',
            sample_id: 'SAMN12345678',
            evidence_uri: 'https://example.org/evidence/42',
            score: 0.91,
            nested: { cohort: 'validation', samples: 128 },
            api_key: 'must-not-render',
            credentials: { username: 'private-user' },
            session_id: 'private-session',
            owner_id: 'private-owner',
            source_event_id: 42,
          },
        }),
      }),
      'en-US',
      'Completed'
    );

    const tree = JSON.stringify(detail.outputTree);
    expect(tree).toContain('7df72f3a-9f43-4f4d-9897-fdc67e346acc');
    expect(tree).toContain('SAMN12345678');
    expect(tree).toContain('https://example.org/evidence/42');
    expect(tree).toContain('0.91');
    expect(tree).toContain('validation');
    expect(tree).toContain('128');
    expect(tree).not.toContain('must-not-render');
    expect(tree).not.toMatch(/private-(?:user|session|owner)/u);
    expect(tree).not.toContain('Source Event Id');
    expect(tree).not.toContain('Api Key');
  });

  it('redacts an internal fragment without deleting public evidence on the same output line', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'python',
        output: JSON.stringify({
          stdout: 'Observed affinity improved; runtime policy detail omitted; confidence remains moderate.',
        }),
      }),
      'en-US',
      '1 line of output'
    );

    const rendered = JSON.stringify(detail);
    expect(rendered).toContain('Observed affinity improved');
    expect(rendered).toContain('confidence remains moderate');
    expect(rendered).not.toContain('runtime policy detail');
  });

  it('turns retrieval receipts into readable evidence instead of transport diagnostics', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'fetch_article_fulltext',
        input: JSON.stringify({
          url: 'https://europepmc.org/article/MED/12345678',
        }),
        output: JSON.stringify({
          available: false,
          doi: '10.1000/example',
          pmcid: 'PMC12345678',
          reason: 'full_text_not_found',
          source: 'Europe PMC',
          sourceUrl: 'https://europepmc.org/article/MED/12345678',
          status_code: 200,
          backend: 'GoHTTPBackend',
        }),
      }),
      'zh-CN',
      '已完成'
    );

    expect(detail.resultRows).toEqual(
      expect.arrayContaining([
        { label: '全文可用性', value: '未发现开放获取内容' },
        { label: 'DOI', value: '10.1000/example' },
        { label: 'PMC 编号', value: 'PMC12345678' },
        { label: '获取说明', value: '未发现开放获取全文' },
        { label: '资料来源', value: 'Europe PMC' },
        {
          label: '来源链接',
          value: 'https://europepmc.org/article/MED/12345678',
        },
      ])
    );
    expect(JSON.stringify(detail)).not.toMatch(/Status Code|GoHTTPBackend/u);
  });

  it('extracts safe plan titles into a readable ordered list', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'generate_plan',
        input: JSON.stringify({
          task_summary: 'Compare responder signatures',
          steps: [
            { title: 'Select a public cohort' },
            { title: 'Compare cell populations' },
            { title: 'Validate marker signatures' },
          ],
        }),
      }),
      'en-US',
      '3 steps'
    );

    expect(detail.planDocument?.phases[0].steps).toEqual([
      expect.objectContaining({ id: 'plan-phase-1-0', title: 'Select a public cohort' }),
      expect.objectContaining({ id: 'plan-phase-1-1', title: 'Compare cell populations' }),
      expect.objectContaining({ id: 'plan-phase-1-2', title: 'Validate marker signatures' }),
    ]);
    expect(detail.planSummary).toBe('Compare responder signatures');
    expect(detail.inputRows).toEqual([]);
    expect(detail.inputCollections).toEqual([]);
    expect(detail.showIdentity).toBe(false);
    expect(detail.showGenericOutput).toBe(false);
  });

  it('keeps detailed plan descriptions for a third disclosure level', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'generate_plan',
        input: JSON.stringify({
          task_summary: 'Analyze a public cohort',
          steps: [
            {
              title: 'Prepare the expression matrix',
              description:
                'Validate sample labels and preserve the normalized matrix for reproducible downstream analysis.',
            },
          ],
        }),
      }),
      'en-US',
      '1 step'
    );

    expect(detail.planDocument?.phases[0].steps).toEqual([
      expect.objectContaining({
        id: 'plan-phase-1-0',
        title: 'Prepare the expression matrix',
        description: 'Validate sample labels and preserve the normalized matrix for reproducible downstream analysis.',
      }),
    ]);
  });

  it('projects commands, stdout, file edits and artifact lists into auditable task details', () => {
    const command = buildToolPublicDetailPresentation(
      tool({
        name: 'bash',
        input: JSON.stringify({
          command: 'python analyze.py --dataset GSE120575',
          environment: 'scanpy',
        }),
        output: JSON.stringify({
          stdout: 'cells=16291\npatients=32\nstatus=complete',
        }),
      }),
      'en-US',
      '3 lines of output'
    );
    const edit = buildToolPublicDetailPresentation(
      tool({
        name: 'edit_file',
        input: JSON.stringify({
          file_path: 'out/summary_report.md',
          old_string: '',
          new_string: '# Study summary\n\nValidated cohort statistics.',
        }),
      }),
      'en-US',
      'Completed'
    );
    const save = buildToolPublicDetailPresentation(
      tool({
        name: 'save_artifacts',
        input: JSON.stringify({
          files: ['out/summary_report.md', 'out/signature_auc.png'],
          checkpoints: ['data/processed.h5ad'],
        }),
      }),
      'en-US',
      '2 artifacts'
    );

    expect(command.inputBlocks[0]).toMatchObject({
      label: 'Command',
      language: 'bash',
    });
    expect(command.toolLabel).toBe('BASH');
    expect(command.toolContext).toBe('ENV scanpy');
    expect(command.inputBlocks[0].content).toContain('analyze.py');
    expect(command.outputBlocks[0]).toMatchObject({
      label: 'STDOUT',
      language: 'console',
    });
    expect(command.outputBlocks[0].content).toContain('cells=16291');
    expect(edit.inputRows).toContainEqual({
      label: 'Task file',
      value: 'out/summary_report.md',
    });
    expect(edit.toolLabel).toBe('FILE');
    expect(edit.toolContext).toBe('out/summary_report.md');
    expect(edit.inputBlocks).toContainEqual(
      expect.objectContaining({
        label: 'Updated content',
        content: '# Study summary\n\nValidated cohort statistics.',
        variant: 'markdown',
      })
    );
    expect(save.inputCollections).toEqual([
      {
        label: 'Deliverable files',
        items: ['out/summary_report.md', 'out/signature_auc.png'],
        total: 2,
      },
      {
        label: 'Recoverable results',
        items: ['data/processed.h5ad'],
        total: 1,
      },
    ]);
  });

  it('keeps Windows commands in the analysis presentation instead of the generic fallback', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'powershell',
        input: JSON.stringify({
          command: 'Get-Content .\\results.csv | Select-Object -First 5',
        }),
        output: JSON.stringify({ stdout: 'compound,score\nA,-8.2' }),
      }),
      'en-US',
      '2 lines of output'
    );

    expect(detail.detailKind).toBe('analysis');
    expect(detail.toolLabel).toBe('POWERSHELL');
    expect(detail.inputBlocks).toContainEqual(expect.objectContaining({ label: 'Command', language: 'powershell' }));
  });

  it('projects safe dynamic scientific parameters without a tool-specific UI implementation', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'mcp__structures__dock_ligands',
        input: JSON.stringify({
          pdb_id: '9XAM',
          chain: 'A',
          exhaustiveness: 24,
          num_modes: 2,
          ligands: ['CC(=O)N1CCN(CC1)C2=NC=CC=C2', 'CN1CCC[C@H]1CO'],
          kernel_id: 'private-kernel',
          api_key: 'secret-value',
        }),
        output: JSON.stringify({
          pdb_id: '9XAM',
          selected_count: 2,
          score: -8.4,
        }),
      }),
      'zh-CN',
      '2 条结果'
    );

    expect(detail.detailKind).toBe('analysis');
    expect(detail.inputRows).toEqual(
      expect.arrayContaining([
        { label: 'PDB 编号', value: '9XAM' },
        { label: '蛋白链', value: 'A' },
        { label: '搜索强度', value: '24' },
        { label: '构象数量', value: '2' },
      ])
    );
    expect(detail.inputCollections).toContainEqual({
      label: '配体',
      items: ['CC(=O)N1CCN(CC1)C2=NC=CC=C2', 'CN1CCC[C@H]1CO'],
      total: 2,
    });
    expect(detail.resultRows).toEqual(
      expect.arrayContaining([
        { label: 'PDB 编号', value: '9XAM' },
        { label: '评分', value: '-8.4' },
      ])
    );
    expect(JSON.stringify(detail)).not.toMatch(/private-kernel|secret-value/u);
  });

  it('renders dynamic article payloads as readable evidence instead of raw serialization', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'mcp__pubmed__get_article_metadata',
        input: JSON.stringify({ pmids: ['35658005'] }),
        output: JSON.stringify({
          count: 1,
          articles: [
            {
              title: 'Adagrasib in Non-Small-Cell Lung Cancer Harboring a KRASG12C Mutation.',
              abstract:
                'A registrational cohort evaluated adagrasib in previously treated KRAS G12C-mutated non-small-cell lung cancer.',
              doi: '10.1056/NEJMoa2204619',
            },
          ],
        }),
      }),
      'zh-CN',
      '1 条结果'
    );

    expect(detail.resultCollections).toContainEqual({
      label: '文献',
      total: 1,
      items: [expect.stringContaining('Adagrasib in Non-Small-Cell Lung Cancer Harboring a KRASG12C Mutation.')],
    });
    expect(detail.resultCollections[0].items[0]).toContain('10.1056/NEJMoa2204619');
    expect(JSON.stringify(detail)).not.toContain('"articles"');
  });

  it('marks loaded task guidance as rich markdown without exposing its internal location', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'skill',
        input: JSON.stringify({ skill: 'single-cell-analysis' }),
        output: '# Single-cell analysis\n\n## Workflow\n\nValidate the cohort before clustering.',
      }),
      'en-US',
      'Ready'
    );

    expect(detail.toolLabel).toBe('GUIDE');
    expect(detail.toolContext).toBe('single-cell-analysis');
    expect(detail.inputBlocks).toContainEqual(
      expect.objectContaining({ label: 'Analysis guide', variant: 'markdown' })
    );
    expect(detail.outputBlocks).toEqual([]);
  });

  it('keeps task commands auditable without exposing the product skill installation path', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'bash',
        input: JSON.stringify({
          command:
            'python "./.synon/runtime/skills/medicinal-chemistry-optimization/hash/scripts/rdkit_analog_generator.py" --count 30',
        }),
      }),
      'en-US',
      'Completed'
    );

    expect(detail.inputBlocks[0].content).toContain('"./analysis-tools/rdkit_analog_generator.py"');
    expect(detail.inputBlocks[0].content).not.toContain('././analysis-tools');
    expect(detail.inputBlocks[0].content).toContain('--count 30');
    expect(detail.inputBlocks[0].content).not.toContain('.synon/runtime/skills');
  });

  it('keeps task diagnostics auditable while redacting host paths', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'repl',
        input: JSON.stringify({ domain: 'www.ncbi.nlm.nih.gov' }),
        output: JSON.stringify({
          stdout: [
            '正在下载 internal_data.txt.gz...',
            '下载失败: <urlopen error [Errno -3] Temporary failure in name resolution>',
            '下载完成，成功处理了 0/2 项',
            '/home/private/work/output.json',
          ].join('\n'),
          files_written: ['/home/private/work/output.json'],
        }),
      }),
      'zh-CN',
      '4 行输出'
    );

    expect(detail.inputRows).toContainEqual({
      label: '访问来源',
      value: 'www.ncbi.nlm.nih.gov',
    });
    expect(detail.resultRows).toContainEqual({
      label: '生成结果',
      value: '1 项',
    });
    expect(detail.narrative).toEqual([]);
    expect(detail.outputBlocks).toContainEqual(
      expect.objectContaining({
        label: 'STDOUT',
        content: expect.stringContaining('下载完成，成功处理了 0/2 项'),
      })
    );
    const rendered = JSON.stringify(detail);
    expect(rendered).toContain('internal_data.txt.gz');
    expect(rendered).toContain('Errno');
    expect(rendered).not.toContain('/home/private');
    expect(rendered).toContain('output.json');
  });

  it('keeps concise plain-text output available behind the public output disclosure', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'search_records',
        input: JSON.stringify({ query: 'STAT6' }),
        output: ['3 records found', 'STAT6 study cohort'].join('\n'),
      }),
      'en-US',
      '2 lines of output'
    );

    expect(detail.toolLabel).toBe('Search');
    expect(detail.resultSummary).toBe('2 lines of output');
    expect(detail.outputBlocks).toContainEqual(
      expect.objectContaining({
        label: 'Output',
        content: '3 records found\nSTAT6 study cohort',
      })
    );
  });

  it('projects structured scientific records as an expandable audit collection', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'mcp__structures-interactions__pdb_search_structures',
        input: JSON.stringify({ accession: 'P01116' }),
        output: JSON.stringify({
          total_count: 542,
          n_retrieved: 3,
          records: [
            { pdb_id: '12XE', score: 1 },
            { pdb_id: '12UW', score: 1 },
            { pdb_id: '9XAM', score: 1 },
          ],
        }),
      }),
      'zh-CN',
      '3 条结果'
    );

    expect(detail.resultRows).toContainEqual({
      label: '获取结果',
      value: '3 项',
    });
    expect(detail.resultCollections).toContainEqual({
      label: '记录',
      total: 3,
      items: ['PDB 编号: 12XE · 评分: 1', 'PDB 编号: 12UW · 评分: 1', 'PDB 编号: 9XAM · 评分: 1'],
    });
  });

  it('projects task records embedded in a calculation result without exposing runtime wrappers', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'repl',
        input: JSON.stringify({ background: false }),
        output: JSON.stringify({
          kernel_id: 'kernel-private',
          tool_use_id: 'tool-private',
          stdout: "{'n_retrieved': 2, 'records': [{'pdb_id': '12XE', 'score': 1.0}, {'pdb_id': '9XAM', 'score': 1.0}]}",
        }),
      }),
      'zh-CN',
      '2 条结果'
    );

    expect(detail.resultRows).toContainEqual({
      label: '获取结果',
      value: '2 项',
    });
    expect(detail.resultCollections).toContainEqual({
      label: '记录',
      total: 2,
      items: ['PDB 编号: 12XE · 评分: 1', 'PDB 编号: 9XAM · 评分: 1'],
    });
    expect(JSON.stringify(detail)).not.toContain('kernel-private');
    expect(JSON.stringify(detail)).not.toContain('tool-private');
  });

  it('projects compute discovery and provider execution as a dedicated compute detail', () => {
    const listed = buildToolPublicDetailPresentation(
      tool({
        name: 'list_compute',
        input: JSON.stringify({}),
        output: JSON.stringify({
          providers: [
            { name: 'gpu-lab', family: 'ssh', capabilities: ['durable_jobs'] },
            { name: 'managed-inference', family: 'infer', state: 'ready' },
          ],
        }),
      }),
      'en-US',
      '2 results'
    );
    const executed = buildToolPublicDetailPresentation(
      tool({
        name: 'compute_provider',
        input: JSON.stringify({
          provider: 'managed-inference',
          code: 'print(model.status())',
        }),
        output: JSON.stringify({ stdout: 'ready\n' }),
      }),
      'en-US',
      '1 line of output'
    );

    expect(listed.detailKind).toBe('compute');
    expect(listed.toolLabel).toBe('COMPUTE');
    expect(listed.resultCollections).toContainEqual({
      label: 'Available compute',
      total: 2,
      items: ['Name: gpu-lab · Family: ssh', 'Name: managed-inference · Family: infer · State: ready'],
    });
    expect(executed.detailKind).toBe('compute');
    expect(executed.toolContext).toBe('managed-inference');
    expect(executed.inputBlocks).toContainEqual(
      expect.objectContaining({
        label: 'Analysis code',
        language: 'python',
        content: 'print(model.status())',
      })
    );
  });

  it('shows the purpose and outcome of access operations without exposing host identities', () => {
    const access = buildToolPublicDetailPresentation(
      tool({
        name: 'request_network_access',
        input: JSON.stringify({
          domain: 'ftp.ncbi.nlm.nih.gov',
          reason: 'Download the selected public cohort',
        }),
        output: JSON.stringify({
          granted: true,
          domain: 'ftp.ncbi.nlm.nih.gov',
        }),
      }),
      'en-US',
      'Allowed'
    );
    const deletion = buildToolPublicDetailPresentation(
      tool({
        name: 'delete_host_files',
        input: JSON.stringify({
          paths: ['/home/private/run/obsolete.csv', '/home/private/run/duplicate.tsv'],
          reason: 'Remove superseded task exports',
        }),
        output: JSON.stringify({
          trashed: ['/home/private/run/obsolete.csv', '/home/private/run/duplicate.tsv'],
          recoverable: true,
        }),
      }),
      'en-US',
      '2 results'
    );

    expect(access.detailKind).toBe('access');
    expect(access.inputRows).toEqual([
      { label: 'Access source', value: 'ftp.ncbi.nlm.nih.gov' },
      { label: 'Purpose', value: 'Download the selected public cohort' },
    ]);
    expect(access.resultRows).toContainEqual({
      label: 'Access',
      value: 'Allowed',
    });
    expect(deletion.detailKind).toBe('file');
    expect(deletion.inputCollections).toContainEqual({
      label: 'Requested locations',
      items: ['obsolete.csv', 'duplicate.tsv'],
      total: 2,
    });
    expect(deletion.resultCollections).toContainEqual({
      label: 'Moved to Trash',
      items: ['obsolete.csv', 'duplicate.tsv'],
      total: 2,
    });
    expect(JSON.stringify(deletion)).not.toContain('/home/private');
  });

  it('keeps project memory auditable while removing record and project identities', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'search_memory',
        input: JSON.stringify({
          query: 'validated assay preference',
          entity: 'project:proj-private-12345',
        }),
        output: JSON.stringify({
          output: '[2 days ago] Use orthogonal assay confirmation [mem_private_123 · project:proj-private-12345]',
          results_returned: 1,
        }),
      }),
      'en-US',
      '1 result'
    );

    expect(detail.detailKind).toBe('memory');
    expect(detail.inputRows).toContainEqual({
      label: 'Query',
      value: 'validated assay preference',
    });
    expect(detail.inputRows).toContainEqual({
      label: 'Memory scope',
      value: 'Current project',
    });
    expect(detail.resultRows).toContainEqual({
      label: 'Matches',
      value: '1 item',
    });
    expect(detail.outputBlocks).toContainEqual(
      expect.objectContaining({
        label: 'Memory records',
        content: '[2 days ago] Use orthogonal assay confirmation [[memory record] · project]',
      })
    );
    expect(JSON.stringify(detail)).not.toContain('proj-private');
    expect(JSON.stringify(detail)).not.toContain('mem_private');
  });

  it('keeps retrieval metadata and evidence distinct from search-result cards', () => {
    const detail = buildToolPublicDetailPresentation(
      tool({
        name: 'web_fetch',
        input: JSON.stringify({
          url: 'https://example.org/article?id=42&token=private',
        }),
        output: JSON.stringify({
          content: 'A bounded public abstract.',
          returnedResults: 1,
          source_url: 'https://example.org/article?id=42&signature=private-output',
        }),
      }),
      'en-US',
      '1 result'
    );

    expect(detail.detailKind).toBe('retrieval');
    expect(detail.inputRows).toContainEqual({
      label: 'Source',
      value: 'https://example.org/article?id=42&token=%5Bredacted%5D',
    });
    expect(detail.outputBlocks).toContainEqual(
      expect.objectContaining({
        label: 'Detailed result',
        content: 'A bounded public abstract.',
      })
    );
    expect(detail.resultRows).toContainEqual({
      label: 'Source URL',
      value: 'https://example.org/article?id=42&signature=%5Bredacted%5D',
    });
    expect(JSON.stringify(detail)).not.toContain('private');
  });

  it('keeps every distinct record and every nested public field accessible', () => {
    const records = Array.from({ length: 105 }, (_, index) => ({
      id: `REC-${String(index + 1).padStart(3, '0')}`,
      title: 'Shared scientific title',
      measurements: {
        primary: { value: index + 0.5, unit: 'nM' },
        flags: ['validated', `batch-${index + 1}`],
      },
    }));
    const detail = buildToolPublicDetailPresentation(
      tool({ name: 'analyze_records', output: JSON.stringify({ records }) }),
      'en-US',
      '105 results'
    );

    expect(detail.resultCollections).toContainEqual(
      expect.objectContaining({
        total: 105,
        items: expect.arrayContaining([expect.stringContaining('REC-001'), expect.stringContaining('REC-105')]),
      })
    );
    const recordsNode =
      detail.outputTree?.kind === 'object'
        ? detail.outputTree.children.find((child) => child.path === '$/records')
        : null;
    expect(recordsNode).toMatchObject({ kind: 'array', total: 105 });
    if (!recordsNode || recordsNode.kind !== 'array') throw new Error('records detail tree missing');
    expect(recordsNode.children).toHaveLength(105);
    expect(JSON.stringify(recordsNode.children[104])).toContain('batch-105');
    expect(JSON.stringify(recordsNode.children[104])).toContain('104.5');
  });
});
