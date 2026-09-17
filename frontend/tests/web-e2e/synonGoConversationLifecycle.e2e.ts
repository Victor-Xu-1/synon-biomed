import { expect, test, type Page } from '@playwright/test';
import { webPassword, webUsername } from './synonGoWebCredentials';
const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

type LifecycleFixture = {
  conversationId: string;
  messageId: string;
  projectId: string;
  prompt: string;
};

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('persists a project conversation across realtime delivery and reload', async ({ page }) => {
      await login(page);
      const fixture = await createLifecycleFixture(page, viewport.name);

      await page.goto(`/#/conversation/${fixture.conversationId}`, { waitUntil: 'domcontentloaded' });
      await expect(page.getByTestId('conversation-deleted-tombstone')).toHaveCount(0);
      await expect(page.getByText(fixture.prompt, { exact: false }).first()).toBeVisible();

      await page.reload({ waitUntil: 'domcontentloaded' });
      await expect(page).toHaveURL(new RegExp(`#/conversation/${fixture.conversationId}$`));
      await expect(page.getByTestId('conversation-deleted-tombstone')).toHaveCount(0);
      await expect(page.getByText(fixture.prompt, { exact: false }).first()).toBeVisible();

      const recovered = await page.evaluate(async ({ conversationId, messageId }) => {
        const [conversationResponse, messagesResponse, runtimeResponse] = await Promise.all([
          fetch(`/api/conversations/${encodeURIComponent(conversationId)}`),
          fetch(`/api/conversations/${encodeURIComponent(conversationId)}/messages?limit=50`),
          fetch(`/api/conversations/${encodeURIComponent(conversationId)}/runtime/ensure`, {
            method: 'POST',
            headers: { 'content-type': 'application/json' },
            body: '{}',
          }),
        ]);
        const messages = (await messagesResponse.json()) as { items?: Array<{ id?: string; msg_id?: string }> };
        const runtime = (await runtimeResponse.json()) as { recovered?: boolean };
        return {
          conversationStatus: conversationResponse.status,
          messagesStatus: messagesResponse.status,
          runtimeStatus: runtimeResponse.status,
          recovered: runtime.recovered,
          messagePresent: messages.items?.some((item) => item.id === messageId || item.msg_id === messageId) ?? false,
        };
      }, fixture);
      expect(recovered).toEqual({
        conversationStatus: 200,
        messagesStatus: 200,
        runtimeStatus: 200,
        recovered: true,
        messagePresent: true,
      });

      await cleanupLifecycleFixture(page, fixture);
    });
  });
}

async function login(page: Page) {
  await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
  await page.locator('input[name="username"]').fill(webUsername);
  await page.locator('input[name="password"]').fill(webPassword);
  await page.locator('button[type="submit"]').click();
  await expect(page).toHaveURL(/#\/guid/);
}

async function createLifecycleFixture(page: Page, viewportName: string): Promise<LifecycleFixture> {
  return page.evaluate(async (viewport) => {
    const suffix = `${viewport}-${crypto.randomUUID().slice(0, 8)}`;
    const prompt = `P3 durable browser message ${suffix}`;
    const socket = new WebSocket(
      `${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/api/events/ws?after_sequence=latest`
    );
    const events: Array<{ type?: string; conversation_id?: string; action?: string }> = [];
    const waitForConversationEvent = async (conversationId: string, action: string) => {
      const deadline = Date.now() + 10_000;
      while (Date.now() < deadline) {
        if (
          events.some(
            (event) =>
              event.type === 'conversation.listChanged' &&
              event.conversation_id === conversationId &&
              event.action === action
          )
        ) {
          return;
        }
        await new Promise((resolve) => window.setTimeout(resolve, 50));
      }
      throw new Error(`conversation.listChanged ${action} was not observed`);
    };
    socket.addEventListener('message', (event) => {
      try {
        events.push(JSON.parse(String(event.data)) as { type?: string; conversation_id?: string; action?: string });
      } catch {
        // The test only records valid event envelopes.
      }
    });
    await new Promise<void>((resolve, reject) => {
      const timer = window.setTimeout(() => reject(new Error('event websocket did not open')), 10_000);
      socket.addEventListener('open', () => {
        window.clearTimeout(timer);
        resolve();
      });
      socket.addEventListener('error', () => {
        window.clearTimeout(timer);
        reject(new Error('event websocket failed'));
      });
    });

    const projectResponse = await fetch('/api/projects', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ name: `P3 browser ${suffix}`, description: 'Disposable P3 acceptance fixture' }),
    });
    if (projectResponse.status !== 201) throw new Error(`project create failed: ${projectResponse.status}`);
    const project = (await projectResponse.json()) as { project_id?: string };
    if (!project.project_id) throw new Error('project create response has no project_id');

    const conversationResponse = await fetch('/api/conversations', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        name: `P3 lifecycle ${suffix}`,
        assistant: { id: 'synonbiomed:OPERON', locale: 'zh-CN', conversation_overrides: {} },
        extra: { project_id: project.project_id, project_name: `P3 browser ${suffix}` },
      }),
    });
    if (conversationResponse.status !== 201) {
      throw new Error(
        `conversation create failed: ${conversationResponse.status} ${await conversationResponse.text()}`
      );
    }
    const conversation = (await conversationResponse.json()) as { id?: string };
    if (!conversation.id) throw new Error('conversation create response has no id');

    await waitForConversationEvent(conversation.id, 'created');
    const messageResponse = await fetch(`/api/conversations/${encodeURIComponent(conversation.id)}/messages`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ content: prompt, files: [], inject_skills: [], session_options: {} }),
    });
    if (messageResponse.status !== 202) {
      throw new Error(`message send failed: ${messageResponse.status} ${await messageResponse.text()}`);
    }
    const message = (await messageResponse.json()) as { msg_id?: string };
    if (!message.msg_id) throw new Error('message send response has no msg_id');
    await waitForConversationEvent(conversation.id, 'updated');
    socket.close();
    return {
      projectId: project.project_id,
      conversationId: conversation.id,
      messageId: message.msg_id,
      prompt,
    };
  }, viewportName);
}

async function cleanupLifecycleFixture(page: Page, fixture: LifecycleFixture) {
  const statuses = await page.evaluate(async ({ conversationId, projectId }) => {
    const conversation = await fetch(`/api/conversations/${encodeURIComponent(conversationId)}`, { method: 'DELETE' });
    const project = await fetch(`/api/projects/${encodeURIComponent(projectId)}`, { method: 'DELETE' });
    return [conversation.status, project.status];
  }, fixture);
  expect(statuses).toEqual([200, 200]);
}
