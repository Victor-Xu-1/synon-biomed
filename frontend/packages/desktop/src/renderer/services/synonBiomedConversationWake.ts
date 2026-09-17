import { ipcBridge } from '@/common';
import type { IResponseMessage } from '@/common/adapter/messageStreamProtocol';

export type SynonBiomedConversationWakeOptions = {
  includeRelatedConversations?: boolean;
};

export type SynonBiomedConversationWakeKind = 'stream' | 'reconcile';

export type SynonBiomedConversationWakeSignal = {
  kind: SynonBiomedConversationWakeKind;
  /**
   * Rebuild the durable message window while a turn is still running.
   *
   * Direct Transcript publications remain the sole live-text authority. This
   * flag is reserved for boundaries that can add non-text cards (tools) or
   * where a reconnect/rebase may have missed publications.
   */
  refreshHistory?: boolean;
  /** The durable runtime/turn publication has reached a terminal boundary. */
  terminalBoundary?: boolean;
};

const getWakeSignalPriority = (signal: SynonBiomedConversationWakeSignal): number =>
  signal.refreshHistory === true ? 3 : signal.terminalBoundary === true ? 2 : signal.kind === 'reconcile' ? 1 : 0;

type PublicationBoundaryCarrier = {
  source_publication_sequence?: unknown;
  publication_boundary_id?: unknown;
};

const readPublicationBoundary = (event: PublicationBoundaryCarrier): string | undefined =>
  Number.isSafeInteger(event.source_publication_sequence) &&
  Number(event.source_publication_sequence) >= 1 &&
  typeof event.publication_boundary_id === 'string' &&
  event.publication_boundary_id.trim() === event.publication_boundary_id &&
  event.publication_boundary_id.length > 0
    ? event.publication_boundary_id
    : undefined;

type FrameScopedWakeEvent = {
  conversation_id?: unknown;
  session_id?: unknown;
  scope?: { id?: unknown };
  root_frame_id?: unknown;
  frame_id?: unknown;
};

/**
 * Converts the durable realtime authority into a coalescible runtime/history
 * wake. Text stays on the direct Transcript WebSocket lane; only user-visible
 * boundaries reconcile the indexed durable projection.
 */
export const subscribeSynonBiomedConversationWake = (
  conversationId: string,
  wake: (signal?: SynonBiomedConversationWakeSignal) => void,
  options: SynonBiomedConversationWakeOptions = {}
): (() => void) => {
  const exact = (candidate: string): boolean => candidate === conversationId;
  const related = (event: FrameScopedWakeEvent): boolean => {
    const direct = [event.conversation_id, event.session_id, event.scope?.id].some(
      (candidate) => typeof candidate === 'string' && exact(candidate)
    );
    const frame = [event.root_frame_id, event.frame_id].some(
      (candidate) => typeof candidate === 'string' && exact(candidate)
    );
    return direct || (options.includeRelatedConversations === true && frame);
  };
  let disposed = false;
  let scheduled = false;
  let pendingSignal: SynonBiomedConversationWakeSignal | null = null;
  const publicationPriorities = new Map<string, number>();
  const scheduleWake = (signal: SynonBiomedConversationWakeSignal, publicationBoundary?: string): void => {
    if (disposed) return;
    if (publicationBoundary) {
      const priority = getWakeSignalPriority(signal);
      const deliveredPriority = publicationPriorities.get(publicationBoundary);
      // One durable publication is often surfaced by response, runtime and
      // turn channels. Ignore equal/weaker duplicates, but allow a later
      // runtime/turn signal to promote an earlier lightweight response wake.
      if (deliveredPriority !== undefined && deliveredPriority >= priority) return;
      publicationPriorities.set(publicationBoundary, priority);
      // Long-running tasks must not retain an unbounded set of boundary IDs.
      if (publicationPriorities.size > 64) {
        const oldest = publicationPriorities.keys().next().value;
        if (oldest !== undefined) publicationPriorities.delete(oldest);
      }
    }
    if (pendingSignal === null || getWakeSignalPriority(signal) > getWakeSignalPriority(pendingSignal)) {
      pendingSignal = signal;
    }
    if (scheduled) return;
    scheduled = true;
    queueMicrotask(() => {
      scheduled = false;
      if (disposed || pendingSignal === null) return;
      const nextSignal = pendingSignal;
      pendingSignal = null;
      wake(nextSignal);
    });
  };
  const disposers = [
    ipcBridge.acpConversation.responseStream.on((event: IResponseMessage) => {
      // Text deltas already update the bounded live message projection. Waking
      // the canonical runtime/streaming read models for every token turned one
      // provider stream into thousands of duplicate HTTP+SQLite reads. Start
      // and terminal boundaries still wake the lightweight runtime authority.
      if (!exact(event.conversation_id) || event.type === 'text') return;
      // A response finish/error closes the direct text lane, so an authority
      // read is now safe. The later runtime/turn terminal publication has a
      // higher priority and may promote the same durable boundary if this
      // first read still reports processing.
      scheduleWake(
        event.type === 'finish' || event.type === 'error' ? { kind: 'reconcile' } : { kind: 'stream' },
        readPublicationBoundary(event)
      );
    }),
    ipcBridge.conversation.userCreated.on((event) => {
      if (exact(event.conversation_id)) scheduleWake({ kind: 'stream' });
    }),
    ipcBridge.conversation.historyRebased.on((event) => {
      if (exact(event.conversation_id)) scheduleWake({ kind: 'reconcile', refreshHistory: true });
    }),
    ipcBridge.conversation.turnCompleted.on((event) => {
      if (related(event)) scheduleWake({ kind: 'reconcile', terminalBoundary: true }, readPublicationBoundary(event));
    }),
    ipcBridge.conversation.listChanged.on((event) => {
      if (related(event)) {
        scheduleWake({ kind: 'stream' });
      }
    }),
    ipcBridge.runtime.statusChanged.on((event) => {
      if (event.scope.kind === 'conversation' && related(event)) {
        // Tool lifecycle updates arrive on the same durable message.stream
        // publication. The runtime copy is only a lightweight state wake; full
        // history is reserved for explicit rebase/reconnect recovery.
        const isToolBoundary = event.boundary_kind === 'tool';
        const isTerminal = event.terminal_status !== undefined || event.phase === 'ready' || event.phase === 'failed';
        scheduleWake(
          isToolBoundary
            ? { kind: 'stream' }
            : isTerminal
              ? { kind: 'reconcile', terminalBoundary: true }
              : { kind: 'stream' },
          readPublicationBoundary(event)
        );
      }
    }),
    ipcBridge.kernel.executionCellUpdate.on((event) => {
      if (event.root_frame_id === conversationId || event.frame_id === conversationId) scheduleWake({ kind: 'stream' });
    }),
    ipcBridge.realtime.reconnected.on(() => scheduleWake({ kind: 'reconcile', refreshHistory: true })),
  ];
  return () => {
    disposed = true;
    pendingSignal = null;
    publicationPriorities.clear();
    disposers.forEach((dispose) => dispose());
  };
};
