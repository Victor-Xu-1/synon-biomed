export type ConversationToolStdout = {
  toolUseId: string;
  execId?: string;
  toolName?: string;
  startedAt?: string;
  background?: boolean;
  status?: 'queued' | 'running' | 'persisting';
  stdout: string;
  stderr: string;
  throughChunkSequence?: number;
  stdoutStartByte?: number;
  stdoutEndByte?: number;
  stdoutTruncated?: boolean;
  recoveryError?: boolean;
};

export type ConversationStreamingFrame = {
  frameId: string;
  toolStdout: ConversationToolStdout[];
};

export type ConversationStreamingSnapshot = {
  rootFrameId: string;
  historyRevision?: number;
  frames: ConversationStreamingFrame[];
};

type RecordValue = Record<string, unknown>;
const UTF8_ENCODER = new TextEncoder();

/** Decode the recovery endpoint into the only auxiliary live slice the UI consumes. */
export function normalizeConversationStreamingSnapshot(value: unknown): ConversationStreamingSnapshot {
  if (!isRecord(value) || !Array.isArray(value.buffers)) throw invalidStreamingResponse();
  const rootFrameId = exactIdentifier(value.root_frame_id);
  if (!rootFrameId) throw invalidStreamingResponse();
  const historyRevision = value.history_revision;
  if (historyRevision !== undefined && (!Number.isSafeInteger(historyRevision) || (historyRevision as number) < 0)) {
    throw invalidStreamingResponse();
  }
  const seenFrameIds = new Set<string>();
  const frames = value.buffers.map(normalizeFrame);
  for (const frame of frames) {
    if (seenFrameIds.has(frame.frameId)) throw invalidStreamingResponse();
    seenFrameIds.add(frame.frameId);
  }
  return {
    rootFrameId,
    ...(typeof historyRevision === 'number' ? { historyRevision } : {}),
    frames,
  };
}

function normalizeFrame(value: unknown): ConversationStreamingFrame {
  if (!isRecord(value)) throw invalidStreamingResponse();
  const frameId = exactIdentifier(value.frame_id);
  if (!frameId || !Array.isArray(value.tool_stdout)) throw invalidStreamingResponse();
  const seenToolIds = new Set<string>();
  const toolStdout = value.tool_stdout.map(normalizeToolOutput);
  for (const output of toolStdout) {
    const identity = output.execId ? `exec:${output.execId}` : `tool:${output.toolUseId}`;
    if (seenToolIds.has(identity)) throw invalidStreamingResponse();
    seenToolIds.add(identity);
  }
  return { frameId, toolStdout };
}

function normalizeToolOutput(value: unknown): ConversationToolStdout {
  if (!isRecord(value)) throw invalidStreamingResponse();
  const toolUseId = optionalIdentifier(value.tool_use_id);
  const execId = optionalIdentifier(value.exec_id);
  if (toolUseId === null || execId === null || (!toolUseId && !execId)) throw invalidStreamingResponse();
  if (value.stdout !== undefined && typeof value.stdout !== 'string') throw invalidStreamingResponse();
  if (value.stderr !== undefined && typeof value.stderr !== 'string') throw invalidStreamingResponse();
  if (
    (value.started_at !== undefined || value.status !== undefined) &&
    (!execId ||
      (value.started_at !== undefined && !validDate(value.started_at)) ||
      (value.status !== undefined && !['queued', 'running', 'persisting'].includes(String(value.status))))
  ) {
    throw invalidStreamingResponse();
  }
  if (
    !optionalNonnegativeInteger(value.through_chunk_sequence) ||
    !optionalNonnegativeInteger(value.stdout_start_byte) ||
    !optionalNonnegativeInteger(value.stdout_end_byte) ||
    (value.stdout_truncated !== undefined && typeof value.stdout_truncated !== 'boolean') ||
    (value.background !== undefined && typeof value.background !== 'boolean')
  ) {
    throw invalidStreamingResponse();
  }
  const stdoutStartByte = value.stdout_start_byte as number | undefined;
  const stdoutEndByte = value.stdout_end_byte as number | undefined;
  if (
    (stdoutStartByte === undefined) !== (stdoutEndByte === undefined) ||
    (stdoutStartByte !== undefined &&
      stdoutEndByte !== undefined &&
      (stdoutStartByte > stdoutEndByte ||
        stdoutEndByte - stdoutStartByte !== UTF8_ENCODER.encode(String(value.stdout ?? '')).byteLength))
  ) {
    throw invalidStreamingResponse();
  }
  return {
    toolUseId: toolUseId || execId || '',
    ...(execId ? { execId } : {}),
    ...(typeof value.tool_name === 'string' && value.tool_name.trim() ? { toolName: value.tool_name.trim() } : {}),
    ...(typeof value.started_at === 'string' ? { startedAt: value.started_at } : {}),
    ...(typeof value.background === 'boolean' ? { background: value.background } : {}),
    ...(typeof value.status === 'string' ? { status: value.status as ConversationToolStdout['status'] } : {}),
    stdout: typeof value.stdout === 'string' ? value.stdout : '',
    stderr: typeof value.stderr === 'string' ? value.stderr : '',
    ...(typeof value.through_chunk_sequence === 'number' ? { throughChunkSequence: value.through_chunk_sequence } : {}),
    ...(typeof stdoutStartByte === 'number' ? { stdoutStartByte } : {}),
    ...(typeof stdoutEndByte === 'number' ? { stdoutEndByte } : {}),
    ...(value.stdout_truncated === true ? { stdoutTruncated: true } : {}),
  };
}

function isRecord(value: unknown): value is RecordValue {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function exactIdentifier(value: unknown): string | undefined {
  if (typeof value !== 'string' || !value || value.trim() !== value) return undefined;
  for (const character of value) {
    const codePoint = character.codePointAt(0);
    if (codePoint !== undefined && (codePoint <= 31 || codePoint === 127)) return undefined;
  }
  return value;
}

function optionalIdentifier(value: unknown): string | null | undefined {
  if (value === undefined || value === '') return undefined;
  return exactIdentifier(value) ?? null;
}

function optionalNonnegativeInteger(value: unknown): boolean {
  return value === undefined || (Number.isSafeInteger(value) && Number(value) >= 0);
}

function validDate(value: unknown): value is string {
  return typeof value === 'string' && value.length <= 64 && Number.isFinite(Date.parse(value));
}

function invalidStreamingResponse(): Error {
  return new Error('Synon Biomed streaming response is invalid');
}
