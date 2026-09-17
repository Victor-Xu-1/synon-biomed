import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ipcBridge } from '@/common';
import type { NormalizedToolCall, ToolMessage } from '@/common/chat/normalizeToolCall';
import { normalizeToolMessages } from '@/common/chat/normalizeToolCall';
import { withTransientHistoryRetry } from '@/renderer/services/synonBiomedHistoryRetry';
import { getSelectedSynonBiomedBranch } from '@/renderer/services/synonBiomedConversationBranches';
import { getRendererAccountScopeToken } from '@/renderer/services/rendererAccountScope';
import { mergePublicToolOutputEvidence } from '../toolSummaryGroupingModel';

const MAX_HYDRATED_TOOL_OUTPUT_BYTES = 8 * 1024 * 1024;
const LARGE_TOOL_RESULT_CONTENT_URL = /^\/api\/artifacts\/large-tool-result-[a-f0-9]+\/versions\/ltr-[a-f0-9-]+$/iu;

function largeToolResultContentUrl(output: string | undefined): string | null {
  if (!output) return null;
  try {
    const envelope = JSON.parse(output) as { content_url?: unknown; truncated?: unknown };
    const contentUrl = typeof envelope.content_url === 'string' ? envelope.content_url.trim() : '';
    return envelope.truncated === true && LARGE_TOOL_RESULT_CONTENT_URL.test(contentUrl) ? contentUrl : null;
  } catch {
    return null;
  }
}

type LargeToolResultHydration = { output: string | null; remote: boolean; contentUrl?: string };

async function hydrateLargeToolResultOutput(
  output: string | undefined,
  signal: AbortSignal
): Promise<LargeToolResultHydration> {
  const contentUrl = largeToolResultContentUrl(output);
  if (!contentUrl) return { output: null, remote: false };
  const head = await fetch(contentUrl, { method: 'HEAD', credentials: 'include', signal });
  if (!head.ok) throw new Error(`large tool result metadata request failed with status ${head.status}`);
  const rawSize = head.headers.get('content-length');
  const declaredSize = rawSize === null ? 0 : Number(rawSize);
  if (!Number.isSafeInteger(declaredSize) || declaredSize < 0) {
    throw new Error('large tool result metadata returned an invalid content length');
  }
  if (declaredSize > MAX_HYDRATED_TOOL_OUTPUT_BYTES) return { output: null, remote: true, contentUrl };
  const response = await fetch(contentUrl, { credentials: 'include', signal });
  if (!response.ok) throw new Error(`large tool result request failed with status ${response.status}`);
  const hydrated = await readBoundedResponseText(response, MAX_HYDRATED_TOOL_OUTPUT_BYTES);
  return hydrated === null ? { output: null, remote: true, contentUrl } : { output: hydrated, remote: false };
}

async function readBoundedResponseText(response: Response, maximumBytes: number): Promise<string | null> {
  if (!response.body) {
    const text = await response.text();
    return new TextEncoder().encode(text).byteLength <= maximumBytes ? text : null;
  }
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    // oxlint-disable-next-line no-await-in-loop -- ReadableStream chunks must be consumed serially.
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > maximumBytes) {
      // oxlint-disable-next-line no-await-in-loop -- Cancel the same serial reader before returning.
      await reader.cancel();
      return null;
    }
    chunks.push(value);
  }
  const merged = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) {
    merged.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder().decode(merged);
}

export function useToolOperationDetail(item: NormalizedToolCall, shouldLoadFull: boolean) {
  const [hydrated, setHydrated] = useState<{ scope: string; item: NormalizedToolCall } | null>(null);
  const [loadingFull, setLoadingFull] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const generationRef = useRef(0);
  const requestRef = useRef<AbortController | null>(null);
  const branchId = item.branchId ?? (item.conversationId ? getSelectedSynonBiomedBranch(item.conversationId) : null);
  const scope = JSON.stringify([
    getRendererAccountScopeToken(),
    branchId,
    item.conversationId,
    item.key,
    item.messageId,
    item.revision,
  ]);
  // Gate during render, before effects clear state after virtual-row reuse.
  const fullItem = hydrated?.scope === scope ? hydrated.item : null;

  useEffect(() => {
    generationRef.current += 1;
    requestRef.current?.abort();
    requestRef.current = null;
    setHydrated(null);
    setLoadingFull(false);
    setLoadError(false);
    return () => requestRef.current?.abort();
  }, [scope]);

  const displayItem = useMemo(
    () =>
      fullItem
        ? {
            ...fullItem,
            status: item.status,
            humanDescription: item.humanDescription ?? fullItem.humanDescription,
            output: mergePublicToolOutputEvidence(fullItem.output, item.output),
            imagePath: item.imagePath ?? fullItem.imagePath,
            fileDiffs: item.fileDiffs ?? fullItem.fileDiffs,
          }
        : item,
    [fullItem, item]
  );

  const loadFullItem = useCallback(async () => {
    if (
      !shouldLoadFull ||
      fullItem ||
      loadingFull ||
      loadError ||
      requestRef.current ||
      !item.conversationId ||
      !item.messageId
    )
      return;
    const generation = generationRef.current;
    const accountScope = getRendererAccountScopeToken();
    const assertAccountScope = () => {
      if (getRendererAccountScopeToken() !== accountScope) {
        throw new DOMException('tool_detail_account_scope_changed', 'AbortError');
      }
    };
    requestRef.current?.abort();
    const controller = new AbortController();
    requestRef.current = controller;
    setLoadingFull(true);
    setLoadError(false);
    try {
      const message = await withTransientHistoryRetry(() =>
        ipcBridge.database.getConversationMessage.invoke({
          conversation_id: item.conversationId!,
          message_id: item.messageId!,
          ...(branchId ? { branch_id: branchId } : {}),
        })
      );
      if (controller.signal.aborted) return;
      assertAccountScope();
      const next = normalizeToolMessages([message as ToolMessage]).find((candidate) => candidate.key === item.key);
      if (!next) throw new Error('tool detail is not available in the hydrated message');
      if (
        next.conversationId !== item.conversationId ||
        next.messageId !== item.messageId ||
        next.name !== item.name ||
        (next.branchId && next.branchId !== branchId) ||
        (next.revision !== undefined && item.revision !== undefined && next.revision < item.revision)
      ) {
        throw new Error('tool detail does not match the requested operation snapshot');
      }
      const hydration = await hydrateLargeToolResultOutput(next.output, controller.signal);
      assertAccountScope();
      const supplementalItems = await Promise.all(
        (item.supplementalHistoryRefs ?? []).map(async (reference) => {
          try {
            const supplementalBranchId = reference.branchId ?? branchId;
            const supplementalMessage = await withTransientHistoryRetry(() =>
              ipcBridge.database.getConversationMessage.invoke({
                conversation_id: reference.conversationId,
                message_id: reference.messageId,
                ...(supplementalBranchId ? { branch_id: supplementalBranchId } : {}),
              })
            );
            const candidates = normalizeToolMessages([supplementalMessage as ToolMessage]);
            const supplemental = reference.operationKey
              ? candidates.find((candidate) => candidate.key === reference.operationKey)
              : candidates.length === 1
                ? candidates[0]
                : undefined;
            if (
              !supplemental ||
              supplemental.messageId !== reference.messageId ||
              supplemental.conversationId !== reference.conversationId ||
              (supplemental.branchId && supplemental.branchId !== supplementalBranchId)
            ) {
              throw new Error('supplemental detail does not match its operation');
            }
            return supplemental;
          } catch {
            // A missing wrapper must not silently masquerade as a full load.
            throw new Error('supplemental tool detail is unavailable');
          }
        })
      );
      assertAccountScope();
      const enriched: NormalizedToolCall = {
        ...next,
        ...(hydration.output ? { output: hydration.output, truncated: false } : {}),
        ...(hydration.remote
          ? {
              remoteDetail: {
                conversationId: item.conversationId,
                messageId: item.messageId,
                ...(branchId ? { branchId } : {}),
                ...(next.revision ? { revision: next.revision } : {}),
                ...(hydration.contentUrl ? { contentUrl: hydration.contentUrl } : {}),
              },
            }
          : {}),
      };
      for (const supplemental of supplementalItems) {
        if (!supplemental) continue;
        enriched.humanDescription ??= supplemental.humanDescription;
        enriched.output = mergePublicToolOutputEvidence(enriched.output, supplemental.output);
      }
      if (
        generation === generationRef.current &&
        (item.branchId ?? getSelectedSynonBiomedBranch(item.conversationId)) === branchId
      ) {
        setHydrated({ scope, item: enriched });
      }
    } catch {
      if (!controller.signal.aborted && generation === generationRef.current) setLoadError(true);
    } finally {
      if (generation === generationRef.current) setLoadingFull(false);
      if (requestRef.current === controller) requestRef.current = null;
    }
  }, [branchId, fullItem, item, loadingFull, loadError, shouldLoadFull, scope]);

  const retryFullItem = useCallback(() => setLoadError(false), []);
  return { displayItem, loadError, loadFullItem, loadingFull, retryFullItem };
}
