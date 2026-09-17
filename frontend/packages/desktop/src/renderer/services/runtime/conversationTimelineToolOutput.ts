import type { TMessage } from '@/common/chat/chatLib';
import type { ConversationStreamingFrame, ConversationToolStdout } from './conversationStreamingModel';

/** Merge auxiliary stdout into the durable tool entity already published on message.stream. */
export function mergeConversationToolOutput(
  current: TMessage[],
  conversationId: string,
  frames: ConversationStreamingFrame[]
): TMessage[] {
  const next = current.filter((message) => message.conversation_id === conversationId);
  const candidatesByIdentity = indexToolCandidates(next);
  const selectedOutputByTool = new Map<number, ConversationToolStdout>();

  for (const frame of frames) {
    for (const output of frame.toolStdout) {
      const index = selectActiveToolIndex(next, candidatesByIdentity, output);
      if (index === null) continue;
      const selected = selectedOutputByTool.get(index);
      if (!selected || compareOutputRecency(output, selected) >= 0) selectedOutputByTool.set(index, output);
    }
  }
  for (const [index, output] of selectedOutputByTool) {
    const message = next[index];
    if (message.type !== 'tool_call' || isTerminalToolStatus(message.content.status)) continue;
    const liveOutput = mergeLiveOutput(message.content.output, message.content.streaming === true, output);
    if (!liveOutput && !output.recoveryError) continue;
    next[index] = {
      ...message,
      hidden: false,
      content: {
        ...message.content,
        ...(liveOutput ? { output: liveOutput, streaming: true } : {}),
        streamingRecoveryError: output.recoveryError === true,
      },
    };
  }
  return next;
}

function compareOutputRecency(left: ConversationToolStdout, right: ConversationToolStdout): number {
  const leftStartedAt = left.startedAt ? Date.parse(left.startedAt) : 0;
  const rightStartedAt = right.startedAt ? Date.parse(right.startedAt) : 0;
  if (leftStartedAt !== rightStartedAt) return leftStartedAt > rightStartedAt ? 1 : -1;
  const leftSequence = left.throughChunkSequence ?? 0;
  const rightSequence = right.throughChunkSequence ?? 0;
  if (leftSequence !== rightSequence) return leftSequence > rightSequence ? 1 : -1;
  return (left.execId ?? '').localeCompare(right.execId ?? '');
}

function indexToolCandidates(messages: TMessage[]): Map<string, number[]> {
  const index = new Map<string, number[]>();
  messages.forEach((message, messageIndex) => {
    if (message.type !== 'tool_call') return;
    for (const identity of [message.content.operation_id, message.content.call_id]) {
      const normalized = identity?.trim();
      if (!normalized) continue;
      const candidates = index.get(normalized) ?? [];
      candidates.push(messageIndex);
      index.set(normalized, candidates);
    }
  });
  return index;
}

function selectActiveToolIndex(
  messages: TMessage[],
  candidatesByIdentity: Map<string, number[]>,
  output: ConversationToolStdout
): number | null {
  const candidates = [
    ...new Set([output.toolUseId, output.execId].flatMap((id) => candidatesByIdentity.get(id ?? '') ?? [])),
  ]
    .filter((index) => messages[index]?.type === 'tool_call')
    .filter((index) => {
      const message = messages[index];
      return message.type === 'tool_call' && !isTerminalToolStatus(message.content.status);
    });
  if (candidates.length === 0) return null;
  return candidates.toSorted((left, right) => compareToolRecency(messages[right], messages[left]))[0];
}

function compareToolRecency(left: TMessage, right: TMessage): number {
  if (left.type !== 'tool_call') return right.type === 'tool_call' ? -1 : 0;
  if (right.type !== 'tool_call') return 1;
  const leftAttempt = Number.isSafeInteger(left.content.attempt) ? Number(left.content.attempt) : 0;
  const rightAttempt = Number.isSafeInteger(right.content.attempt) ? Number(right.content.attempt) : 0;
  if (leftAttempt !== rightAttempt) return leftAttempt > rightAttempt ? 1 : -1;
  const leftRevision = Number.isSafeInteger(left.content.revision) ? Number(left.content.revision) : 0;
  const rightRevision = Number.isSafeInteger(right.content.revision) ? Number(right.content.revision) : 0;
  if (leftRevision !== rightRevision) return leftRevision > rightRevision ? 1 : -1;
  const leftCreatedAt = left.created_at ?? 0;
  const rightCreatedAt = right.created_at ?? 0;
  return leftCreatedAt === rightCreatedAt ? 0 : leftCreatedAt > rightCreatedAt ? 1 : -1;
}

function mergeLiveOutput(
  existing: string | undefined,
  existingIsLive: boolean,
  output: ConversationToolStdout
): string | null {
  const stdout = output.stdout;
  const stderr = output.stderr;
  if (!stdout && !stderr) return existingIsLive ? (existing ?? null) : null;
  if (!existing || existingIsLive) return [stdout, stderr].filter(Boolean).join('\n');
  try {
    const parsed = JSON.parse(existing) as unknown;
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return JSON.stringify({
        ...(parsed as Record<string, unknown>),
        ...(stdout ? { stdout } : {}),
        ...(stderr ? { stderr } : {}),
      });
    }
  } catch {
    // A running plain-text output is superseded by the authoritative stdout snapshot.
  }
  return [stdout, stderr].filter(Boolean).join('\n');
}

function isTerminalToolStatus(status: string | undefined): boolean {
  return (
    status === 'completed' ||
    status === 'error' ||
    status === 'canceled' ||
    status === 'interrupted' ||
    status === 'unknown'
  );
}
