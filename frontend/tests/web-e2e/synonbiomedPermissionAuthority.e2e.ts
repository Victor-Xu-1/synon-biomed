import { expect, test, type Page } from '@playwright/test';
import { webPassword, webUsername } from './synonGoWebCredentials';

test('uses the bottom-bar permission selector as the active conversation authority', async ({ page }) => {
  await login(page);
  const csrfToken = (await page.context().cookies()).find((cookie) => cookie.name === 'synon_csrf')?.value;
  expect(csrfToken).toBeTruthy();
  const writeHeaders = { 'X-Synon-CSRF-Token': csrfToken! };
  const projectResponse = await page.request.post('/api/projects', {
    headers: writeHeaders,
    data: { name: `Permission authority ${Date.now()}`, description: 'Disposable permission authority fixture' },
  });
  expect(projectResponse.status()).toBe(201);
  const project = (await projectResponse.json()) as { project_id?: string };
  expect(project.project_id).toBeTruthy();
  const conversationResponse = await page.request.post('/api/conversations', {
    headers: writeHeaders,
    data: {
      name: 'Permission authority browser fixture',
      assistant: { id: 'synonbiomed:OPERON', locale: 'zh-CN', conversation_overrides: {} },
      extra: { project_id: project.project_id, project_name: 'Permission authority browser fixture' },
    },
  });
  expect(conversationResponse.status()).toBe(201);
  const conversation = (await conversationResponse.json()) as { id?: string };
  expect(conversation.id).toBeTruthy();

  try {
    await page.goto(`/#/conversation/${conversation.id}`);
    const selector = page.getByTestId('synon-biomed-permission-selector');
    await expect(selector).toBeVisible();
    await expect(selector).toHaveAttribute('data-current-permission', 'default');

    await selector.click();
    await page.getByTestId('synon-biomed-permission-option-bypassPermissions').click();
    await expect(selector).toHaveAttribute('data-current-permission', 'bypassPermissions');
    await expectRuntimePermission(page, conversation.id!, 'bypassPermissions');

    await selector.click();
    await page.getByTestId('synon-biomed-permission-option-default').click();
    await expect(selector).toHaveAttribute('data-current-permission', 'default');
    await expectRuntimePermission(page, conversation.id!, 'default');
  } finally {
    await page.request.delete(`/api/conversations/${conversation.id}`, { headers: writeHeaders });
    await page.request.delete(`/api/projects/${project.project_id}`, { headers: writeHeaders });
  }
});

async function expectRuntimePermission(page: Page, conversationId: string, expected: string) {
  const csrfToken = (await page.context().cookies()).find((cookie) => cookie.name === 'synon_csrf')?.value;
  const response = await page.request.post(`/api/conversations/${conversationId}/runtime/ensure`, {
    headers: { 'X-Synon-CSRF-Token': csrfToken ?? '' },
    data: {},
  });
  expect(response.ok(), await response.text()).toBe(true);
  const payload = (await response.json()) as { config_options?: Array<{ id?: string; current_value?: string }> };
  const mode = payload.config_options?.find((option) => option.id === 'mode');
  expect(mode?.current_value).toBe(expected);
}

async function login(page: Page) {
  await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
  await page.getByRole('combobox').selectOption('zh-CN');
  await page.getByRole('textbox', { name: '用户名' }).fill(webUsername);
  await page.getByRole('textbox', { name: '密码' }).fill(webPassword);
  await page.getByRole('button', { name: '登录' }).click();
  await expect(page).toHaveURL(/#\/guid/);
}
