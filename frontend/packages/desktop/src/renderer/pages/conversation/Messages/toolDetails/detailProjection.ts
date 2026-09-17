import type { NormalizedToolCall } from '@/common/chat/normalizeToolCall';
import { toolPublicDetailText, type ToolPublicDetailTextKey } from '@/renderer/services/i18n/toolPublicDetailLocale';
import { getToolActivityKind, getToolPublicPresentationPolicy } from '../toolActivityPresentationRegistry';
import { collectEmbeddedTaskStructures } from '../components/publicTaskOutputParser';
import {
  buildToolPublicDetailBlocks,
  formatPublicTaskPath,
  sanitizePublicTaskText,
} from '../components/toolPublicDetailBlocks';
import { isPublicNarrativeSafe } from '../components/toolStepSummaryModel';
import { buildToolProgressPublicPresentation } from '../components/toolProgressPresentation';
import {
  dedupeCollections,
  dedupeRows,
  isRecord,
  parseStructured,
  publicEnvironmentContext,
  publicMemoryScopeLabel,
  safeShortContext,
  truncate,
} from './detailFormatting';
import { buildToolDetailInputProjection } from './detailInputProjection';
import {
  buildToolDetailOutputProjection,
  collectPlainSafeHighlights,
  normalizeToolDetailResultRows,
} from './detailOutputProjection';
import type { ToolPublicDetailPresentation, ToolPublicPlanAssessment } from './detailTypes';
import { projectToolDetailValue } from './detailValueProjection';
import { buildToolPlanDocument } from './detailPlanProjection';
import { retrievalReceiptPresentation } from './retrievalReceiptPresentation';

export type {
  ToolDetailValueNode,
  ToolPublicDetailCollection,
  ToolPublicDetailPresentation,
  ToolPublicDetailRow,
  ToolPublicPlanAssessment,
} from './detailTypes';

export function buildToolPublicDetailPresentation(
  tool: NormalizedToolCall,
  language: string,
  resultSummary: string | null
): ToolPublicDetailPresentation {
  const chinese = language.toLowerCase().startsWith('zh');
  const policy = getToolPublicPresentationPolicy(tool.name);
  const detailKind = policy.detailKind;
  const input = parseStructured(tool.input);
  const output = parseStructured(tool.output);
  const outputSources = output ? [output, ...collectEmbeddedTaskStructures(output)] : [];
  const inputProjection = input
    ? buildToolDetailInputProjection(input, chinese, detailKind, tool.name.toLowerCase() === 'web_fetch')
    : { rows: [], collections: [] };

  const detailBlocks = buildToolPublicDetailBlocks(tool, input, output, chinese);
  const inputBlocks =
    detailKind === 'method' ? [...detailBlocks.inputBlocks, ...detailBlocks.outputBlocks] : detailBlocks.inputBlocks;
  const outputBlocks = detailKind === 'method' ? [] : detailBlocks.outputBlocks;
  const outputProjection = buildToolDetailOutputProjection(outputSources, detailKind, chinese, outputBlocks.length > 0);
  const progressRows = tool.progress ? buildToolProgressPublicPresentation(tool.progress, language).rows : [];
  const resultCollections = dedupeCollections(outputProjection.collections);
  const resultRows = normalizeToolDetailResultRows(
    [...progressRows, ...outputProjection.rows],
    resultCollections,
    detailKind,
    chinese
  );
  const narrative = [...outputProjection.narrative];
  if (
    narrative.length === 0 &&
    outputSources.length === 0 &&
    detailBlocks.outputBlocks.length === 0 &&
    tool.status === 'completed'
  ) {
    narrative.push(...collectPlainSafeHighlights(tool.output));
  }

  const treeLabel = toolPublicDetailText(chinese, 'collectionDetails');
  return {
    detailKind,
    toolLabel: publicToolKindLabel(tool, chinese),
    toolContext: publicToolContext(tool, input, chinese),
    resultSummary,
    planSummary: detailKind === 'plan' && input ? buildPlanSummary(input) : null,
    inputRows: dedupeRows(inputProjection.rows),
    inputCollections: dedupeCollections(inputProjection.collections),
    resultRows,
    resultCollections,
    inputBlocks,
    outputBlocks,
    inputTree: input
      ? projectToolDetailValue(input, {
          chinese,
          detailKind,
          label: treeLabel,
          source: 'input',
          byteLimit: tool.name.toLowerCase() === 'web_fetch',
        })
      : null,
    outputTree: output
      ? projectToolDetailValue(output, { chinese, detailKind, label: treeLabel, source: 'output' })
      : null,
    narrative: [...new Set(narrative)],
    notices: detailKind === 'retrieval' ? retrievalReceiptPresentation(input, output, chinese).notices : [],
    planDocument: detailKind === 'plan' && input ? buildToolPlanDocument(input, chinese) : null,
    planAssessment: detailKind === 'plan' && input ? buildPlanAssessment(input, chinese) : null,
    showIdentity: policy.showIdentity,
    showGenericOutput: policy.showGenericOutput,
    inputBlockMode: policy.inputBlockMode,
    collectionsInitiallyExpanded: policy.collectionsInitiallyExpanded,
  };
}

function buildPlanSummary(input: Record<string, unknown>): string | null {
  const candidate = typeof input.task_summary === 'string' ? input.task_summary.trim() : '';
  if (!candidate || !isPublicNarrativeSafe(candidate)) return null;
  return truncate(sanitizePublicTaskText(candidate) ?? '', 1600) || null;
}

function buildPlanAssessment(input: Record<string, unknown>, chinese: boolean): ToolPublicPlanAssessment | null {
  const plan = isRecord(input.plan) ? input.plan : input;
  const feasibility = isRecord(plan.feasibility)
    ? plan.feasibility
    : isRecord(input.feasibility)
      ? input.feasibility
      : null;
  if (!feasibility) return null;
  const confidence = typeof feasibility.confidence === 'string' ? feasibility.confidence.trim().toLowerCase() : '';
  const labels: Record<string, ToolPublicDetailTextKey> = {
    high: 'confidenceHigh',
    medium: 'confidenceMedium',
    low: 'confidenceLow',
  };
  const labelKey = labels[confidence];
  if (!labelKey) return null;
  const rationale =
    typeof feasibility.rationale === 'string' ? sanitizePublicTaskText(feasibility.rationale.trim()) : null;
  return {
    confidence: toolPublicDetailText(chinese, labelKey),
    rationale: rationale ? truncate(rationale, 1200) : null,
  };
}

function publicToolKindLabel(tool: NormalizedToolCall, chinese: boolean): string {
  const normalized = tool.name.trim().toLowerCase();
  if (normalized === 'bash' || normalized === 'shell') return 'BASH';
  if (normalized === 'powershell') return 'POWERSHELL';
  if (normalized === 'python') return 'PYTHON';
  if (normalized === 'r') return 'R';
  if (normalized === 'repl' || normalized === 'code_execution')
    return toolPublicDetailText(chinese, 'toolAnalysisUpper');
  if (normalized === 'skill' || normalized === 'load_skill') return toolPublicDetailText(chinese, 'toolGuide');
  if (normalized === 'generate_plan') return toolPublicDetailText(chinese, 'toolPlan');
  if (normalized === 'manage_environments' || normalized === 'manage_packages') {
    return toolPublicDetailText(chinese, 'toolEnvironmentUpper');
  }
  if (normalized === 'edit_file' || normalized === 'write_file' || normalized === 'replace') {
    return toolPublicDetailText(chinese, 'toolFile');
  }
  if (normalized === 'save_artifacts') return toolPublicDetailText(chinese, 'toolFiles');
  switch (getToolActivityKind(tool.name)) {
    case 'analysis':
      return toolPublicDetailText(chinese, 'toolAnalysis');
    case 'inspect':
      return toolPublicDetailText(chinese, 'toolInspection');
    case 'search':
      return toolPublicDetailText(chinese, 'toolSearch');
    case 'fetch':
      return toolPublicDetailText(chinese, 'toolRetrieval');
    case 'save':
      return toolPublicDetailText(chinese, 'toolSave');
    case 'edit':
      return toolPublicDetailText(chinese, 'toolEdit');
    case 'prepare':
      return toolPublicDetailText(chinese, 'toolPreparation');
    case 'plan':
      return toolPublicDetailText(chinese, 'toolPlanning');
    case 'environment':
      return toolPublicDetailText(chinese, 'toolEnvironment');
    case 'compute':
      return toolPublicDetailText(chinese, 'toolCompute');
    case 'memory':
      return toolPublicDetailText(chinese, 'toolMemory');
    case 'access':
      return toolPublicDetailText(chinese, 'toolAccess');
    default:
      return toolPublicDetailText(chinese, 'toolOperation');
  }
}

function publicToolContext(
  tool: NormalizedToolCall,
  input: Record<string, unknown> | null,
  chinese: boolean
): string | null {
  if (!input) return null;
  const normalizedName = tool.name.trim().toLowerCase();
  if (normalizedName === 'skill' || normalizedName === 'load_skill') return safeShortContext(input.skill);
  if (normalizedName === 'read_memory' || normalizedName === 'write_memory') {
    return publicMemoryScopeLabel(input.entity, chinese) ?? safeShortContext(input.category);
  }
  const path = typeof input.file_path === 'string' ? formatPublicTaskPath(input.file_path) : null;
  if (path) return truncate(path, 96);
  if (getToolActivityKind(tool.name) === 'analysis') {
    const environment = publicEnvironmentContext(input.environment);
    if (environment) return `ENV ${environment}`;
  }
  const domain = safeShortContext(input.domain);
  if (domain) return domain.replace(/^https?:\/\//iu, '');
  const provider = safeShortContext(input.provider);
  if (provider) return provider;
  const filename = safeShortContext(input.filename);
  return filename ? formatPublicTaskPath(filename) : null;
}
