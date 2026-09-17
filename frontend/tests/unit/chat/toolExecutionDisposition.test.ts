import { describe, expect, it } from 'vitest';
import type { NormalizedToolCall } from '@/common/chat/normalizeToolCall';
import { toolExecutionDisposition } from '@/renderer/pages/conversation/Messages/components/toolExecutionDisposition';
import { buildToolStepResultSummary } from '@/renderer/pages/conversation/Messages/components/toolStepSummaryModel';

const call = (output: unknown, status: NormalizedToolCall['status'] = 'completed'): NormalizedToolCall => ({
  key: 'operation',
  name: 'manage_environments',
  status,
  output: JSON.stringify(output),
});

describe('execution disposition from typed tool results', () => {
  it('does not report an unexecuted decision as completed in either language', () => {
    const tool = call({
      ok: true,
      executed: false,
      decision_required: true,
      status: 'implementation_selection_required',
    });
    expect(toolExecutionDisposition(tool)).toBe('not-executed');
    expect(buildToolStepResultSummary(tool, 'zh-CN')).toBe('未执行');
    expect(buildToolStepResultSummary(tool, 'en-US')).toBe('Not executed');
  });
  it('distinguishes a successful preflight from execution and preserves outer failures', () => {
    const result = {
      ok: true,
      mode: 'preflight',
      executed: false,
      feasible: true,
    };
    expect(buildToolStepResultSummary(call(result), 'zh-CN')).toBe('预检通过');
    expect(buildToolStepResultSummary(call({ ok: true, result }), 'en-US')).toBe('Preflight passed');
    expect(toolExecutionDisposition(call({ ok: false, result }))).toBe('not-executed');
    expect(toolExecutionDisposition(call({ ...result, feasible: false }))).toBe('preflight-blocked');
  });
  it('does not infer lifecycle state from scientific rows or prose', () => {
    expect(toolExecutionDisposition(call({ records: [{ executed: false, feasible: false }] }))).toBeNull();
    expect(toolExecutionDisposition(call('executed: false'))).toBeNull();
    expect(toolExecutionDisposition({ ...call(null), output: '{"executed":false' })).toBeNull();
  });
  it('recognizes package preflight receipts without an executed field', () => {
    const result = { ok: true, mode: 'preflight', feasible: true };
    expect(toolExecutionDisposition(call(result))).toBe('preflight-passed');
    expect(toolExecutionDisposition(call({ result }))).toBe('preflight-passed');
    expect(toolExecutionDisposition(call({ ...result, feasible: false }))).toBe('preflight-blocked');
    expect(toolExecutionDisposition(call({ ok: false, result }))).toBeNull();
    expect(toolExecutionDisposition(call({ ...result, executed: true }))).toBeNull();
    expect(toolExecutionDisposition(call({ mode: 'preflight', feasible: true }))).toBeNull();
  });
  it('retains active and canceled precedence and distinguishes non-execution from failure', () => {
    const result = { executed: false, decision_required: true };
    expect(buildToolStepResultSummary(call(result, 'running'), 'en-US')).toBe('Running');
    expect(buildToolStepResultSummary(call(result, 'error'), 'en-US')).toBe('Not executed');
    expect(buildToolStepResultSummary(call({ executed: true }, 'error'), 'en-US')).toBe('Failed');
    expect(buildToolStepResultSummary(call(result, 'canceled'), 'en-US')).toBe('Stopped');
  });
});
