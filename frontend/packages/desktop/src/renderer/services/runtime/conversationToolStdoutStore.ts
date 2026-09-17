import type { IToolStdoutChunkEvent } from '@/common/adapter/ipcBridge';
import type { ConversationStreamingFrame } from './conversationStreamingModel';

export const MAX_TOOL_STDOUT_BYTES = 256 * 1024;
export const MAX_TOOL_STDOUT_ENTRIES = 64;
export const MAX_PENDING_TOOL_STDOUT_CHUNKS = 256;
const MAX_GAP_HYDRATION_RETRIES = 3;
const GAP_HYDRATION_RETRY_BASE_MS = 25;

type ToolOutput = ConversationStreamingFrame['toolStdout'][number];

type StoredOutput = ToolOutput & {
  rootFrameId: string;
  frameId: string;
  sequence: number;
  pendingChunks: Map<number, PendingChunk>;
  gapHydrationRequested: boolean;
  observedVersion: number;
};

type PendingChunk = {
  text: string;
  startByte?: number;
  endByte?: number;
};

type RootSubscription = {
  count: number;
  listeners: Set<() => void>;
  disposeChunk: () => void;
  disposeReconnect: () => void;
  hydration?: AbortController;
  gapRetryCount: number;
  gapRetryTimer?: ReturnType<typeof setTimeout>;
};

export type ToolStdoutStoreDependencies = {
  subscribeChunks: (listener: (event: IToolStdoutChunkEvent) => void) => () => void;
  subscribeReconnect: (listener: () => void) => () => void;
  hydrate: (rootFrameId: string, signal: AbortSignal) => Promise<ConversationStreamingFrame[]>;
  schedulePublish?: (callback: () => void) => unknown;
  cancelScheduledPublish?: (handle: unknown) => void;
};

const EMPTY_SNAPSHOT: ConversationStreamingFrame[] = [];
const UTF8_ENCODER = new TextEncoder();
const UTF8_DECODER = new TextDecoder();

const utf8Size = (value: string): number => UTF8_ENCODER.encode(value).byteLength;

function dropUtf8Prefix(value: string, minimumBytes: number, totalBytes: number): { text: string; bytes: number } {
  let removedBytes = 0;
  let characterOffset = 0;
  while (characterOffset < value.length && removedBytes < minimumBytes) {
    const codePoint = value.codePointAt(characterOffset) ?? 0;
    removedBytes += codePoint <= 0x7f ? 1 : codePoint <= 0x7ff ? 2 : codePoint <= 0xffff ? 3 : 4;
    characterOffset += codePoint > 0xffff ? 2 : 1;
  }
  return { text: value.slice(characterOffset), bytes: Math.max(0, totalBytes - removedBytes) };
}

const exactRuntimeIdentifier = (value: unknown): string | null => {
  if (typeof value !== 'string' || value.length === 0 || value.length > 256 || value.trim() !== value) return null;
  for (const character of value) {
    if (character <= '\u0020' || character === '\u007f') return null;
  }
  return value;
};

const optionalRuntimeIdentifier = (value: unknown): string | null | undefined => {
  if (value === undefined || value === '') return undefined;
  return exactRuntimeIdentifier(value);
};

const validOptionalToolName = (value: unknown): value is string | undefined =>
  value === undefined ||
  (typeof value === 'string' &&
    value.length <= 256 &&
    [...value].every((character) => character >= '\u0020' && character !== '\u007f'));

const validExecStartedAt = (value: unknown): value is string =>
  typeof value === 'string' && value.length > 0 && value.length <= 64 && Number.isFinite(Date.parse(value));

const validActiveExecStatus = (value: unknown): value is NonNullable<ToolOutput['status']> =>
  value === 'queued' || value === 'running' || value === 'persisting';

function boundedUtf8Tail(value: string): { text: string; bytes: number; sourceBytes: number; truncated: boolean } {
  const encoded = UTF8_ENCODER.encode(value);
  if (encoded.byteLength <= MAX_TOOL_STDOUT_BYTES) {
    return { text: value, bytes: encoded.byteLength, sourceBytes: encoded.byteLength, truncated: false };
  }
  let start = encoded.byteLength - MAX_TOOL_STDOUT_BYTES;
  while (start < encoded.byteLength && (encoded[start] & 0xc0) === 0x80) start += 1;
  const text = UTF8_DECODER.decode(encoded.subarray(start));
  return { text, bytes: encoded.byteLength - start, sourceBytes: encoded.byteLength, truncated: true };
}

export class ConversationToolStdoutStore {
  private readonly entries = new Map<string, StoredOutput>();
  private readonly roots = new Map<string, RootSubscription>();
  private readonly frameRoots = new Map<string, string>();
  private readonly snapshots = new Map<string, ConversationStreamingFrame[]>();
  private readonly pendingPublishRoots = new Set<string>();
  private scheduledPublishHandle: unknown;
  private observedVersion = 0;

  constructor(private readonly dependencies: ToolStdoutStoreDependencies) {}

  subscribe(rootFrameId: string, listener: () => void): () => void {
    const root = rootFrameId.trim();
    if (!root) return () => undefined;
    let subscription = this.roots.get(root);
    if (!subscription) {
      subscription = {
        count: 0,
        listeners: new Set(),
        disposeChunk: this.dependencies.subscribeChunks((event) => this.onChunk(root, event)),
        disposeReconnect: this.dependencies.subscribeReconnect(() => this.hydrate(root, true)),
        gapRetryCount: 0,
      };
      this.roots.set(root, subscription);
      this.frameRoots.set(root, root);
      this.hydrate(root, true);
    }
    subscription.count += 1;
    subscription.listeners.add(listener);
    return () => {
      const current = this.roots.get(root);
      if (!current) return;
      current.listeners.delete(listener);
      current.count -= 1;
      if (current.count > 0) return;
      current.hydration?.abort();
      if (current.gapRetryTimer) clearTimeout(current.gapRetryTimer);
      current.disposeChunk();
      current.disposeReconnect();
      this.roots.delete(root);
      this.clearRoot(root);
      this.pendingPublishRoots.delete(root);
      if (this.roots.size === 0 && this.scheduledPublishHandle !== undefined) {
        this.dependencies.cancelScheduledPublish?.(this.scheduledPublishHandle);
        this.scheduledPublishHandle = undefined;
        this.pendingPublishRoots.clear();
      }
    };
  }

  getSnapshot(rootFrameId: string): ConversationStreamingFrame[] {
    return this.snapshots.get(rootFrameId) ?? EMPTY_SNAPSHOT;
  }

  /** Read the latest accepted chunks at a durable lifecycle boundary, before the next paint is published. */
  getCurrentSnapshot(rootFrameId: string): ConversationStreamingFrame[] {
    return this.buildSnapshot(rootFrameId);
  }

  clearCachedData(): void {
    const affectedRoots = new Set([...this.snapshots.keys(), ...this.roots.keys()]);
    for (const subscription of this.roots.values()) {
      subscription.hydration?.abort('account_scope_changed');
      if (subscription.gapRetryTimer) clearTimeout(subscription.gapRetryTimer);
      subscription.gapRetryTimer = undefined;
      subscription.gapRetryCount = 0;
    }
    if (this.scheduledPublishHandle !== undefined) {
      this.dependencies.cancelScheduledPublish?.(this.scheduledPublishHandle);
      this.scheduledPublishHandle = undefined;
    }
    this.pendingPublishRoots.clear();
    this.entries.clear();
    this.snapshots.clear();
    this.frameRoots.clear();
    for (const rootFrameId of this.roots.keys()) this.frameRoots.set(rootFrameId, rootFrameId);
    for (const rootFrameId of affectedRoots) {
      for (const listener of this.roots.get(rootFrameId)?.listeners ?? []) listener();
    }
  }

  private onChunk(rootFrameId: string, event: IToolStdoutChunkEvent): void {
    const frameId = exactRuntimeIdentifier(event.frame_id);
    const eventRootFrameId = exactRuntimeIdentifier(event.root_frame_id);
    if (!frameId || !eventRootFrameId || eventRootFrameId !== rootFrameId) return;
    const knownRootFrameId = this.frameRoots.get(frameId);
    if (knownRootFrameId && knownRootFrameId !== rootFrameId) return;
    const parsedToolUseId = optionalRuntimeIdentifier(event.tool_use_id);
    const parsedExecId = optionalRuntimeIdentifier(event.exec_id);
    if (parsedToolUseId === null || parsedExecId === null) {
      this.requestGapHydration(rootFrameId);
      return;
    }
    const toolUseId = parsedToolUseId ?? parsedExecId;
    const execId = parsedExecId;
    if (!toolUseId || typeof event.chunk !== 'string' || !event.chunk) return;
    const key = this.key(rootFrameId, frameId, toolUseId, execId);
    const previous = this.entries.get(key);
    if (
      (event.started_at !== undefined && (!execId || !validExecStartedAt(event.started_at))) ||
      (event.background !== undefined && typeof event.background !== 'boolean') ||
      (event.status !== undefined && (!execId || !validActiveExecStatus(event.status))) ||
      !validOptionalToolName(event.tool_name) ||
      (event.chunk_sequence !== undefined &&
        (!Number.isSafeInteger(event.chunk_sequence) || Number(event.chunk_sequence) < 1)) ||
      (event.chunk_start_byte !== undefined &&
        (!Number.isSafeInteger(event.chunk_start_byte) || Number(event.chunk_start_byte) < 0)) ||
      (event.chunk_end_byte !== undefined &&
        (!Number.isSafeInteger(event.chunk_end_byte) || Number(event.chunk_end_byte) < 0)) ||
      (event.chunk_sequence === undefined &&
        (event.chunk_start_byte !== undefined || event.chunk_end_byte !== undefined))
    ) {
      if (previous) previous.gapHydrationRequested = true;
      this.requestGapHydration(rootFrameId);
      return;
    }
    this.frameRoots.set(frameId, rootFrameId);
    const sequence = event.chunk_sequence === undefined ? 0 : Number(event.chunk_sequence);
    if (sequence > 0 && previous && sequence <= previous.sequence) return;
    const startByte = event.chunk_start_byte === undefined ? undefined : Number(event.chunk_start_byte);
    const endByte = event.chunk_end_byte === undefined ? undefined : Number(event.chunk_end_byte);
    if (
      (startByte === undefined) !== (endByte === undefined) ||
      (startByte !== undefined &&
        endByte !== undefined &&
        (startByte < 0 || endByte < startByte || endByte - startByte !== utf8Size(event.chunk)))
    ) {
      if (previous) previous.gapHydrationRequested = true;
      this.requestGapHydration(rootFrameId);
      return;
    }
    const pendingChunks = new Map(previous?.pendingChunks ?? []);
    if (sequence > 0 && pendingChunks.has(sequence)) return;
    const startedAt = event.started_at ?? previous?.startedAt;
    const background = event.background ?? previous?.background;
    const status = event.status ?? previous?.status;
    const next: StoredOutput = {
      rootFrameId,
      frameId,
      toolUseId,
      ...(execId ? { execId } : previous?.execId ? { execId: previous.execId } : {}),
      ...(event.tool_name?.trim()
        ? { toolName: event.tool_name.trim() }
        : previous?.toolName
          ? { toolName: previous.toolName }
          : {}),
      ...(startedAt ? { startedAt } : {}),
      ...(background !== undefined ? { background } : {}),
      ...(status ? { status } : {}),
      stdout: previous?.stdout ?? '',
      stderr: previous?.stderr ?? '',
      sequence: previous?.sequence ?? 0,
      pendingChunks,
      gapHydrationRequested: previous?.gapHydrationRequested ?? false,
      observedVersion: ++this.observedVersion,
      ...(previous?.stdoutStartByte !== undefined ? { stdoutStartByte: previous.stdoutStartByte } : {}),
      ...(previous?.stdoutEndByte !== undefined ? { stdoutEndByte: previous.stdoutEndByte } : {}),
      ...(previous?.stdoutTruncated ? { stdoutTruncated: true } : {}),
      ...(previous?.recoveryError ? { recoveryError: true } : {}),
    };

    if (sequence > 0) {
      pendingChunks.set(sequence, {
        text: event.chunk,
        ...(startByte === undefined ? {} : { startByte }),
        ...(endByte === undefined ? {} : { endByte }),
      });
      if (pendingChunks.size > MAX_PENDING_TOOL_STDOUT_CHUNKS) {
        pendingChunks.clear();
        next.gapHydrationRequested = true;
        this.entries.set(key, next);
        this.requestGapHydration(rootFrameId);
        return;
      }
      this.drainPendingChunks(next);
    } else {
      this.appendChunk(next, { text: event.chunk });
    }
    this.entries.delete(key);
    this.entries.set(key, next);
    this.evictOverflow();
    let recoveryChanged = false;
    if (next.pendingChunks.size > 0) {
      next.gapHydrationRequested = true;
      this.requestGapHydration(rootFrameId);
    } else {
      recoveryChanged = this.resetGapRecovery(rootFrameId);
    }
    if (sequence === 0 || next.sequence > (previous?.sequence ?? 0) || recoveryChanged) {
      this.requestPublish(rootFrameId);
    }
  }

  private hydrate(rootFrameId: string, resetRetries = false): void {
    const subscription = this.roots.get(rootFrameId);
    if (!subscription) return;
    if (resetRetries) {
      subscription.gapRetryCount = 0;
      if (subscription.gapRetryTimer) clearTimeout(subscription.gapRetryTimer);
      subscription.gapRetryTimer = undefined;
    }
    subscription.hydration?.abort();
    const controller = new AbortController();
    const versionAtStart = this.observedVersion;
    let retryGapAfterHydration = false;
    subscription.hydration = controller;
    void this.dependencies
      .hydrate(rootFrameId, controller.signal)
      .then((buffers) => {
        if (controller.signal.aborted) return;
        for (const buffer of buffers) {
          const knownRootFrameId = this.frameRoots.get(buffer.frameId);
          if (knownRootFrameId && knownRootFrameId !== rootFrameId) {
            throw new Error('streaming snapshot changed frame root ownership');
          }
        }
        const seen = new Set<string>();
        for (const buffer of buffers) {
          this.frameRoots.set(buffer.frameId, rootFrameId);
          for (const output of buffer.toolStdout) {
            const key = this.key(rootFrameId, buffer.frameId, output.toolUseId, output.execId);
            seen.add(key);
            const previous = this.entries.get(key);
            const snapshotSequence = output.throughChunkSequence ?? 0;
            if (
              previous &&
              previous.observedVersion > versionAtStart &&
              (snapshotSequence === 0 || previous.sequence >= snapshotSequence)
            ) {
              continue;
            }
            if (previous?.sequence && snapshotSequence === 0) continue;
            if (previous && snapshotSequence > 0 && previous.sequence > snapshotSequence) continue;
            const bounded = boundedUtf8Tail(output.stdout);
            const endByte = output.stdoutEndByte ?? utf8Size(output.stdout);
            const pendingChunks = new Map(previous?.pendingChunks ?? []);
            for (const pendingSequence of pendingChunks.keys()) {
              if (pendingSequence <= snapshotSequence) pendingChunks.delete(pendingSequence);
            }
            const next: StoredOutput = {
              rootFrameId,
              frameId: buffer.frameId,
              toolUseId: output.toolUseId,
              ...(output.execId ? { execId: output.execId } : {}),
              ...(output.toolName ? { toolName: output.toolName } : {}),
              ...(output.startedAt ? { startedAt: output.startedAt } : {}),
              ...(output.background !== undefined ? { background: output.background } : {}),
              ...(output.status ? { status: output.status } : {}),
              stdout: bounded.text,
              stderr: boundedUtf8Tail(output.stderr).text,
              sequence: snapshotSequence,
              pendingChunks,
              gapHydrationRequested: false,
              observedVersion: ++this.observedVersion,
              stdoutStartByte:
                bounded.truncated || output.stdoutStartByte === undefined
                  ? Math.max(0, endByte - bounded.bytes)
                  : output.stdoutStartByte,
              stdoutEndByte: endByte,
              ...(output.stdoutTruncated || bounded.truncated ? { stdoutTruncated: true } : {}),
            };
            this.drainPendingChunks(next);
            this.entries.delete(key);
            this.entries.set(key, next);
          }
        }
        for (const [key, output] of this.entries) {
          if (output.rootFrameId === rootFrameId && output.observedVersion <= versionAtStart && !seen.has(key)) {
            this.entries.delete(key);
          }
        }
        this.evictOverflow();
        if (this.hasPendingGap(rootFrameId)) {
          retryGapAfterHydration = true;
        } else {
          this.resetGapRecovery(rootFrameId);
        }
        this.requestPublish(rootFrameId);
      })
      .catch(() => {
        if (controller.signal.aborted) return;
        let changed = false;
        for (const output of this.entries.values()) {
          if (output.rootFrameId !== rootFrameId) continue;
          output.gapHydrationRequested = false;
          if (!output.recoveryError) {
            output.recoveryError = true;
            output.observedVersion = ++this.observedVersion;
            changed = true;
          }
        }
        if (changed) this.requestPublish(rootFrameId);
        retryGapAfterHydration = this.hasPendingGap(rootFrameId);
      })
      .finally(() => {
        const current = this.roots.get(rootFrameId);
        if (current?.hydration === controller) {
          current.hydration = undefined;
          if (retryGapAfterHydration || this.hasPendingGap(rootFrameId)) this.scheduleGapHydration(rootFrameId);
        }
      });
  }

  private evictOverflow(): void {
    while (this.entries.size > MAX_TOOL_STDOUT_ENTRIES) {
      const oldest = this.entries.keys().next().value;
      if (!oldest) break;
      const evictedRoot = this.entries.get(oldest)?.rootFrameId;
      this.entries.delete(oldest);
      if (evictedRoot) this.requestPublish(evictedRoot);
    }
  }

  private drainPendingChunks(output: StoredOutput): void {
    for (;;) {
      const sequence = output.sequence + 1;
      const chunk = output.pendingChunks.get(sequence);
      if (!chunk) break;
      const expectedStart = output.stdoutEndByte ?? 0;
      if (chunk.startByte !== undefined && chunk.startByte !== expectedStart) break;
      output.pendingChunks.delete(sequence);
      this.appendChunk(output, chunk);
      output.sequence = sequence;
    }
    if (output.pendingChunks.size === 0) {
      output.gapHydrationRequested = false;
      delete output.recoveryError;
    }
  }

  private hasPendingGap(rootFrameId: string): boolean {
    for (const output of this.entries.values()) {
      if (output.rootFrameId === rootFrameId && (output.pendingChunks.size > 0 || output.gapHydrationRequested)) {
        return true;
      }
    }
    return false;
  }

  private requestGapHydration(rootFrameId: string): void {
    const subscription = this.roots.get(rootFrameId);
    if (!subscription || subscription.hydration || subscription.gapRetryTimer) return;
    this.hydrate(rootFrameId);
  }

  private scheduleGapHydration(rootFrameId: string): void {
    const subscription = this.roots.get(rootFrameId);
    if (!subscription || subscription.hydration || subscription.gapRetryTimer) return;
    if (subscription.gapRetryCount >= MAX_GAP_HYDRATION_RETRIES) {
      let changed = false;
      for (const output of this.entries.values()) {
        if (
          output.rootFrameId !== rootFrameId ||
          (!output.gapHydrationRequested && output.pendingChunks.size === 0) ||
          output.recoveryError
        )
          continue;
        output.recoveryError = true;
        output.gapHydrationRequested = false;
        output.observedVersion = ++this.observedVersion;
        changed = true;
      }
      if (changed) this.requestPublish(rootFrameId);
      return;
    }
    const delay = GAP_HYDRATION_RETRY_BASE_MS * 2 ** subscription.gapRetryCount;
    subscription.gapRetryCount += 1;
    subscription.gapRetryTimer = setTimeout(() => {
      const current = this.roots.get(rootFrameId);
      if (!current) return;
      current.gapRetryTimer = undefined;
      if (this.hasPendingGap(rootFrameId)) this.hydrate(rootFrameId);
    }, delay);
  }

  private resetGapRecovery(rootFrameId: string): boolean {
    const subscription = this.roots.get(rootFrameId);
    if (subscription) {
      if (subscription.gapRetryTimer) clearTimeout(subscription.gapRetryTimer);
      subscription.gapRetryTimer = undefined;
      subscription.gapRetryCount = 0;
    }
    let changed = false;
    for (const output of this.entries.values()) {
      if (output.rootFrameId !== rootFrameId || output.pendingChunks.size > 0) continue;
      output.gapHydrationRequested = false;
      if (output.recoveryError) {
        delete output.recoveryError;
        output.observedVersion = ++this.observedVersion;
        changed = true;
      }
    }
    return changed;
  }

  private appendChunk(output: StoredOutput, chunk: PendingChunk): void {
    const incoming = boundedUtf8Tail(chunk.text);
    const fallbackStart = output.stdoutEndByte ?? 0;
    const endByte = chunk.endByte ?? fallbackStart + incoming.sourceBytes;
    const currentBytes =
      output.stdoutStartByte !== undefined && output.stdoutEndByte !== undefined
        ? output.stdoutEndByte - output.stdoutStartByte
        : utf8Size(output.stdout);
    let combinedBytes = incoming.bytes;
    if (incoming.truncated) {
      output.stdout = incoming.text;
    } else {
      const overflow = Math.max(0, currentBytes + incoming.bytes - MAX_TOOL_STDOUT_BYTES);
      const retained = overflow > 0 ? dropUtf8Prefix(output.stdout, overflow, currentBytes) : null;
      output.stdout = `${retained?.text ?? output.stdout}${chunk.text}`;
      combinedBytes += retained?.bytes ?? currentBytes;
    }
    output.stdoutEndByte = endByte;
    output.stdoutStartByte = Math.max(0, endByte - combinedBytes);
    if (incoming.truncated || output.stdoutStartByte > 0) output.stdoutTruncated = true;
  }

  private requestPublish(rootFrameId: string): void {
    const schedule = this.dependencies.schedulePublish;
    if (!schedule) {
      this.publish(rootFrameId);
      return;
    }
    this.pendingPublishRoots.add(rootFrameId);
    if (this.scheduledPublishHandle !== undefined) return;
    this.scheduledPublishHandle = schedule(() => {
      this.scheduledPublishHandle = undefined;
      const roots = [...this.pendingPublishRoots];
      this.pendingPublishRoots.clear();
      for (const root of roots) {
        if (this.roots.has(root)) this.publish(root);
      }
    });
  }

  private publish(rootFrameId: string): void {
    const snapshot = this.buildSnapshot(rootFrameId);
    this.snapshots.set(rootFrameId, snapshot);
    for (const listener of this.roots.get(rootFrameId)?.listeners ?? []) listener();
  }

  private buildSnapshot(rootFrameId: string): ConversationStreamingFrame[] {
    const byFrame = new Map<string, ToolOutput[]>();
    for (const output of this.entries.values()) {
      if (output.rootFrameId !== rootFrameId) continue;
      const outputs = byFrame.get(output.frameId) ?? [];
      outputs.push({
        toolUseId: output.toolUseId,
        ...(output.execId ? { execId: output.execId } : {}),
        ...(output.toolName ? { toolName: output.toolName } : {}),
        ...(output.startedAt ? { startedAt: output.startedAt } : {}),
        ...(output.background !== undefined ? { background: output.background } : {}),
        ...(output.status ? { status: output.status } : {}),
        stdout: output.stdout,
        stderr: output.stderr,
        ...(output.sequence > 0 ? { throughChunkSequence: output.sequence } : {}),
        ...(output.stdoutStartByte !== undefined ? { stdoutStartByte: output.stdoutStartByte } : {}),
        ...(output.stdoutEndByte !== undefined ? { stdoutEndByte: output.stdoutEndByte } : {}),
        ...(output.stdoutTruncated ? { stdoutTruncated: true } : {}),
        ...(output.recoveryError ? { recoveryError: true } : {}),
      });
      byFrame.set(output.frameId, outputs);
    }
    return [...byFrame].map(([frameId, toolStdout]) => ({ frameId, toolStdout }));
  }

  private clearRoot(rootFrameId: string): void {
    for (const [key, output] of this.entries) {
      if (output.rootFrameId === rootFrameId) this.entries.delete(key);
    }
    this.snapshots.delete(rootFrameId);
    for (const [frameId, ownerRootFrameId] of this.frameRoots) {
      if (ownerRootFrameId === rootFrameId) this.frameRoots.delete(frameId);
    }
  }

  private key(rootFrameId: string, frameId: string, toolUseId: string, execId?: string): string {
    return `${rootFrameId}\0${frameId}\0${execId || `tool:${toolUseId}`}`;
  }
}
