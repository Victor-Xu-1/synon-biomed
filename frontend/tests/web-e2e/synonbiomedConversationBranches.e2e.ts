import type { APIRequestContext, Page } from '@playwright/test';
import { randomUUID } from 'node:crypto';
import { expect, test } from './officialChromeTest';
import { createControlledLlmFixture, type ControlledLlmFixture } from '../integration/synonbiomedControlledLlmFixture';
import { csrfHeaders, loginToScientificWorkbench, removeScientificWorkspace } from './synonBiomedScientificFixture';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
const llmFixtures: ControlledLlmFixture[] = [];

test.afterEach(async () => {
  for (const fixture of llmFixtures.splice(0)) await fixture.dispose();
});

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('edits into a canonical branch and switches exact branch history', async ({ page }) => {
      await loginToScientificWorkbench(page);
      llmFixtures.push(await createControlledLlmFixture(gatewayBaseUrl));
      const workspace = await createReadOnlyBranchWorkspace(page, `branches-${viewport.name}`);
      const headers = await csrfHeaders(page);
      try {
        const prompt = `Canonical branch acceptance ${viewport.name}`;
        const revisedPrompt = `Canonical branch correction ${viewport.name}`;
        const continuationPrompt = `Continue exact main branch ${viewport.name}`;
        await seedReadOnlyUserMessage(page.request, workspace.conversationId, prompt, headers);

        await page.goto(`/#/conversation/${encodeURIComponent(workspace.conversationId)}`);
        await expect(page).toHaveURL(new RegExp(`#/conversation/${workspace.conversationId}$`));
        const userMessage = rightMessageRow(page, prompt);
        await expect(userMessage).toBeVisible();
        await userMessage.hover();
        const editButton = userMessage.getByRole('button', { name: /编辑消息|Edit message/ });
        await expect(editButton).toBeVisible();
        await editButton.click();
        const editor = page.getByRole('textbox', { name: /编辑历史用户消息|Edit previous user message/ });
        await editor.fill(revisedPrompt);
        const forkResponse = page.waitForResponse(
          (response) =>
            response.request().method() === 'POST' &&
            response.url().includes(`/api/frames/${workspace.conversationId}/fork`)
        );
        await page.getByRole('button', { name: /^(保存并提交|Save and submit)$/ }).click();
        expect((await forkResponse).status()).toBe(200);
        await expect(editor).not.toBeVisible();
        await expect(rightMessageText(page, revisedPrompt).first()).toHaveText(revisedPrompt);
        await expect(page.getByTestId('synon-biomed-branch-trigger')).toHaveCount(0);

        if (viewport.name === 'desktop') {
          const attachButton = page.getByTestId('conversation-attach-folder-btn');
          await attachButton.click();
          const attachMenu = page.getByRole('menu', { name: /添加到消息|Add to message/ });
          await expect(attachMenu.getByRole('menuitem', { name: /添加文件|Add file/ })).toBeVisible();
          await expect(attachMenu.getByRole('menuitem', { name: /你的文件|Your files/ })).toBeVisible();
          await expect(attachMenu.getByRole('menuitem', { name: /手动审阅|Manual review/ })).toBeVisible();
          await attachMenu.getByRole('menuitem', { name: /添加文件|Add file/ }).press('Escape');
          await expect(attachMenu).not.toBeVisible();
          await expect(attachButton).toBeFocused();
        } else {
          await expect(page.getByTestId('conversation-attach-folder-btn')).toHaveCount(0);
          const mobileMore = page.getByTestId('sendbox-mobile-plus-btn');
          await expect(mobileMore).toBeVisible();
          await mobileMore.click();
          await expect(page.getByTestId('mobile-action-sheet-attach-host-files')).toBeVisible();
          await expect(page.getByTestId('mobile-action-sheet-attach-my-device')).toBeVisible();
          await page.keyboard.press('Escape');
          await expect(page.getByRole('dialog')).not.toBeVisible();
        }

        const branchesResponse = await page.request.get(
          `/api/frames/${encodeURIComponent(workspace.conversationId)}/branches`
        );
        expect(branchesResponse.status(), await redactedFailure('branch list', branchesResponse)).toBe(200);
        const branches = (await branchesResponse.json()) as { branches?: unknown[]; active_branch_id?: unknown };
        expect(branches.branches).toHaveLength(2);
        expect(branches.active_branch_id).toMatch(/^br_[0-9a-f]{8}$/);

        const cancel = await page.request.post(
          `/api/frames/${encodeURIComponent(workspace.conversationId)}/cancel?reason=e2e_branch_continue`,
          { headers }
        );
        expect(cancel.status(), await redactedFailure('branch continuation cancellation', cancel)).toBe(200);
        await waitForSettledFrame(page.request, workspace.conversationId);

        const revisedRow = rightMessageRow(page, revisedPrompt);
        await revisedRow.getByRole('button', { name: /上一个分支|Previous branch/ }).click();
        await expect(rightMessageText(page, prompt).first()).toHaveText(prompt);
        await expect(rightMessageText(page, revisedPrompt)).toHaveCount(0);

        const composer = page.getByTestId('sendbox-input');
        await expect(composer).toBeEnabled();
        await composer.fill(continuationPrompt);
        const sendButton = page.getByTestId('sendbox-send-btn');
        await expect(sendButton).toBeEnabled();
        const continuationResponse = page.waitForResponse(
          (response) =>
            response.request().method() === 'POST' &&
            response.url().includes(`/api/conversations/${workspace.conversationId}/messages`)
        );
        await sendButton.click();
        expect((await continuationResponse).status()).toBe(202);
        await expect(rightMessageText(page, continuationPrompt).first()).toHaveText(continuationPrompt);
        const continuationCancel = await page.request.post(
          `/api/frames/${encodeURIComponent(workspace.conversationId)}/cancel?reason=e2e_continuation_complete`,
          { headers }
        );
        expect(
          continuationCancel.status(),
          await redactedFailure('continued branch cancellation', continuationCancel)
        ).toBe(200);
        await waitForSettledFrame(page.request, workspace.conversationId);

        await rightMessageRow(page, prompt)
          .getByRole('button', { name: /下一个分支|Next branch/ })
          .click();
        await expect(rightMessageText(page, revisedPrompt).first()).toHaveText(revisedPrompt);
        await expect(rightMessageText(page, continuationPrompt)).toHaveCount(0);

        await rightMessageRow(page, revisedPrompt)
          .getByRole('button', { name: /上一个分支|Previous branch/ })
          .click();
        await expect(rightMessageText(page, continuationPrompt).first()).toHaveText(continuationPrompt);
        await expect(page).toHaveURL(new RegExp(`#/conversation/${workspace.conversationId}$`));

        await page.reload();
        await expect(rightMessageText(page, continuationPrompt).first()).toHaveText(continuationPrompt);
        await expect(page.getByTestId('synon-biomed-branch-trigger')).toHaveCount(0);
        await expect(page.getByRole('button', { name: /上一个分支|Previous branch/ })).toBeVisible();
        await expect(page.getByText('1 / 2', { exact: true })).toBeVisible();
        await assertNoHorizontalPageOverflow(page);
      } finally {
        await removeScientificWorkspace(page, workspace);
      }
    });
  });
}

async function seedReadOnlyUserMessage(
  request: APIRequestContext,
  rootFrameId: string,
  prompt: string,
  headers: Record<string, string>
): Promise<void> {
  const message = await request.post(`/api/frames/${encodeURIComponent(rootFrameId)}/message`, {
    data: { input_data: { request: prompt }, thinking: false },
    headers,
  });
  expect(message.status(), await redactedFailure('message creation', message)).toBe(200);
  const cancel = await request.post(`/api/frames/${encodeURIComponent(rootFrameId)}/cancel?reason=e2e_cleanup`, {
    headers,
  });
  expect(cancel.status(), await redactedFailure('message cancellation', cancel)).toBe(200);
  await waitForSettledFrame(request, rootFrameId);
}

async function waitForSettledFrame(request: APIRequestContext, rootFrameId: string): Promise<void> {
  await expect
    .poll(async () => {
      const response = await request.get(`/api/frames/${encodeURIComponent(rootFrameId)}?shallow=true`);
      if (!response.ok()) return `http-${response.status()}`;
      const payload = (await response.json()) as { frame?: { status?: unknown }; status?: unknown };
      return String(payload.frame?.status ?? payload.status ?? '');
    })
    .toMatch(/^(paused|cancelled|completed|failed)$/);
}

async function createReadOnlyBranchWorkspace(page: Page, label: string) {
  const suffix = `${label}-${randomUUID().slice(0, 8)}`;
  const projectName = `Branch authority ${suffix}`;
  const headers = await csrfHeaders(page);
  const projectResponse = await page.request.post('/api/projects', {
    data: { name: projectName, description: 'Disposable canonical branch fixture.', context: '' },
    headers,
  });
  expect(projectResponse.status(), await redactedFailure('project creation', projectResponse)).toBe(201);
  const project = (await projectResponse.json()) as { project_id?: unknown };
  expect(project.project_id).toEqual(expect.any(String));
  const projectId = String(project.project_id);

  const conversationResponse = await page.request.post('/api/conversations', {
    data: {
      name: `Canonical branches ${suffix}`,
      assistant: { id: 'synonbiomed:OPERON', locale: 'zh-CN', conversation_overrides: {} },
      extra: { project_id: projectId, project_name: projectName },
    },
    headers,
  });
  if (conversationResponse.status() !== 201) {
    await page.request.delete(`/api/projects/${encodeURIComponent(projectId)}`, { headers });
  }
  expect(conversationResponse.status(), await redactedFailure('conversation creation', conversationResponse)).toBe(201);
  const conversation = (await conversationResponse.json()) as { id?: unknown };
  expect(conversation.id).toEqual(expect.any(String));
  return { conversationId: String(conversation.id), projectId, projectName };
}

async function redactedFailure(label: string, response: { status(): number; ok(): boolean }): Promise<string> {
  return response.ok() ? label : `${label} request failed with HTTP ${response.status()}`;
}

function rightMessageText(page: Page, text: string) {
  return page.locator('[data-testid$="-right"] [data-testid="message-text-content"]').filter({ hasText: text });
}

function rightMessageRow(page: Page, text: string) {
  return page.locator('[data-testid$="-right"]').filter({ hasText: text }).first();
}

async function assertNoHorizontalPageOverflow(page: Page) {
  await expect
    .poll(() =>
      page.evaluate(() => ({
        clientWidth: document.documentElement.clientWidth,
        scrollWidth: document.documentElement.scrollWidth,
      }))
    )
    .toEqual({
      clientWidth: await page.evaluate(() => document.documentElement.clientWidth),
      scrollWidth: await page.evaluate(() => document.documentElement.clientWidth),
    });
}
