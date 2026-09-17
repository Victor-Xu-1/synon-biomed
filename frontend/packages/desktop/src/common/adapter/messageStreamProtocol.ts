export const MESSAGE_STREAM_TYPES = ['start', 'thinking', 'text', 'content', 'tool_call', 'finish', 'error'] as const;

export type MessageStreamType = (typeof MESSAGE_STREAM_TYPES)[number];

export type ToolLifecycleStatus =
  | 'pending'
  | 'running'
  | 'waiting'
  | 'blocked'
  | 'completed'
  | 'error'
  | 'canceled'
  | 'interrupted'
  | 'unknown';

export type ToolCallStreamData = {
  call_id: string;
  name: string;
  status: ToolLifecycleStatus;
  attempt?: number;
  operation_id?: string;
  parent_operation_id?: string;
  revision?: number;
  phase?: string;
  settlement_reason?: string;
  args?: Record<string, unknown> | string;
  input?: Record<string, unknown> | string;
  output?: string;
  error?: string;
  description?: string;
  progress?: Record<string, unknown>;
  _compact?: Record<string, unknown>;
};

export type ArtifactReferenceRelation = 'produced' | 'consumed' | 'cited' | 'attached';
export type ArtifactReferenceAvailability = 'available' | 'deleted' | 'missing';

export type ArtifactReferenceWire = {
  artifact_id: string;
  version_id: string;
  relation: ArtifactReferenceRelation;
  availability?: ArtifactReferenceAvailability;
  attempt?: number;
  source_event_id?: number;
  ordinal?: number;
  filename?: string;
  content_type?: string;
  size_bytes?: number;
  checksum?: string;
};

export interface IResponseMessage {
  type: string;
  data: unknown;
  msg_id: string;
  turn_id?: string;
  conversation_id: string;
  created_at?: number;
  hidden?: boolean;
  position?: 'left' | 'right' | 'center' | 'pop';
  status?: 'finish' | 'pending' | 'error' | 'work';
  terminal_status?: 'completed' | 'failed' | 'cancelled';
  /** A later attempt accepted for this same logical input supersedes this terminal state. */
  terminal_superseded?: boolean;
  /** Replace accumulated text for the same msg_id instead of appending. */
  replace?: boolean;
  /** Replace every persisted assistant segment owned by this exact attempt. */
  replace_scope?: 'attempt';
  /** Durable attempt identity used for exact supersession without parsing msg_id. */
  assistant_attempt_id?: string;
  artifact_refs?: ArtifactReferenceWire[];
  source_publication_sequence?: number;
  publication_boundary_id?: string;
}

type JsonRecord = Record<string, unknown>;

const STREAM_TYPES = new Set<string>(MESSAGE_STREAM_TYPES);
const POSITIONS = new Set(['left', 'right', 'center', 'pop']);
const STATUSES = new Set(['finish', 'pending', 'error', 'work']);
const TERMINAL_STATUSES = new Set(['completed', 'failed', 'cancelled']);
const TOOL_LIFECYCLE_STATUSES = new Set<ToolLifecycleStatus>([
  'pending',
  'running',
  'waiting',
  'blocked',
  'completed',
  'error',
  'canceled',
  'interrupted',
  'unknown',
]);
const MAX_IDENTIFIER_LENGTH = 256;
const MAX_ARTIFACT_IDENTIFIER_LENGTH = 512;
const MAX_ARTIFACT_REFERENCES = 256;
const ARTIFACT_RELATIONS = new Set<ArtifactReferenceRelation>(['produced', 'consumed', 'cited', 'attached']);
const ARTIFACT_AVAILABILITY = new Set<ArtifactReferenceAvailability>(['available', 'deleted', 'missing']);

const isRecord = (value: unknown): value is JsonRecord =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const hasControlCharacter = (value: string): boolean => {
  for (const character of value) {
    const codePoint = character.codePointAt(0);
    if (codePoint !== undefined && (codePoint <= 31 || codePoint === 127)) return true;
  }
  return false;
};

const isExactIdentifier = (value: unknown): value is string =>
  typeof value === 'string' &&
  value.length > 0 &&
  value.length <= MAX_IDENTIFIER_LENGTH &&
  value.trim() === value &&
  !hasControlCharacter(value);

const isArtifactIdentifier = (value: unknown): value is string =>
  typeof value === 'string' &&
  value.length > 0 &&
  value.length <= MAX_ARTIFACT_IDENTIFIER_LENGTH &&
  value.trim() === value &&
  !hasControlCharacter(value);

const isOptionalEnum = (value: unknown, allowed: Set<string>): value is string | undefined =>
  value === undefined || (typeof value === 'string' && allowed.has(value));

const isOptionalBoolean = (value: unknown): value is boolean | undefined =>
  value === undefined || typeof value === 'boolean';

const isOptionalSafeInteger = (value: unknown, minimum: number): value is number | undefined =>
  value === undefined || (Number.isSafeInteger(value) && (value as number) >= minimum);

const isOptionalBoundedString = (value: unknown, maximum: number): value is string | undefined =>
  value === undefined || (typeof value === 'string' && value.length <= maximum && !hasControlCharacter(value));

const isOptionalSizedString = (value: unknown, maximum: number): value is string | undefined =>
  value === undefined || (typeof value === 'string' && value.length <= maximum);

const isOptionalBoundedRecordOrString = (value: unknown, maximum: number): boolean => {
  if (value === undefined) return true;
  if (typeof value === 'string') return value.length <= maximum;
  if (!isRecord(value)) return false;
  try {
    return JSON.stringify(value).length <= maximum;
  } catch {
    return false;
  }
};

const isOptionalBoundedRecord = (value: unknown, maximum: number): boolean =>
  value === undefined || (isRecord(value) && isOptionalBoundedRecordOrString(value, maximum));

function decodeToolCallData(raw: unknown): ToolCallStreamData | undefined {
  if (
    !isRecord(raw) ||
    !isExactIdentifier(raw.call_id) ||
    !isExactIdentifier(raw.name) ||
    typeof raw.status !== 'string' ||
    !TOOL_LIFECYCLE_STATUSES.has(raw.status as ToolLifecycleStatus) ||
    !isOptionalSafeInteger(raw.attempt, 1) ||
    !isOptionalSafeInteger(raw.revision, 1) ||
    (raw.operation_id !== undefined && !isExactIdentifier(raw.operation_id)) ||
    (raw.parent_operation_id !== undefined && !isExactIdentifier(raw.parent_operation_id)) ||
    (raw.parent_operation_id !== undefined && raw.parent_operation_id === (raw.operation_id ?? raw.call_id)) ||
    !isOptionalBoundedString(raw.phase, 80) ||
    !isOptionalBoundedString(raw.settlement_reason, 160) ||
    !isOptionalBoundedString(raw.description, 512) ||
    !isOptionalSizedString(raw.output, 256 * 1024) ||
    !isOptionalSizedString(raw.error, 256 * 1024) ||
    !isOptionalBoundedRecordOrString(raw.args, 64 * 1024) ||
    !isOptionalBoundedRecordOrString(raw.input, 64 * 1024) ||
    !isOptionalBoundedRecord(raw.progress, 16 * 1024) ||
    !isOptionalBoundedRecord(raw._compact, 4 * 1024)
  ) {
    return undefined;
  }
  return raw as ToolCallStreamData;
}

export function decodeArtifactReferences(raw: unknown): ArtifactReferenceWire[] | undefined {
  if (!Array.isArray(raw) || raw.length > MAX_ARTIFACT_REFERENCES) return undefined;
  const seen = new Set<string>();
  const references: ArtifactReferenceWire[] = [];
  for (const item of raw) {
    if (
      !isRecord(item) ||
      !isArtifactIdentifier(item.artifact_id) ||
      !isArtifactIdentifier(item.version_id) ||
      typeof item.relation !== 'string' ||
      !ARTIFACT_RELATIONS.has(item.relation as ArtifactReferenceRelation) ||
      (item.availability !== undefined &&
        (typeof item.availability !== 'string' ||
          !ARTIFACT_AVAILABILITY.has(item.availability as ArtifactReferenceAvailability))) ||
      !isOptionalSafeInteger(item.attempt, 1) ||
      !isOptionalSafeInteger(item.source_event_id, 1) ||
      !isOptionalSafeInteger(item.ordinal, 0) ||
      !isOptionalBoundedString(item.filename, 512) ||
      !isOptionalBoundedString(item.content_type, 256) ||
      !isOptionalSafeInteger(item.size_bytes, 0) ||
      !isOptionalBoundedString(item.checksum, 256)
    ) {
      return undefined;
    }
    const key = `${item.artifact_id}\0${item.version_id}`;
    if (seen.has(key)) return undefined;
    seen.add(key);
    references.push({
      artifact_id: item.artifact_id,
      version_id: item.version_id,
      relation: item.relation as ArtifactReferenceRelation,
      ...(item.availability !== undefined ? { availability: item.availability as ArtifactReferenceAvailability } : {}),
      ...(item.attempt !== undefined ? { attempt: item.attempt as number } : {}),
      ...(item.source_event_id !== undefined ? { source_event_id: item.source_event_id as number } : {}),
      ...(item.ordinal !== undefined ? { ordinal: item.ordinal as number } : {}),
      ...(item.filename !== undefined ? { filename: item.filename as string } : {}),
      ...(item.content_type !== undefined ? { content_type: item.content_type as string } : {}),
      ...(item.size_bytes !== undefined ? { size_bytes: item.size_bytes as number } : {}),
      ...(item.checksum !== undefined ? { checksum: item.checksum as string } : {}),
    });
  }
  return references;
}

function decodeMessagePayload(raw: unknown): IResponseMessage | undefined {
  const artifactRefs =
    isRecord(raw) && Object.hasOwn(raw, 'artifact_refs') ? decodeArtifactReferences(raw.artifact_refs) : undefined;
  const toolCallData = isRecord(raw) && raw.stream_type === 'tool_call' ? decodeToolCallData(raw.data) : undefined;
  if (
    !isRecord(raw) ||
    raw.type !== 'message.stream' ||
    !isExactIdentifier(raw.stream_type) ||
    !STREAM_TYPES.has(raw.stream_type) ||
    !Object.hasOwn(raw, 'data') ||
    !isExactIdentifier(raw.msg_id) ||
    !isExactIdentifier(raw.conversation_id) ||
    (raw.turn_id !== undefined && !isExactIdentifier(raw.turn_id)) ||
    (raw.created_at !== undefined && (!Number.isSafeInteger(raw.created_at) || (raw.created_at as number) < 0)) ||
    !isOptionalEnum(raw.position, POSITIONS) ||
    !isOptionalEnum(raw.status, STATUSES) ||
    !isOptionalEnum(raw.terminal_status, TERMINAL_STATUSES) ||
    !isOptionalBoolean(raw.terminal_superseded) ||
    !isOptionalBoolean(raw.hidden) ||
    !isOptionalBoolean(raw.replace) ||
    raw.provisional !== undefined ||
    (raw.replace_scope !== undefined && raw.replace_scope !== 'attempt') ||
    (raw.assistant_attempt_id !== undefined && !isExactIdentifier(raw.assistant_attempt_id)) ||
    (raw.source_publication_sequence !== undefined &&
      (!Number.isSafeInteger(raw.source_publication_sequence) || (raw.source_publication_sequence as number) < 1)) ||
    (raw.publication_boundary_id !== undefined && !isExactIdentifier(raw.publication_boundary_id)) ||
    (raw.source_publication_sequence === undefined) !== (raw.publication_boundary_id === undefined) ||
    (raw.terminal_status !== undefined && raw.stream_type !== 'finish' && raw.stream_type !== 'error') ||
    (raw.terminal_superseded !== undefined && raw.terminal_status === undefined) ||
    (raw.replace_scope === 'attempt' &&
      (raw.replace !== true || raw.stream_type !== 'text' || !isExactIdentifier(raw.assistant_attempt_id))) ||
    (Object.hasOwn(raw, 'artifact_refs') && artifactRefs === undefined) ||
    (raw.stream_type === 'tool_call' && toolCallData === undefined)
  ) {
    return undefined;
  }

  return {
    type: raw.stream_type,
    data: toolCallData ?? raw.data,
    msg_id: raw.msg_id,
    conversation_id: raw.conversation_id,
    ...(raw.turn_id !== undefined ? { turn_id: raw.turn_id as string } : {}),
    ...(raw.created_at !== undefined ? { created_at: raw.created_at as number } : {}),
    ...(raw.hidden !== undefined ? { hidden: raw.hidden } : {}),
    ...(raw.position !== undefined ? { position: raw.position as IResponseMessage['position'] } : {}),
    ...(raw.status !== undefined ? { status: raw.status as IResponseMessage['status'] } : {}),
    ...(raw.terminal_status !== undefined
      ? {
          terminal_status: raw.terminal_status as IResponseMessage['terminal_status'],
        }
      : {}),
    ...(raw.terminal_superseded !== undefined ? { terminal_superseded: raw.terminal_superseded } : {}),
    ...(raw.replace !== undefined ? { replace: raw.replace } : {}),
    ...(raw.replace_scope !== undefined ? { replace_scope: raw.replace_scope as 'attempt' } : {}),
    ...(raw.assistant_attempt_id !== undefined ? { assistant_attempt_id: raw.assistant_attempt_id as string } : {}),
    ...(Object.hasOwn(raw, 'artifact_refs') ? { artifact_refs: artifactRefs! } : {}),
    ...(raw.source_publication_sequence !== undefined
      ? {
          source_publication_sequence: raw.source_publication_sequence as number,
        }
      : {}),
    ...(raw.publication_boundary_id !== undefined
      ? { publication_boundary_id: raw.publication_boundary_id as string }
      : {}),
  };
}

export function decodeMessageStreamPayload(raw: unknown): IResponseMessage | undefined {
  return decodeMessagePayload(raw);
}
