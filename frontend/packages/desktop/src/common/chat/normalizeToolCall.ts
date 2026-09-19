import type { IMessageAcpToolCall, IMessageToolCall, IMessageToolGroup } from './chatLib';
import { getAcpImagePath } from './acpToolCallOutput';

export type NormalizedToolStatus =
  | 'pending'
  | 'running'
  | 'waiting'
  | 'blocked'
  | 'completed'
  | 'error'
  | 'canceled'
  | 'interrupted'
  | 'unknown';

export type NormalizedToolProgress = {
  phase: string;
  message?: string;
  process?: string;
  phasePercent?: number;
  bytesPerSecond?: number;
  bytesCompleted?: number;
  bytesTotal?: number;
  completedItems?: number;
  totalItems?: number;
  elapsedMs?: number;
  indeterminate: boolean;
};

export interface NormalizedToolCall {
  key: string;
  name: string;
  status: NormalizedToolStatus;
  description?: string;
  humanDescription?: string;
  input?: string;
  output?: string;
  truncated?: boolean;
  compactResultCount?: number;
  progress?: NormalizedToolProgress;
  attempt?: number;
  operationId?: string;
  parentOperationId?: string;
  revision?: number;
  phase?: string;
  settlementReason?: string;
  messageId?: string;
  conversationId?: string;
  branchId?: string;
  supplementalHistoryRefs?: Array<{
    messageId: string;
    conversationId: string;
    operationKey?: string;
    branchId?: string;
  }>;
  imagePath?: string;
  streamingRecoveryError?: boolean;
  remoteDetail?: {
    conversationId: string;
    messageId: string;
    branchId?: string;
    revision?: number;
    contentUrl?: string;
  };
  fileDiffs?: Array<{
    path: string;
    oldText: string;
    newText: string;
  }>;
  subagent?: IMessageToolCall['content']['subagent'];
}

const normalizeHumanDescription = (value: unknown): string | undefined => {
  if (typeof value !== 'string') return undefined;
  const normalized = value.trim().replace(/\s+/g, ' ');
  return normalized ? normalized.slice(0, 256) : undefined;
};

const boundedProgressNumber = (candidate: unknown, maximum: number): number | undefined =>
  typeof candidate === 'number' && Number.isFinite(candidate) && candidate >= 0 && candidate <= maximum
    ? candidate
    : undefined;

const normalizeToolProgress = (value: unknown): NormalizedToolProgress | undefined => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined;
  const record = value as Record<string, unknown>;
  const phase = typeof record.phase === 'string' ? record.phase.trim() : '';
  if (!/^[a-z0-9_-]{1,80}$/u.test(phase)) return undefined;
  const phasePercent = boundedProgressNumber(record.phasePercent, 100);
  const bytesPerSecond = boundedProgressNumber(record.bytesPerSecond, 1e15);
  const bytesCompleted = boundedProgressNumber(record.bytesCompleted, Number.MAX_SAFE_INTEGER);
  const bytesTotal = boundedProgressNumber(record.bytesTotal, Number.MAX_SAFE_INTEGER);
  const completedItems = boundedProgressNumber(record.completedItems, Number.MAX_SAFE_INTEGER);
  const totalItems = boundedProgressNumber(record.totalItems, Number.MAX_SAFE_INTEGER);
  const elapsedMs = boundedProgressNumber(record.elapsedMs, 365 * 24 * 60 * 60 * 1000);
  const message = normalizeHumanDescription(record.message);
  const process =
    typeof record.process === 'string' && /^[a-z0-9._-]{1,40}$/u.test(record.process) ? record.process : undefined;
  return {
    phase,
    ...(message ? { message } : {}),
    ...(process ? { process } : {}),
    ...(phasePercent === undefined ? {} : { phasePercent }),
    ...(bytesPerSecond === undefined ? {} : { bytesPerSecond }),
    ...(bytesCompleted === undefined ? {} : { bytesCompleted }),
    ...(bytesTotal === undefined || bytesTotal <= 0 ? {} : { bytesTotal }),
    ...(completedItems === undefined ? {} : { completedItems }),
    ...(totalItems === undefined || totalItems <= 0 ? {} : { totalItems }),
    ...(elapsedMs === undefined ? {} : { elapsedMs }),
    indeterminate: record.indeterminate === true,
  };
};

const formatValue = (value: unknown): string => {
  if (typeof value === 'string') return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
};

const outputReportsSemanticFailure = (output: string | undefined): boolean => {
  if (!output) return false;
  try {
    const value = JSON.parse(output) as unknown;
    if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
    const record = value as Record<string, unknown>;
    return (
      record.ok === false ||
      record.outcome === 'failed' ||
      record.status === 'failed' ||
      record.status === 'error' ||
      record.exit_status === 'error'
    );
  } catch {
    return false;
  }
};

// ===== tool_group → NormalizedToolCall[] =====

function normalizeToolGroupStatus(status: string): NormalizedToolStatus {
  switch (status) {
    case 'Success':
      return 'completed';
    case 'Error':
      return 'error';
    case 'Canceled':
      return 'canceled';
    case 'Pending':
      return 'pending';
    case 'Executing':
    case 'Confirming':
    default:
      return 'running';
  }
}

const getResultDisplayText = (
  result_display: IMessageToolGroup['content'][0]['result_display']
): string | undefined => {
  if (!result_display) return undefined;
  if (typeof result_display === 'string') return result_display;
  if ('file_diff' in result_display) return result_display.file_diff;
  if ('img_url' in result_display) return result_display.relative_path || result_display.img_url;
  return undefined;
};

export function normalizeToolGroup(message: IMessageToolGroup): NormalizedToolCall[] {
  if (!Array.isArray(message.content)) return [];
  return message.content.map(({ name, call_id, description, confirmationDetails, status, result_display }) => {
    let desc = typeof description === 'string' ? description.slice(0, 100) : '';
    const type = confirmationDetails?.type;
    if (type === 'edit') desc = confirmationDetails.file_name;
    if (type === 'exec') desc = confirmationDetails.command;
    if (type === 'info') desc = confirmationDetails.urls?.join(';') || confirmationDetails.title;
    if (type === 'mcp') desc = confirmationDetails.server_name + ':' + confirmationDetails.tool_name;

    let input: string | undefined;
    if (confirmationDetails) {
      const { title: _title, type: _type, ...rest } = confirmationDetails;
      if (Object.keys(rest).length) input = formatValue(rest);
    } else if (description) {
      input = description;
    }

    return {
      key: call_id,
      name,
      status: normalizeToolGroupStatus(status),
      description: desc,
      input,
      output: getResultDisplayText(result_display),
    };
  });
}

// ===== acp_tool_call → NormalizedToolCall =====

function normalizeAcpStatus(status: string): NormalizedToolStatus {
  switch (status) {
    case 'completed':
      return 'completed';
    case 'failed':
      return 'error';
    case 'in_progress':
      return 'running';
    case 'pending':
    default:
      return 'pending';
  }
}

const buildParamSummary = (kind: string, rawInput?: Record<string, unknown>): string | undefined => {
  if (!rawInput) return undefined;

  if (kind === 'read' || kind === 'edit') {
    return (rawInput.file_path as string) || (rawInput.path as string) || (rawInput.file_name as string);
  }
  if (kind === 'execute') {
    return rawInput.command as string;
  }
  if (kind === 'search' || kind === 'grep') {
    const parts: string[] = [];
    if (rawInput.pattern) parts.push(`"${rawInput.pattern}"`);
    if (rawInput.path) parts.push(`in ${rawInput.path}`);
    else if (rawInput.glob) parts.push(`in ${rawInput.glob}`);
    return parts.length > 0 ? parts.join(' ') : undefined;
  }
  if (kind === 'glob') {
    const parts: string[] = [];
    if (rawInput.pattern) parts.push(`${rawInput.pattern}`);
    if (rawInput.path) parts.push(`in ${rawInput.path}`);
    return parts.length > 0 ? parts.join(' ') : undefined;
  }
  if (kind === 'write') {
    return (rawInput.file_path as string) || (rawInput.path as string);
  }

  for (const key of ['file_path', 'command', 'path', 'pattern', 'query', 'url']) {
    if (rawInput[key] && typeof rawInput[key] === 'string') return rawInput[key] as string;
  }
  return undefined;
};

type AcpToolCallUpdateCompat = IMessageAcpToolCall['content']['update'] & {
  session_update?: string;
  raw_input?: Record<string, unknown>;
};

type AcpToolCallContentCompat = IMessageAcpToolCall['content'] & {
  _compact?: {
    truncated?: boolean;
    original_size?: number;
    preview_chars?: number;
    result_count?: number;
  };
  update?: AcpToolCallUpdateCompat;
};

export function normalizeAcpToolCall(message: IMessageAcpToolCall): NormalizedToolCall | undefined {
  const content = message.content as AcpToolCallContentCompat | undefined;
  const update = content?.update;
  if (!update) return undefined;

  const rawInput = update.rawInput ?? update.raw_input;
  const input = rawInput ? formatValue(rawInput) : undefined;

  let output: string | undefined;
  if (Array.isArray(update.content) && update.content.length) {
    output = update.content
      .map((item) => {
        if (item.type === 'content' && item.content?.text) return item.content.text;
        if (item.type === 'diff' && 'path' in item) return `[diff] ${item.path}`;
        return '';
      })
      .filter(Boolean)
      .join('\n');
  }

  const keyParam = buildParamSummary(update.kind, rawInput);
  const fileDiffs = Array.isArray(update.content)
    ? update.content.flatMap((item) =>
        item.type === 'diff'
          ? [
              {
                path: item.path ?? '',
                oldText: item.old_text ?? '',
                newText: item.new_text ?? '',
              },
            ]
          : []
      )
    : [];

  const transportStatus = normalizeAcpStatus(update.status);
  const status = transportStatus === 'completed' && outputReportsSemanticFailure(output) ? 'error' : transportStatus;

  return {
    key: update.tool_call_id,
    name: update.title?.trim() || update.kind,
    status,
    description: keyParam || (rawInput?.command as string) || update.kind,
    humanDescription: normalizeHumanDescription(rawInput?.human_description) ?? normalizeHumanDescription(update.title),
    input,
    output,
    truncated: content?._compact?.truncated === true,
    compactResultCount: normalizeCompactResultCount(content?._compact?.result_count),
    messageId: message.id,
    conversationId: message.conversation_id,
    imagePath: getAcpImagePath(update),
    ...(fileDiffs.length > 0 ? { fileDiffs } : {}),
  };
}

// ===== tool_call → NormalizedToolCall =====

function normalizeToolCallStatus(status?: string): NormalizedToolStatus {
  switch (status) {
    case 'completed':
      return 'completed';
    case 'error':
      return 'error';
    case 'canceled':
      return 'canceled';
    case 'running':
      return 'running';
    case 'waiting':
      return 'waiting';
    case 'blocked':
      return 'blocked';
    case 'interrupted':
      return 'interrupted';
    case 'unknown':
      return 'unknown';
    default:
      return status === undefined || status === '' ? 'pending' : 'unknown';
  }
}

type ToolCallContentCompat = IMessageToolCall['content'] & {
  _compact?: {
    truncated?: boolean;
    original_size?: number;
    result_count?: number;
    human_description?: string;
  };
};

export function normalizeToolCall(message: IMessageToolCall): NormalizedToolCall | undefined {
  const content = message.content as ToolCallContentCompat;
  const {
    call_id,
    name,
    status,
    input,
    output,
    error,
    args,
    description,
    subagent,
    progress,
    attempt,
    operation_id,
    parent_operation_id,
    revision,
    phase,
    settlement_reason,
  } = content;
  if (!call_id) return undefined;

  const displayInput =
    input !== undefined
      ? formatValue(input)
      : args && (typeof args === 'string' || Object.keys(args).length > 0)
        ? formatValue(args)
        : undefined;
  const inputRecord = input && typeof input === 'object' && !Array.isArray(input) ? input : undefined;
  const argsRecord = args && typeof args === 'object' && !Array.isArray(args) ? args : undefined;

  const transportStatus = normalizeToolCallStatus(status);
  const normalizedStatus =
    transportStatus === 'completed' && outputReportsSemanticFailure(output ?? error) ? 'error' : transportStatus;
  const operationKey =
    Number.isSafeInteger(attempt) && Number(attempt) >= 1 ? `${Number(attempt)}:${operation_id || call_id}` : call_id;

  return {
    key: operationKey,
    name,
    status: normalizedStatus,
    description: description || undefined,
    humanDescription: normalizeHumanDescription(
      inputRecord?.human_description ?? argsRecord?.human_description ?? content._compact?.human_description
    ),
    input: displayInput,
    output: output ?? error,
    truncated: content._compact?.truncated === true,
    compactResultCount: normalizeCompactResultCount(content._compact?.result_count),
    progress: normalizeToolProgress(progress),
    ...(Number.isSafeInteger(attempt) && Number(attempt) >= 1 ? { attempt: Number(attempt) } : {}),
    ...(operation_id ? { operationId: operation_id } : {}),
    ...(parent_operation_id ? { parentOperationId: parent_operation_id } : {}),
    ...(Number.isSafeInteger(revision) && Number(revision) >= 1 ? { revision: Number(revision) } : {}),
    ...(phase ? { phase } : {}),
    ...(settlement_reason ? { settlementReason: settlement_reason } : {}),
    ...(content.streamingRecoveryError === true ? { streamingRecoveryError: true } : {}),
    messageId: message.id,
    conversationId: message.conversation_id,
    ...(typeof content.synonBiomed?.branchId === 'string' && /^br_[0-9a-f]{8}$/u.test(content.synonBiomed.branchId)
      ? { branchId: content.synonBiomed.branchId }
      : {}),
    subagent,
  };
}

function normalizeCompactResultCount(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? Math.trunc(value) : undefined;
}

// ===== Unified entry =====

export type ToolMessage = IMessageToolGroup | IMessageAcpToolCall | IMessageToolCall;

const isTerminalToolStatus = (status: NormalizedToolStatus): boolean =>
  status === 'completed' ||
  status === 'error' ||
  status === 'canceled' ||
  status === 'interrupted' ||
  status === 'unknown';

export const isActiveToolStatus = (status: NormalizedToolStatus): boolean =>
  status === 'pending' || status === 'running' || status === 'waiting' || status === 'blocked';

const mergeNormalizedToolCall = (existing: NormalizedToolCall, incoming: NormalizedToolCall): NormalizedToolCall => {
  // A terminal receipt is one coherent snapshot, not just immutable output.
  // Never pair its evidence with a later input, revision or history pointer.
  if (isTerminalToolStatus(existing.status)) {
    const settled = { ...incoming };
    for (const [key, value] of Object.entries(existing)) {
      if (value !== undefined) Object.assign(settled, { [key]: value });
    }
    return settled;
  }
  if (existing.revision !== undefined && incoming.revision !== undefined && incoming.revision < existing.revision) {
    return existing;
  }
  const merged = { ...existing };
  for (const [key, value] of Object.entries(incoming)) {
    if (value !== undefined) Object.assign(merged, { [key]: value });
  }
  return merged;
};

export function normalizeToolMessages(messages: ToolMessage[]): NormalizedToolCall[] {
  const normalized = messages
    .flatMap((m) => {
      if (m.type === 'tool_group') return normalizeToolGroup(m);
      if (m.type === 'acp_tool_call') return normalizeAcpToolCall(m);
      if (m.type === 'tool_call') return normalizeToolCall(m);
      return undefined;
    })
    .filter((item): item is NormalizedToolCall => item !== undefined);
  const byKey = new Map<string, NormalizedToolCall>();
  for (const item of normalized) {
    const identity = JSON.stringify([item.conversationId, item.branchId, item.key]);
    const existing = byKey.get(identity);
    byKey.set(identity, existing ? mergeNormalizedToolCall(existing, item) : item);
  }
  return [...byKey.values()];
}

export function hasRunningToolMessages(messages: ToolMessage[]): boolean {
  return messages.some((m) => {
    if (m.type === 'tool_group') {
      return Array.isArray(m.content) && m.content.some((t) => isActiveToolStatus(normalizeToolGroupStatus(t.status)));
    }
    if (m.type === 'acp_tool_call') {
      return Boolean(m.content?.update && isActiveToolStatus(normalizeAcpStatus(m.content.update.status)));
    }
    if (m.type === 'tool_call') {
      return isActiveToolStatus(normalizeToolCallStatus(m.content?.status));
    }
    return false;
  });
}
