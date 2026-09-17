import { randomUUID } from 'node:crypto';
import { request as playwrightRequest, type Page, type Route } from '@playwright/test';
import { expect, test } from './officialChromeTest';
import {
  createScientificWorkspace,
  csrfHeaders,
  loginToScientificWorkbench,
  removeScientificWorkspace,
} from './synonBiomedScientificFixture';
import { activateSynonGoTranscriptRebase, prepareSynonGoTranscriptRebase } from './synonGoFrameFixture';
import { webPassword, webUsername } from './synonGoWebCredentials';

type WireMessage = {
  id?: unknown;
  msg_id?: unknown;
  content?: { synonBiomed?: { messageIndex?: unknown } };
};

type MessagePage = {
  items?: WireMessage[];
  oldest_cursor?: unknown;
  newest_cursor?: unknown;
  has_more_before?: unknown;
  branch_id?: unknown;
  branch_generation?: unknown;
};

type ReadCursor = {
  message_uuid: string;
  message_index: number;
};

type OwnerCredentials = {
  id: string;
  username: string;
  password: string;
};

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('rebases real transcript history without pagination or owner cursor leakage', async ({ context, page }) => {
      test.setTimeout(120_000);
      if (process.env.SYNON_GO_E2E_REQUIRE_GOOGLE_CHROME === 'true') {
        await requireOfficialGoogleChrome(page);
      }
      await loginToScientificWorkbench(page);
      const freshOwner = await provisionFreshOwner(page, viewport.name);
      const workspace = await createScientificWorkspace(page, `transcript-rebase-${viewport.name}`);
      let primaryOwnerActive = true;
      let releaseHeldPage: (() => void) | undefined;
      let heldHandlerDone: Promise<void> | undefined;
      let heldRequestStarted = false;

      try {
        const preparation = prepareSynonGoTranscriptRebase(workspace.conversationId, 320);
        expect(preparation.eventCount).toBeGreaterThanOrEqual(320);
        expect(preparation.sourceGeneration).toBe(1);
        await expectMessageHistoryUnavailable(page, workspace.conversationId);

        // The fixture writes through the same durable repository as an
        // offline migration process. Reconnect once so the production replay
        // coordinator drains the source stream before the activation fence;
        // the migrated payload must remain unavailable until activation.
        const replay = await page.request.get('/api/events?after_sequence=latest&limit=1');
        expect(replay.ok(), `replay preparation HTTP ${replay.status()}`).toBe(true);
        await expectMessageHistoryUnavailable(page, workspace.conversationId);

        const applicationSockets: string[] = [];
        page.on('websocket', (socket) => {
          const url = new URL(socket.url());
          if (url.pathname === '/api/events/ws') applicationSockets.push(socket.url());
        });
        const canonicalPages: MessagePage[] = [];
        page.on('response', async (response) => {
          const url = new URL(response.url());
          if (
            response.ok() &&
            url.pathname === `/api/conversations/${workspace.conversationId}/messages` &&
            !url.searchParams.has('before')
          ) {
            const payload = (await response.json().catch(() => undefined)) as MessagePage | undefined;
            if (payload) canonicalPages.push(payload);
          }
        });

        const routeConversationLoaded = page.waitForResponse((response) => {
          const url = new URL(response.url());
          return url.pathname === `/api/conversations/${workspace.conversationId}` && response.ok();
        });
        const routeHistoryUnavailable = page.waitForResponse((response) => {
          const url = new URL(response.url());
          return (
            url.pathname === `/api/conversations/${workspace.conversationId}/messages` &&
            url.searchParams.get('limit') === '200' &&
            response.status() === 503
          );
        });
        await page.goto(`/#/conversation/${workspace.conversationId}`, { waitUntil: 'domcontentloaded' });
        await Promise.all([routeConversationLoaded, routeHistoryUnavailable]);
        const socketsBeforeReconnect = applicationSockets.length;
        await context.setOffline(true);
        const activation = activateSynonGoTranscriptRebase(workspace.conversationId, preparation.cutoverId);
        expect(activation.targetEpoch).toBeGreaterThan(1);
        expect(activation.authorityGeneration).toBeGreaterThan(preparation.sourceGeneration);
        await context.setOffline(false);
        await page.reload({ waitUntil: 'domcontentloaded' });

        await expect.poll(() => applicationSockets.length).toBeGreaterThan(socketsBeforeReconnect);
        const activatedPage = await getMessagePage(page, workspace.conversationId);
        expect(activatedPage.branch_id).toBe(activation.activeBranchId);
        expect(activatedPage.items).toHaveLength(50);
        expect(activatedPage.has_more_before).toBe(true);
        const readAnchor = requireMessages(activatedPage)[0];
        await assertSingleMessageCoordinates(page, workspace.conversationId, readAnchor);
        await putReadCursor(page, workspace.conversationId, readAnchor);

        await page.reload({ waitUntil: 'domcontentloaded' });
        await expect(page.getByTestId('message-list-scroller')).toBeVisible();
        await expect
          .poll(() => canonicalPages.some((candidate) => candidate.branch_id === activation.activeBranchId))
          .toBe(true);
        await expect(page.getByTestId('jump-to-last-seen')).toHaveCount(0);
        await expect.poll(() => getReadCursorIndex(page, workspace.conversationId)).toBe(messageIndex(readAnchor));

        const heldRelease = new Promise<void>((resolve) => {
          releaseHeldPage = resolve;
        });
        let markHeldStarted!: () => void;
        const heldStarted = new Promise<void>((resolve) => {
          markHeldStarted = resolve;
        });
        let heldPage: MessagePage | undefined;
        let heldOnce = false;
        let markHeldDone!: () => void;
        heldHandlerDone = new Promise<void>((resolve) => {
          markHeldDone = resolve;
        });
        const paginationRoute = async (route: Route) => {
          const url = new URL(route.request().url());
          const before = url.searchParams.get('before');
          if (url.pathname !== `/api/conversations/${workspace.conversationId}/messages` || !before) {
            await route.continue();
            return;
          }
          if (heldOnce) {
            await route.continue();
            return;
          }
          heldOnce = true;
          const upstream = await route.fetch();
          const body = await upstream.body();
          heldPage = JSON.parse(body.toString('utf8')) as MessagePage;
          heldRequestStarted = true;
          markHeldStarted();
          try {
            await heldRelease;
            await route.fulfill({ response: upstream, body });
          } finally {
            markHeldDone();
          }
        };
        await page.route('**/api/conversations/**/messages?**', paginationRoute);
        const transcriptScroller = page.getByTestId('message-list-scroller');
        await transcriptScroller.hover();
        for (let attempt = 0; attempt < 4; attempt += 1) {
          if ((await transcriptScroller.evaluate((element) => element.scrollTop)) <= 1) break;
          await page.mouse.wheel(0, -6_000);
        }
        await expect.poll(() => transcriptScroller.evaluate((element) => element.scrollTop)).toBeLessThanOrEqual(1);
        await expect.poll(() => heldRequestStarted).toBe(true);
        await heldStarted;
        expect(requireMessages(heldPage).length).toBeGreaterThan(0);
        expect(requireMessages(heldPage).length).toBeLessThanOrEqual(200);
        expect(heldPage?.has_more_before).toBe(false);
        const heightBeforePrepend = await transcriptScroller.evaluate((element) => element.scrollHeight);

        releaseHeldPage();
        releaseHeldPage = undefined;
        await heldHandlerDone;
        await expect
          .poll(() => transcriptScroller.evaluate((element) => element.scrollHeight))
          .toBeGreaterThan(heightBeforePrepend);
        await expect(page.getByTestId('transcript-load-older')).toHaveCount(0);
        const activatedIDs = new Set(requireMessages(activatedPage).map(messageID));
        const heldOnlyIDs = new Set(
          requireMessages(heldPage)
            .map(messageID)
            .filter((id) => !activatedIDs.has(id))
        );
        expect(heldOnlyIDs.size).toBeGreaterThan(0);
        // Virtuoso preserves the currently visible anchor when an older page
        // is prepended, so narrow viewports need not mount any prepended row.
        // The increased virtual height above proves the delayed page was
        // merged without coupling acceptance to viewport overscan.
        const visibleIDs = await renderedMessageIDs(page);
        expect(new Set(visibleIDs).size).toBe(visibleIDs.length);
        const latestActivatedID = messageID(requireMessages(activatedPage).at(-1)!);
        const bottomControl = page.locator('button[aria-label="滚动到对话底部"]');
        await expect(bottomControl).toHaveAttribute('aria-hidden', 'false');
        await bottomControl.click();
        await expect.poll(() => renderedMessageIDs(page)).toContain(latestActivatedID);

        await page.reload();
        await expect(page.getByTestId('message-list-scroller')).toBeVisible();
        await expect.poll(() => renderedMessageIDs(page)).toContain(latestActivatedID);
        await assertSingleMessageCoordinates(page, workspace.conversationId, readAnchor);
        const primaryCursor = await getReadCursor(page, workspace.conversationId);
        expect(primaryCursor).not.toBeNull();
        await assertSingleMessageCoordinates(page, workspace.conversationId, {
          id: primaryCursor!.message_uuid,
          content: { synonBiomed: { messageIndex: primaryCursor!.message_index } },
        });

        primaryOwnerActive = false;
        await switchToFreshOwner(page, freshOwner);
        await expect.poll(() => currentOwnerID(page)).toBe(freshOwner.id);
        const foreignPage = await page.request.get(
          `/api/conversations/${encodeURIComponent(workspace.conversationId)}/messages?limit=50&content_mode=compact`
        );
        expect(foreignPage.status()).toBe(404);
        const foreignCursor = await page.request.get(
          `/api/frames/${encodeURIComponent(workspace.conversationId)}/read-cursor`
        );
        expect(foreignCursor.status()).toBe(404);
        await page.evaluate((conversationId) => {
          window.location.hash = `/conversation/${encodeURIComponent(conversationId)}`;
        }, workspace.conversationId);
        await expect(page).toHaveURL(/#\/guid$/);
        await expect(page.getByTestId('conversation-deleted-tombstone')).toHaveCount(0);
        await expect(page.locator('[data-source-message-id]')).toHaveCount(0);

        await restorePrimaryOwner(page);
        primaryOwnerActive = true;
        await navigateHash(page, `/conversation/${encodeURIComponent(workspace.conversationId)}`);
        await expect(page.getByTestId('message-list-scroller')).toBeVisible();
        await expect.poll(() => renderedMessageIDs(page)).toContain(latestActivatedID);
        const restoredPage = await getMessagePage(page, workspace.conversationId);
        expect(restoredPage.branch_id).toBe(activation.activeBranchId);
        expect(await getReadCursor(page, workspace.conversationId)).toEqual(primaryCursor);
        await assertSingleMessageCoordinates(page, workspace.conversationId, readAnchor);
      } finally {
        releaseHeldPage?.();
        if (heldRequestStarted) await heldHandlerDone;
        await releaseFixtureRouteSafely(page);
        if (!primaryOwnerActive) {
          await restorePrimaryOwner(page);
        }
        await removeScientificWorkspace(page, workspace);
      }
    });
  });
}

async function getMessagePage(page: Page, conversationId: string): Promise<MessagePage> {
  const response = await page.request.get(
    `/api/conversations/${encodeURIComponent(conversationId)}/messages?limit=50&content_mode=compact`
  );
  const body = await response.text();
  expect(response.status(), `message page HTTP ${response.status()}: ${body}`).toBe(200);
  return JSON.parse(body) as MessagePage;
}

async function expectMessageHistoryUnavailable(page: Page, conversationId: string): Promise<void> {
  const response = await page.request.get(
    `/api/conversations/${encodeURIComponent(conversationId)}/messages?limit=50&content_mode=compact`
  );
  expect(response.status()).toBe(503);
  expect(await response.json()).toEqual({
    code: 'HISTORY_NOT_READY',
    message: 'conversation history is still being prepared',
  });
}

function requireMessages(page: MessagePage | undefined): WireMessage[] {
  if (!page || !Array.isArray(page.items)) throw new Error('message page has no items');
  page.items.forEach((message) => {
    messageID(message);
    messageIndex(message);
  });
  return page.items;
}

function messageID(message: WireMessage): string {
  const id = typeof message.id === 'string' && message.id ? message.id : message.msg_id;
  if (typeof id !== 'string' || !id) throw new Error('message has no stable id');
  return id;
}

function messageIndex(message: WireMessage): number {
  const index = message.content?.synonBiomed?.messageIndex;
  if (!Number.isSafeInteger(index) || Number(index) < 0) throw new Error('message has no stable index');
  return Number(index);
}

async function assertSingleMessageCoordinates(
  page: Page,
  conversationId: string,
  expected: WireMessage
): Promise<void> {
  const id = messageID(expected);
  const response = await page.request.get(
    `/api/conversations/${encodeURIComponent(conversationId)}/messages/${encodeURIComponent(id)}`
  );
  expect(response.status(), `single message HTTP ${response.status()}`).toBe(200);
  const single = (await response.json()) as WireMessage;
  expect(messageID(single)).toBe(id);
  expect(messageIndex(single)).toBe(messageIndex(expected));
}

async function putReadCursor(page: Page, conversationId: string, message: WireMessage): Promise<void> {
  const response = await page.request.put(`/api/frames/${encodeURIComponent(conversationId)}/read-cursor`, {
    headers: await csrfHeaders(page),
    data: {
      message_uuid: messageID(message),
      message_index: messageIndex(message),
      repair: false,
    },
  });
  expect(response.status(), `read cursor HTTP ${response.status()}`).toBe(200);
}

async function getReadCursorIndex(page: Page, conversationId: string): Promise<number> {
  return (await getReadCursor(page, conversationId))?.message_index ?? -1;
}

async function getReadCursor(page: Page, conversationId: string): Promise<ReadCursor | null> {
  const response = await page.request.get(`/api/frames/${encodeURIComponent(conversationId)}/read-cursor`);
  if (!response.ok()) return null;
  const cursor = (await response.json()) as {
    message_uuid?: unknown;
    message_index?: unknown;
  } | null;
  if (
    typeof cursor?.message_uuid !== 'string' ||
    !cursor.message_uuid ||
    !Number.isSafeInteger(cursor.message_index) ||
    Number(cursor.message_index) < 0
  ) {
    throw new Error('read cursor response has no stable coordinates');
  }
  return { message_uuid: cursor.message_uuid, message_index: Number(cursor.message_index) };
}

async function renderedMessageIDs(page: Page): Promise<string[]> {
  return page
    .locator('[data-source-message-id]')
    .evaluateAll((rows) =>
      rows
        .map((row) => row.getAttribute('data-source-message-id'))
        .filter((id): id is string => typeof id === 'string' && id.length > 0)
    );
}

async function requireOfficialGoogleChrome(page: Page): Promise<void> {
  const expectedVersion = process.env.SYNON_GO_E2E_GOOGLE_CHROME_VERSION?.trim();
  if (!expectedVersion) throw new Error('SYNON_GO_E2E_GOOGLE_CHROME_VERSION is required');
  expect(page.context().browser()?.version()).toBe(expectedVersion);
  await expect
    .poll(() => page.evaluate(() => navigator.userAgent))
    .toContain(`Chrome/${expectedVersion.split('.')[0]}.`);
}

async function provisionFreshOwner(page: Page, label: string): Promise<OwnerCredentials> {
  const suffix = randomUUID().replaceAll('-', '');
  const username = `rebase_${label}_${suffix.slice(0, 12)}`;
  const password = 'Synon-Rebase-E2E-2026!';
  const api = await playwrightRequest.newContext({ baseURL: new URL(page.url()).origin });
  try {
    const register = await api.post('/api/auth/register', {
      data: { name: username, email: `rebase_${suffix}@example.test`, password, remember: false },
    });
    expect(register.status(), `owner registration HTTP ${register.status()}`).toBe(200);
    const payload = (await register.json()) as { user?: { id?: unknown } };
    if (typeof payload.user?.id !== 'string' || !payload.user.id)
      throw new Error('owner registration returned no user id');
    const state = await api.storageState();
    const csrf = state.cookies.find((cookie) => cookie.name === 'synon_csrf')?.value;
    if (!csrf) throw new Error('registered owner has no CSRF cookie');
    const headers = { 'X-Synon-CSRF-Token': decodeURIComponent(csrf) };
    for (const endpoint of [
      '/api/preferences/builtin-allowlist/onboarding-seen',
      '/api/preferences/first-run-onboarding/complete',
    ]) {
      const response = await api.post(endpoint, { headers });
      expect(response.ok(), `${endpoint} HTTP ${response.status()}`).toBe(true);
    }
    return { id: payload.user.id, username, password };
  } finally {
    await api.dispose();
  }
}

async function switchToFreshOwner(page: Page, owner: OwnerCredentials): Promise<void> {
  await page.keyboard.press('Control+Shift+L');
  await expect.poll(() => currentOwnerID(page)).toBeNull();
  await navigateHash(page, '/login');
  await page.locator('#username').fill(owner.username);
  await page.locator('#password').fill(owner.password);
  await page.locator('button[type="submit"]').click();
  await expect.poll(() => currentOwnerID(page)).toBe(owner.id);
  await expect(page).toHaveURL(/#\/guid$/);
}

async function currentOwnerID(page: Page): Promise<string | null> {
  const response = await page.request.get('/api/auth/user');
  if (!response.ok()) return null;
  const payload = (await response.json()) as { user?: { id?: unknown } };
  return typeof payload.user?.id === 'string' ? payload.user.id : null;
}

async function restorePrimaryOwner(page: Page): Promise<void> {
  const current = await page.request.get('/api/auth/user');
  if (current.ok()) {
    const payload = (await current.json()) as { user?: { username?: unknown } };
    if (payload.user?.username === webUsername) return;
    await page.keyboard.press('Control+Shift+L');
    await expect.poll(() => currentOwnerID(page)).toBeNull();
  }
  await navigateHash(page, '/login');
  await page.locator('#username').fill(webUsername);
  await page.locator('#password').fill(webPassword);
  await page.locator('button[type="submit"]').click();
  await expect.poll(() => currentUsername(page)).toBe(webUsername);
  await expect(page).toHaveURL(/#\/guid$/);
}

async function currentUsername(page: Page): Promise<string | null> {
  const response = await page.request.get('/api/auth/user');
  if (!response.ok()) return null;
  const payload = (await response.json()) as { user?: { username?: unknown } };
  return typeof payload.user?.username === 'string' ? payload.user.username : null;
}

async function navigateHash(page: Page, hashPath: string): Promise<void> {
  await page.evaluate((nextHash) => {
    window.location.hash = nextHash;
  }, hashPath);
  await expect(page).toHaveURL(new RegExp(`#${hashPath.replace(/[.*+?^${}()|[\]\\]/gu, '\\$&')}$`));
}

async function releaseFixtureRouteSafely(page: Page): Promise<void> {
  await page.unroute('**/api/conversations/**/messages?**').catch(() => undefined);
}
