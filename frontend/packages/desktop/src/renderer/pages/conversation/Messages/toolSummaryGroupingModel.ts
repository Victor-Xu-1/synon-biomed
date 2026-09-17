import type { IMessageAcpToolCall, IMessageToolCall, IMessageToolGroup } from '@/common/chat/chatLib';
import { normalizeToolMessages, type NormalizedToolCall, type ToolMessage } from '@/common/chat/normalizeToolCall';
import { canGroupToolActivitySequence } from './toolActivityPresentationRegistry';

export type ToolSummaryMessage = IMessageToolGroup | IMessageAcpToolCall | IMessageToolCall;

const normalizedMessageTools = (messages: ToolSummaryMessage[]): NormalizedToolCall[] =>
  normalizeToolMessages(messages as ToolMessage[]);

const isNestedConnectorChild = (tool: NormalizedToolCall): boolean => /^mcp(?:__|_)/iu.test(tool.name.trim());

const isConnectorDispatchWrapper = (tool: NormalizedToolCall): boolean => {
  if (!/^(?:repl|python|r|bash|code_execution)$/iu.test(tool.name.trim())) return false;
  if (!tool.input) return false;
  try {
    const parsed = JSON.parse(tool.input) as Record<string, unknown>;
    return typeof parsed.code === 'string' && /\bhost\.mcp\s*\(/u.test(parsed.code);
  } catch {
    return false;
  }
};

const parseOutputRecord = (value: string | undefined): Record<string, unknown> | null => {
  if (!value) return null;
  try {
    const parsed = JSON.parse(value) as unknown;
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? (parsed as Record<string, unknown>) : null;
  } catch {
    return null;
  }
};

/**
 * Connector wrappers often print the exact evidence the model inspected while
 * the typed child carries the structured source payload. They are two durable
 * views of one operation, so retain both in one output envelope. Only
 * user-auditable execution evidence is copied from the wrapper; kernel and
 * dispatch identities deliberately stay private.
 */
export const mergePublicToolOutputEvidence = (
  primaryOutput: string | undefined,
  supplementalOutput: string | undefined
): string | undefined => {
  const supplemental = parseOutputRecord(supplementalOutput);
  if (!supplemental) return primaryOutput ?? supplementalOutput;

  const primary = parseOutputRecord(primaryOutput);
  if (!primary) return primaryOutput ?? supplementalOutput;

  const merged: Record<string, unknown> = { ...primary };
  for (const key of ['stdout', 'stderr', 'files_written'] as const) {
    const supplementalValue = supplemental[key];
    const primaryValue = primary[key];
    const supplementalHasEvidence =
      (typeof supplementalValue === 'string' && supplementalValue.trim().length > 0) ||
      (Array.isArray(supplementalValue) && supplementalValue.length > 0);
    const primaryHasEvidence =
      (typeof primaryValue === 'string' && primaryValue.trim().length > 0) ||
      (Array.isArray(primaryValue) && primaryValue.length > 0);
    const isRedundantSerialization =
      key === 'stdout' &&
      (isStructuredSerialization(supplementalValue) || isReadableEvidenceAlreadyStructured(primary, supplementalValue));
    if (supplementalHasEvidence && !primaryHasEvidence && !isRedundantSerialization) merged[key] = supplementalValue;
  }

  return JSON.stringify(merged);
};

const isStructuredSerialization = (value: unknown): boolean => {
  if (typeof value !== 'string') return false;
  const trimmed = value.trim();
  if (!trimmed || (!trimmed.startsWith('{') && !trimmed.startsWith('['))) return false;
  try {
    JSON.parse(trimmed);
    return true;
  } catch {
    return false;
  }
};

const isReadableEvidenceAlreadyStructured = (structured: Record<string, unknown>, value: unknown): boolean => {
  if (typeof value !== 'string' || !value.trim()) return false;
  const serialized = JSON.stringify(structured).toLocaleLowerCase();
  const evidenceValues = value
    .split(/\r?\n/gu)
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) => line.replace(/^[^:：]{1,48}[:：]\s*/u, '').trim())
    .filter((line) => line.length >= 3);
  return evidenceValues.length > 0 && evidenceValues.every((line) => serialized.includes(line.toLocaleLowerCase()));
};

/**
 * A kernel cell that only dispatches a typed connector call and its durable
 * child result are one public operation. Keep the child evidence and use the
 * wrapper's task description when it is more informative.
 */
export function collapseNestedConnectorDispatches(tools: NormalizedToolCall[]): NormalizedToolCall[] {
  const byOperationId = new Map<string, number>();
  const childrenByParent = new Map<string, number[]>();
  for (let index = 0; index < tools.length; index += 1) {
    const tool = tools[index];
    byOperationId.set(toolOperationIdentity(tool, tool.operationId ?? tool.key), index);
    if (tool.parentOperationId) {
      const parentIdentity = toolOperationIdentity(tool, tool.parentOperationId);
      const children = childrenByParent.get(parentIdentity) ?? [];
      children.push(index);
      childrenByParent.set(parentIdentity, children);
    }
  }
  const consumed = new Set<number>();
  const collapsed: NormalizedToolCall[] = [];
  for (let index = 0; index < tools.length; index += 1) {
    if (consumed.has(index)) continue;
    const tool = tools[index];
    const operationID = toolOperationIdentity(tool, tool.operationId ?? tool.key);
    const childIndexes = childrenByParent.get(operationID) ?? [];
    const childIndex = childIndexes.length === 1 ? childIndexes[0] : undefined;
    const child = childIndex === undefined ? undefined : tools[childIndex];
    if (
      child &&
      childIndex > index &&
      byOperationId.get(toolOperationIdentity(child, child.parentOperationId ?? '')) === index &&
      isConnectorDispatchWrapper(tool) &&
      isNestedConnectorChild(child)
    ) {
      const wrapperHistoryRef =
        tool.messageId && tool.conversationId
          ? [
              {
                messageId: tool.messageId,
                conversationId: tool.conversationId,
                operationKey: tool.key,
                branchId: tool.branchId,
              },
            ]
          : [];
      collapsed.push({
        ...child,
        humanDescription: tool.humanDescription ?? child.humanDescription,
        output: mergePublicToolOutputEvidence(child.output, tool.output),
        supplementalHistoryRefs: [...(child.supplementalHistoryRefs ?? []), ...wrapperHistoryRef],
      });
      consumed.add(childIndex);
      continue;
    }
    collapsed.push(tool);
  }
  return collapsed;
}

function toolOperationIdentity(tool: NormalizedToolCall, operationId: string): string {
  return `${tool.attempt ?? 0}:${operationId}`;
}

/**
 * Protocol batches are transport boundaries, not presentation boundaries.
 * Consecutive calls in one public work phase share one visual carrier. A real
 * assistant progress message is handled by the projection as a hard boundary;
 * a semantic phase change is the fallback boundary for older tool-only traces.
 */
export function startsNewToolSummary(current: ToolSummaryMessage[], next: ToolSummaryMessage): boolean {
  if (current.length === 0) return false;
  const currentTools = collapseNestedConnectorDispatches(normalizedMessageTools(current));
  const combinedTools = collapseNestedConnectorDispatches(normalizedMessageTools([...current, next]));
  if (combinedTools.length === currentTools.length) return false;
  const nextTools = combinedTools.slice(currentTools.length);
  if (currentTools.length === 0 || nextTools.length === 0) return true;
  return !canGroupToolActivitySequence(combinedTools.map((tool) => tool.name));
}
