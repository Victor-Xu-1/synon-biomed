import type { NormalizedToolCall } from '@/common/chat/normalizeToolCall';

export type ToolExecutionDisposition = 'not-executed' | 'preflight-passed' | 'preflight-blocked';

export interface ToolExecutionPresentationGroup {
  item: NormalizedToolCall;
  repeatCount: number;
}

const record = (value: unknown): Record<string, unknown> | null =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : null;

interface ToolProtocolEnvelope {
  outer: Record<string, unknown>;
  result: Record<string, unknown>;
  envelopes: Record<string, unknown>[];
}

const protocolEnvelopeCache = new WeakMap<NormalizedToolCall, ToolProtocolEnvelope | null>();

const protocolEnvelope = (tool: NormalizedToolCall): ToolProtocolEnvelope | null => {
  if (protocolEnvelopeCache.has(tool)) return protocolEnvelopeCache.get(tool) ?? null;
  if ((tool.status !== 'completed' && tool.status !== 'error') || !tool.output) return null;
  let outer: Record<string, unknown> | null;
  try {
    outer = record(JSON.parse(tool.output));
  } catch {
    protocolEnvelopeCache.set(tool, null);
    return null;
  }
  if (!outer) {
    protocolEnvelopeCache.set(tool, null);
    return null;
  }
  const nested = record(outer.result);
  const protocol = {
    outer,
    result: nested ?? outer,
    envelopes: [outer, ...(nested ? [nested] : [])],
  };
  protocolEnvelopeCache.set(tool, protocol);
  return protocol;
};

const firstProtocolString = (envelopes: Record<string, unknown>[], key: string): string | null => {
  for (const envelope of envelopes.toReversed()) {
    const value = envelope[key];
    if (typeof value === 'string' && value.trim()) return value.trim().toLowerCase();
  }
  return null;
};

// Lifecycle completion means the tool returned, not necessarily that the
// requested operation executed. Inspect only protocol envelopes, not records
// or prose inside scientific results.
export function toolExecutionDisposition(tool: NormalizedToolCall): ToolExecutionDisposition | null {
  const protocol = protocolEnvelope(tool);
  if (!protocol) return null;
  const { envelopes, result } = protocol;
  const explicitlyExecuted = envelopes.some((value) => value.executed === true);
  const notExecuted = !explicitlyExecuted && envelopes.some((value) => value.executed === false);
  const preflight = result.mode === 'preflight' && result.ok === true && !explicitlyExecuted;
  if (!notExecuted && !preflight) return null;
  if (
    envelopes.some(
      (value) => value.ok === false || value.success === false || value.isError === true || value.partial === true
    )
  ) {
    return notExecuted ? 'not-executed' : null;
  }
  if (envelopes.some((value) => value.decision_required === true)) return 'not-executed';
  if (result.mode === 'preflight') {
    if (result.feasible === true) return 'preflight-passed';
    if (result.feasible === false) return 'preflight-blocked';
  }
  return 'not-executed';
}

export const toolWasNotExecuted = (tool: NormalizedToolCall): boolean => {
  const disposition = toolExecutionDisposition(tool);
  return disposition === 'not-executed' || disposition === 'preflight-blocked';
};

/**
 * Returns a stable machine-derived family only for operations rejected during
 * preflight. The fingerprint is preferred because it identifies one recovery
 * condition across harmless argument rewrites; protocol codes are the safe
 * fallback. Human descriptions and tool arguments are intentionally excluded.
 */
export function toolExecutionRecoveryFamily(tool: NormalizedToolCall): string | null {
  if (!toolWasNotExecuted(tool)) return null;
  const protocol = protocolEnvelope(tool);
  if (!protocol) return null;
  const { envelopes, result } = protocol;
  const isPreflight = envelopes.some((value) => value.preflight === true) || result.mode === 'preflight';
  if (!isPreflight) return null;

  const toolIdentity = firstProtocolString(envelopes, 'tool') ?? tool.name.trim().toLowerCase();
  const reason =
    firstProtocolString(envelopes, 'failure_fingerprint') ??
    firstProtocolString(envelopes, 'detail_code') ??
    firstProtocolString(envelopes, 'code') ??
    firstProtocolString(envelopes, 'status');
  return reason ? `${tool.name.trim().toLowerCase()}\u001f${toolIdentity}\u001f${reason}` : null;
}

/**
 * Consecutive preflight rejections from the same machine-defined recovery
 * family represent one public diagnostic condition. Keep the first item as
 * the stable React/audit representative and expose the exact occurrence count.
 * Real executions, distinct causes, and non-consecutive events remain separate.
 */
export function groupRepeatedNonExecutingTools(tools: NormalizedToolCall[]): ToolExecutionPresentationGroup[] {
  const grouped: Array<ToolExecutionPresentationGroup & { family: string | null }> = [];
  for (const item of tools) {
    const family = toolExecutionRecoveryFamily(item);
    const previous = grouped[grouped.length - 1];
    if (family && previous?.family === family) {
      previous.repeatCount += 1;
      continue;
    }
    grouped.push({ item, repeatCount: 1, family });
  }
  return grouped.map(({ family: _family, ...group }) => group);
}
