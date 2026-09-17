/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { TChatConversation } from '@/common/config/storage';
import { prefetchConversationMessages } from './Messages/messageWindowPrefetch';
import { getSynonBiomedBranchSelectionRevision } from '@/renderer/services/synonBiomedConversationBranches';
import {
  beginConversationDetailHandoff,
  getConversationOrNullShared,
  rememberConversationRouteSnapshot,
} from './utils/conversationCache';

type ConversationRouteModule = typeof import('./index');

let conversationRoutePromise: Promise<ConversationRouteModule> | null = null;

export function loadConversationRoute(): Promise<ConversationRouteModule> {
  conversationRoutePromise ??= import('./index');
  return conversationRoutePromise;
}

export async function prefetchConversationRoute(): Promise<void> {
  await loadConversationRoute();
}

export type ConversationNavigationWarmup = {
  ownerId: string;
  conversationId: string;
  summary?: TChatConversation;
};

/**
 * Start the three independent reads needed by every conversation route before
 * React replaces the current screen. Project navigation and direct history-row
 * navigation must use the same handoff; otherwise project clicks lose the
 * summary/message head start and render a blocking loading surface.
 */
export async function warmConversationNavigation({
  ownerId,
  conversationId,
  summary,
}: ConversationNavigationWarmup): Promise<void> {
  const normalizedOwnerId = ownerId.trim();
  const normalizedConversationId = conversationId.trim();
  if (!normalizedOwnerId || !normalizedConversationId) return;

  if (summary?.id === normalizedConversationId) {
    rememberConversationRouteSnapshot(normalizedOwnerId, summary);
  }

  beginConversationDetailHandoff(normalizedConversationId);
  const routeReady = prefetchConversationRoute().catch((): undefined => undefined);
  // The route shell, title and runtime view all share this request. Starting it
  // before navigation avoids a serial route -> detail -> messages waterfall.
  const detailReady = getConversationOrNullShared(normalizedConversationId)
    .then((conversation) => {
      if (conversation) rememberConversationRouteSnapshot(normalizedOwnerId, conversation, 'detail');
    })
    .catch((): undefined => undefined);
  void prefetchConversationMessages({
    ownerId: normalizedOwnerId,
    conversationId: normalizedConversationId,
    branchRevision: getSynonBiomedBranchSelectionRevision(normalizedConversationId),
    retryTransient: false,
  }).catch((): undefined => undefined);

  // Give the route module and the shared detail read a very short handoff
  // window. Fast local reads finish before React mounts, while a slow or
  // unavailable backend never blocks navigation indefinitely; the route then
  // joins the still-active shared request.
  let releaseDelay: ReturnType<typeof setTimeout> | undefined;
  await Promise.race([
    Promise.all([routeReady, detailReady]).then((): undefined => undefined),
    new Promise<void>((resolve) => {
      releaseDelay = setTimeout(resolve, 200);
    }),
  ]);
  if (releaseDelay !== undefined) clearTimeout(releaseDelay);
}
