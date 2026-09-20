import type { NormalizedToolCall } from '@/common/chat/normalizeToolCall';
import { isActiveToolStatus } from '@/common/chat/normalizeToolCall';
import {
  getToolActivityKind,
  getToolPublicDetailKind,
  toolActivityGroupPhrase,
  toolActivityLabel,
  type ToolActivityKind,
} from '../toolActivityPresentationRegistry';
import { extractResearchSourcePresentation, isResearchActivityTool } from './researchSourcePresentation';
import { buildToolFailurePresentation } from './toolFailurePresentation';
import { buildToolProgressPublicPresentation, hasDeterminateByteTransfer } from './toolProgressPresentation';
import { toolOperationAction, toolOperationSubject } from './toolOperationSubject';
import { retrievalReceiptPresentation } from '../toolDetails/retrievalReceiptPresentation';
import { toolExecutionDisposition, toolExecutionRecoveryFamily, toolWasNotExecuted } from './toolExecutionDisposition';

const INTERNAL_PUBLIC_TEXT =
  /\b(?:artifact(?:[_-]?(?:id|ref))?|backend|bash|checkpoint|codebase|frontend|harness|ledger|mcp|projector|prompt constraint|receipt|repository|retry controller|runtime policy|schema|shell|skill|source code|stderr|stdout|system prompt|tool(?:\s+call)?|validation[_-]?json|version[_-]?id|workdir|workspace)\b|技能|工具调用|工作目录|内部(?:文件|状态|策略)|验证账本|运行时策略|重试控制|加载(?:能力|技能)|提示词|系统提示|编排策略|投影(?:链路|状态)?|事件流|前端|后端|代码仓库|源代码|文件路径|(?:原始|返回)(?:结构|响应|输出|结果)|调试(?:信息|结果)?/iu;
const COMMAND_OR_PATH_PUBLIC_TEXT =
  /(?:^|\s)(?:--?[a-z][\w-]*|[a-z][\w-]*\s+-[a-z])|(?:^|\s)(?:\/[\w.-]+){2,}|[A-Za-z]:\\|\\\\wsl|[`{}]|\[|\]|\b[a-z][a-z0-9]*_[a-z0-9_]+\b|\b[\w.-]+\.(?:json|py|sh|yaml|yml|toml)\b/iu;
const CONSTRUCTION_PUBLIC_TEXT =
  /(?:打印|只打印|转储|输出)(?:完整(?:的)?|全部|原始|当前)?[^。；]*(?:结果|结构|键|keys?|字段|类型|响应|信息)|\b(?:print|dump|raw response|raw result|debug output)\b/iu;

export function isPublicNarrativeSafe(value: string): boolean {
  const normalized = value.trim();
  return (
    normalized.length > 0 &&
    normalized.length <= 256 &&
    !INTERNAL_PUBLIC_TEXT.test(normalized) &&
    !COMMAND_OR_PATH_PUBLIC_TEXT.test(normalized) &&
    !CONSTRUCTION_PUBLIC_TEXT.test(normalized)
  );
}

export interface ToolStepPublicPresentation {
  label: string;
  detail: string | null;
  resultSummary: string | null;
  expandable: boolean;
  showResearchSources: boolean;
  shouldLoadFull: boolean;
  failureSummary: string | null;
}

export function buildToolStepPublicPresentation(
  tool: NormalizedToolCall,
  language: string
): ToolStepPublicPresentation {
  const chinese = language.toLowerCase().startsWith('zh');
  // Search owns one dedicated public detail surface even when a provider
  // returns no usable links. Falling through to the generic structured-output
  // renderer would expose transport/backend diagnostics instead of the query
  // and a truthful empty result.
  const showResearchSources = isResearchActivityTool(tool.name);
  const failureSummary =
    tool.status === 'error' && !toolWasNotExecuted(tool) ? buildPublicFailureSummary(tool, chinese) : null;
  const shouldLoadFull = tool.truncated === true;
  return {
    label: buildToolStepLabel(tool, language),
    detail: buildToolStepDetail(tool, chinese),
    resultSummary: buildToolStepResultSummary(tool, language),
    expandable: true,
    showResearchSources,
    shouldLoadFull,
    failureSummary,
  };
}

export function buildToolStepGroupSummary(
  tools: NormalizedToolCall[],
  language: string
): { headline: string; meta: string; subject?: string } {
  const chinese = language.toLowerCase().startsWith('zh');
  const notExecutedTools = tools.filter(toolWasNotExecuted);
  const recoveryChecks = notExecutedTools.filter((tool) => toolExecutionRecoveryFamily(tool) !== null);
  const otherNotExecuted = notExecutedTools.length - recoveryChecks.length;
  const executedTools = tools.filter((tool) => !toolWasNotExecuted(tool));
  const counts = new Map<ToolActivityKind, number>();
  for (const tool of executedTools) {
    const kind = getToolActivityKind(tool.name);
    counts.set(kind, (counts.get(kind) ?? 0) + 1);
  }
  const reserveForExecutionReview = recoveryChecks.length > 0 || otherNotExecuted > 0 ? 1 : 0;
  const activityPhrases = [...counts.entries()]
    .toSorted((left, right) => right[1] - left[1])
    .slice(0, 3 - reserveForExecutionReview)
    .map(([kind, count]) => toolActivityGroupPhrase(kind, count, chinese));
  const hasMoreActivityKinds = counts.size > activityPhrases.length;
  const phrases = [...activityPhrases];
  if (recoveryChecks.length > 0) {
    phrases.push(
      chinese
        ? `检查了 ${recoveryChecks.length} 次执行尝试`
        : `Reviewed ${recoveryChecks.length} execution ${recoveryChecks.length === 1 ? 'attempt' : 'attempts'}`
    );
  }
  if (otherNotExecuted > 0) {
    phrases.push(
      chinese
        ? `${otherNotExecuted} 项操作未执行`
        : `${otherNotExecuted} ${otherNotExecuted === 1 ? 'operation was' : 'operations were'} not executed`
    );
  }

  const failed = executedTools.filter((tool) => tool.status === 'error').length;
  const stepLabel = executedTools.length
    ? chinese
      ? `${executedTools.length} 步`
      : `${executedTools.length} ${executedTools.length === 1 ? 'step' : 'steps'}`
    : '';
  const notExecutedLabel = notExecutedTools.length
    ? chinese
      ? `${notExecutedTools.length} 项未执行`
      : `${notExecutedTools.length} not executed`
    : '';
  const failureLabel = failed ? (chinese ? `${failed} 项未成功` : `${failed} failed`) : '';
  const activeDescription = tools.findLast(
    (tool) => isActiveToolStatus(tool.status) && Boolean(localizedHumanDescription(tool, chinese))
  );
  const subjects = [
    ...new Set(
      tools.map((tool) => toolOperationSubject(tool, chinese, false)).filter((value): value is string => !!value)
    ),
  ];
  return {
    ...(subjects.length
      ? {
          subject: truncate(
            subjects.slice(0, 2).join(' · ') + (subjects.length > 2 ? (chinese ? ' 等' : ', …') : ''),
            88
          ),
        }
      : {}),
    headline:
      (activeDescription && localizedHumanDescription(activeDescription, chinese)) ??
      `${phrases.join(chinese ? '、' : ', ')}${hasMoreActivityKinds ? (chinese ? '等' : ', and more') : ''}`,
    meta: [stepLabel, notExecutedLabel, failureLabel].filter(Boolean).join(' · '),
  };
}

export function buildToolStepResultSummary(tool: NormalizedToolCall, language: string): string | null {
  const chinese = language.toLowerCase().startsWith('zh');
  switch (toolExecutionDisposition(tool)) {
    case 'not-executed':
      return chinese ? '未执行' : 'Not executed';
    case 'preflight-passed':
      return chinese ? '预检通过' : 'Preflight passed';
    case 'preflight-blocked':
      return chinese ? '预检受限' : 'Preflight blocked';
  }
  if (tool.status === 'error') return chinese ? '未完成' : 'Failed';
  if (tool.status === 'waiting') return chinese ? '等待中' : 'Waiting';
  if (tool.status === 'blocked') return chinese ? '等待外部条件' : 'Blocked';
  if (tool.status === 'interrupted') return chinese ? '执行已中断' : 'Interrupted';
  if (tool.status === 'unknown') return chinese ? '状态待确认' : 'Status unknown';
  if (tool.status === 'running' || tool.status === 'pending') {
    if (tool.progress) {
      const progress = buildToolProgressPublicPresentation(tool.progress, language);
      if (progress.compactResult) return progress.compactResult;
    }
    return chinese ? '进行中' : 'Running';
  }
  if (tool.status === 'canceled') return chinese ? '已停止' : 'Stopped';
  const outputValue = parseStructuredValue(tool.output);
  const activityKind = getToolActivityKind(tool.name);
  if (activityKind === 'fetch') {
    const receipt = retrievalReceiptPresentation(parseStructuredRecord(tool.input), outputValue, chinese);
    if (receipt.summary) return receipt.summary;
  }
  if (['environment', 'compute', 'analysis'].includes(activityKind) && isRecord(outputValue)) {
    const receipt = outputValue as Record<string, unknown>;
    if (receipt.status === 'running' && (receipt.operation_id || receipt.notification_id)) {
      return chinese ? '后台执行中' : 'Running in background';
    }
    if (activityKind === 'environment' && receipt.mode === 'reuse') {
      return chinese ? '复用已有环境' : 'Reused existing environment';
    }
    if (activityKind === 'environment' && Array.isArray(receipt.environments)) {
      const count = (receipt.environments as unknown[]).length;
      return chinese ? `${count} 个环境` : `${count} environments`;
    }
  }

  if (isResearchActivityTool(tool.name)) {
    // The compact history contract carries the aggregate result count outside
    // the byte-truncated output. Prefer it to provider-level counters embedded
    // earlier in the preview, which may truthfully be zero even though another
    // provider supplied the final aggregate results.
    const compactResearchCount = tool.truncated
      ? (tool.compactResultCount ??
        numericFieldFromText(tool.output, ['n_retrieved', 'n_returned', 'returnedResults', 'retrieved']))
      : null;
    if (compactResearchCount !== null) {
      return chinese
        ? `${compactResearchCount} 条结果`
        : `${compactResearchCount} ${compactResearchCount === 1 ? 'result' : 'results'}`;
    }
    const researchResultCount = extractResearchSourcePresentation(tool.input, tool.output).results.length;
    if (researchResultCount > 0) {
      return chinese
        ? `${researchResultCount} 条结果`
        : `${researchResultCount} ${researchResultCount === 1 ? 'result' : 'results'}`;
    }
    const normalizedName = tool.name.trim().toLowerCase();
    const nativeWebSearch = ['web_search', 'websearch', 'web_research', 'webresearch'].includes(normalizedName);
    if (!nativeWebSearch) {
      // MCP/database searches often return structured scientific records rather
      // than public URLs. Count the records actually returned by that tool,
      // including JSON text nested in a generic MCP result envelope. Do not use
      // api_total/total here: those describe the upstream corpus, not the
      // current page visible to the user.
      const explicitReturnedCount = firstNumericField(outputValue, [
        'n_records_returned',
        'rows_retrieved',
        'n_retrieved',
        'n_returned',
        'results_returned',
        'returnedResults',
        'retrieved',
      ]);
      const structuredRecordCount = countNamedCollection(outputValue, [
        'records',
        'results',
        'items',
        'matches',
        'articles',
        'works',
        'studies',
        'trials',
        'compounds',
        'targets',
        'entries',
        'preprints',
        'variants',
        'datasets',
      ]);
      const returnedCount = explicitReturnedCount ?? structuredRecordCount;
      if (returnedCount !== null) {
        return chinese ? `${returnedCount} 条结果` : `${returnedCount} ${returnedCount === 1 ? 'result' : 'results'}`;
      }
    }
    // A compact history preview may retain the authoritative retrieval count
    // before its result records are hydrated. Trust only explicit retrieved /
    // returned counters in that truncated envelope, never a generic transport
    // "results" counter that may describe provider attempts.
    if (tool.truncated) return chinese ? '检索详情待加载' : 'Search details pending';
    return chinese ? '未发现可用来源' : 'No usable sources';
  }

  const input = parseStructuredRecord(tool.input);
  const normalizedName = tool.name.toLowerCase();

  if (normalizedName === 'generate_plan') {
    const count = countPlanSteps(input);
    if (count > 0) return chinese ? `${count} 个步骤` : `${count} ${count === 1 ? 'step' : 'steps'}`;
  }

  if (normalizedName === 'save_artifacts' || normalizedName === 'list_artifacts') {
    const count = countNamedCollection(outputValue, ['artifacts', 'saved', 'results']);
    if (count !== null) return chinese ? `${count} 个结果` : `${count} ${count === 1 ? 'result' : 'results'}`;
  }

  if (normalizedName === 'skill' || normalizedName === 'load_skill') return chinese ? '已准备' : 'Ready';
  if (normalizedName === 'search_skills') {
    const count = countCollection(outputValue);
    if (count !== null) return chinese ? `${count} 项可用方法` : `${count} available`;
  }

  if (getToolPublicDetailKind(tool.name) === 'access' && booleanField(outputValue, 'granted') === true) {
    return chinese ? '已允许' : 'Allowed';
  }

  const resultCount = countNamedCollection(outputValue, [
    'results',
    'records',
    'items',
    'providers',
    'jobs',
    'grants',
    'memories',
    'matches',
  ]);
  const retrievedCount = numericField(outputValue, 'retrieved');
  const returnedCount = numericField(outputValue, 'returnedResults');
  const memoryMatchCount = numericField(outputValue, 'results_returned');
  const publicResultCount = retrievedCount ?? returnedCount ?? memoryMatchCount ?? resultCount;
  if (publicResultCount !== null) {
    return chinese
      ? `${publicResultCount} 条结果`
      : `${publicResultCount} ${publicResultCount === 1 ? 'result' : 'results'}`;
  }

  const compactResultCount = numericFieldFromText(tool.output, [
    'n_retrieved',
    'n_returned',
    'returnedResults',
    'retrieved',
    'selected_count',
  ]);
  if (compactResultCount !== null) {
    return chinese
      ? `${compactResultCount} 条结果`
      : `${compactResultCount} ${compactResultCount === 1 ? 'result' : 'results'}`;
  }

  const lineCount = countOutputLines(tool.output, outputValue);
  if (lineCount > 0) {
    return chinese ? `${lineCount} 行输出` : `${lineCount} ${lineCount === 1 ? 'line' : 'lines'} of output`;
  }
  return chinese ? '已完成' : 'Completed';
}

export function buildToolStepLabel(tool: NormalizedToolCall, language: string): string {
  const chinese = language.toLowerCase().startsWith('zh');
  if (toolExecutionRecoveryFamily(tool)) return chinese ? '检查执行尝试' : 'Review execution plan';
  return (
    localizedHumanDescription(tool, chinese) ??
    toolOperationAction(tool, chinese) ??
    toolActivityLabel(getToolActivityKind(tool.name), chinese)
  );
}

function localizedHumanDescription(tool: NormalizedToolCall, chinese: boolean): string | undefined {
  const description = tool.humanDescription?.trim();
  if (!description || !isPublicNarrativeSafe(description)) return undefined;
  const hasCJK = /[\u3400-\u9fff]/u.test(description);
  return hasCJK === chinese ? description : undefined;
}

function buildToolStepDetail(tool: NormalizedToolCall, chinese: boolean): string | null {
  const subject = toolOperationSubject(tool, chinese, !localizedHumanDescription(tool, chinese));
  // A completed transfer keeps its last observed byte snapshot so the history
  // row remains auditable after the live heartbeat stops. Error/cancelled rows
  // intentionally keep their failure state instead of showing stale progress.
  if (
    tool.progress &&
    (isActiveToolStatus(tool.status) || (tool.status === 'completed' && hasDeterminateByteTransfer(tool.progress)))
  ) {
    const progress = buildToolProgressPublicPresentation(tool.progress, chinese ? 'zh-CN' : 'en-US').compactDetail;
    return [subject && truncate(subject, 48), progress].filter(Boolean).join(' · ');
  }
  if (getToolPublicDetailKind(tool.name) === 'plan' && localizedHumanDescription(tool, chinese)) return null;
  return subject ? truncate(subject, 88) : null;
}

function buildPublicFailureSummary(tool: NormalizedToolCall, chinese: boolean): string {
  const presentation = buildToolFailurePresentation(tool.output);
  if (presentation.httpStatus === 401 || presentation.httpStatus === 403) {
    return chinese ? '当前访问权限不足，请调整授权后重试' : 'Access was not available; update permission and retry.';
  }
  if (presentation.httpStatus === 404) {
    return chinese
      ? '所需内容暂时无法访问，任务可改用其他来源'
      : 'The requested content was unavailable; another source can be used.';
  }
  if (presentation.httpStatus === 408 || presentation.httpStatus === 429 || (presentation.httpStatus ?? 0) >= 500) {
    return chinese
      ? '外部服务暂时不可用，任务可稍后重试或改用其他方案'
      : 'The external service was unavailable; the task can retry or use another approach.';
  }
  return chinese
    ? '此步骤未完成；任务会根据现有结果决定重试或改用其他方案'
    : 'This step did not complete; the task can retry or use another approach.';
}

function parseStructuredValue(value: string | undefined): unknown | null {
  if (!value) return null;
  try {
    return JSON.parse(value);
  } catch {
    return null;
  }
}

function parseStructuredRecord(value: string | undefined): Record<string, unknown> | null {
  const parsed = parseStructuredValue(value);
  return isRecord(parsed) ? parsed : null;
}

function countPlanSteps(input: Record<string, unknown> | null): number {
  if (!input) return 0;
  return countPlanNode(isRecord(input.plan) ? input.plan : input);
}

function countPlanNode(node: Record<string, unknown>): number {
  const directSteps = Array.isArray(node.steps) ? node.steps.filter(isRecord).length : 0;
  const nestedSteps = ['phases', 'delegations', 'stages', 'tasks']
    .flatMap((key) => (Array.isArray(node[key]) ? node[key].filter(isRecord) : []))
    .reduce((total, child) => total + countPlanNode(child), 0);
  return directSteps + nestedSteps;
}

function countNamedCollection(value: unknown, keys: string[], depth = 0): number | null {
  const decoded = decodeNestedStructuredValue(value, depth);
  if (decoded !== value) return countNamedCollection(decoded, keys, depth + 1);
  if (Array.isArray(value)) return value.length;
  if (!isRecord(value) || depth > 4) return null;
  for (const key of keys) {
    if (Array.isArray(value[key])) return value[key].length;
  }
  for (const key of ['result', 'data']) {
    const nested = countNamedCollection(value[key], keys, depth + 1);
    if (nested !== null) return nested;
  }
  return null;
}

function countCollection(value: unknown): number | null {
  return Array.isArray(value) ? value.length : null;
}

function numericField(value: unknown, key: string, depth = 0): number | null {
  const decoded = decodeNestedStructuredValue(value, depth);
  if (decoded !== value) return numericField(decoded, key, depth + 1);
  if (!isRecord(value) || depth > 5) return null;
  const candidate = value[key];
  if (typeof candidate === 'number' && Number.isFinite(candidate) && candidate >= 0) return Math.trunc(candidate);
  for (const nestedKey of ['result', 'data', 'diagnostics']) {
    const nested = numericField(value[nestedKey], key, depth + 1);
    if (nested !== null) return nested;
  }
  return null;
}

function firstNumericField(value: unknown, keys: string[]): number | null {
  for (const key of keys) {
    const count = numericField(value, key);
    if (count !== null) return count;
  }
  return null;
}

function decodeNestedStructuredValue(value: unknown, depth: number): unknown {
  if (typeof value !== 'string' || depth > 5) return value;
  const normalized = value.trim();
  if (!normalized.startsWith('{') && !normalized.startsWith('[')) return value;
  try {
    return JSON.parse(normalized);
  } catch {
    return value;
  }
}

function booleanField(value: unknown, key: string, depth = 0): boolean | null {
  if (!isRecord(value) || depth > 5) return null;
  const candidate = value[key];
  if (typeof candidate === 'boolean') return candidate;
  for (const nestedKey of ['result', 'data', 'diagnostics']) {
    const nested = booleanField(value[nestedKey], key, depth + 1);
    if (nested !== null) return nested;
  }
  return null;
}

function numericFieldFromText(value: string | undefined, keys: string[]): number | null {
  if (!value) return null;
  for (const key of keys) {
    const match = value.match(new RegExp(`["']?${key}["']?\\s*[:=]\\s*(\\d+)`, 'u'));
    if (match) return Number.parseInt(match[1], 10);
  }
  return null;
}

function countOutputLines(output: string | undefined, structured: unknown): number {
  const nested = isRecord(structured)
    ? ['stdout', 'content', 'preview', 'output']
        .map((key) => structured[key])
        .find((value): value is string => typeof value === 'string' && value.trim().length > 0)
    : undefined;
  const candidate = nested ?? (!isRecord(structured) ? output : undefined);
  if (!candidate?.trim()) return 0;
  return candidate.replace(/\r/g, '').trimEnd().split('\n').length;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value));
}

function truncate(value: string, maxCharacters: number): string {
  const characters = Array.from(value);
  return characters.length <= maxCharacters ? value : `${characters.slice(0, maxCharacters - 1).join('')}…`;
}
