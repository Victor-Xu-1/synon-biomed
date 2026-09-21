import { randomUUID } from 'node:crypto';
import { expect, test } from './officialChromeTest';

test.use({ viewport: { width: 1600, height: 775 } });

test('can leave first-use onboarding and switch accounts', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' });
  await expect(page).toHaveURL(/#\/login/);

  const accountIdentity = randomUUID().replaceAll('-', '').slice(0, 12);
  const accountEmail = `switch-${accountIdentity}@example.test`;
  const accountPassword = `Switch-${accountIdentity}-A9!`;
  await page.getByRole('button', { name: /Create new account|创建新账户/ }).click();
  await page.locator('input[name="username"]').fill(`Switch ${accountIdentity}`);
  await page.locator('input[name="email"]').fill(accountEmail);
  await page.locator('input[name="password"]').fill(accountPassword);
  await page.locator('button[type="submit"]').click();

  await expect(page).toHaveURL(/#\/onboarding/);
  await expect(page.getByTestId('onboarding-welcome')).toBeVisible();
  await expect(page.getByTestId('onboarding-switch-account')).toBeEnabled();
  await page.setViewportSize({ width: 390, height: 844 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth)).toBe(390);
  await expect(page.getByTestId('onboarding-switch-account')).toBeInViewport();
  await page.getByTestId('onboarding-switch-account').click();

  await expect(page).toHaveURL(/#\/login/);
  await expect(page.getByRole('textbox', { name: /Username|用户名/ })).toBeVisible();
  const currentUser = await page.request.get('/api/auth/user');
  expect(currentUser.status()).toBe(401);
});

test('fresh authenticated user completes accessible onboarding and enters a recoverable workspace', async ({
  page,
}) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' });
  await expect(page).toHaveURL(/#\/login/);
  const accountIdentity = randomUUID().replaceAll('-', '').slice(0, 12);
  const accountEmail = `onboarding-${accountIdentity}@example.test`;
  const accountPassword = `Onboarding-${accountIdentity}-A9!`;
  let firstUseFailureInjected = false;
  await page.route('**/api/preferences/first-run-onboarding', async (route) => {
    if (route.request().method() === 'GET' && !firstUseFailureInjected) {
      firstUseFailureInjected = true;
      await route.fulfill({
        status: 503,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'SERVICE_UNAVAILABLE' }),
      });
      return;
    }
    await route.continue();
  });
  await page.getByRole('button', { name: /Create new account|创建新账户/ }).click();
  await page.locator('input[name="username"]').fill(`Researcher ${accountIdentity}`);
  await page.locator('input[name="email"]').fill(accountEmail);
  await page.locator('input[name="password"]').fill(accountPassword);
  const registrationPromise = page.waitForResponse(
    (response) => new URL(response.url()).pathname === '/api/auth/register' && response.request().method() === 'POST'
  );
  await page.locator('button[type="submit"]').click();
  const registration = await registrationPromise;
  expect(registration.status()).toBe(200);
  const registered = (await registration.json()) as { user?: { id?: unknown; email?: unknown } };
  expect(typeof registered.user?.id).toBe('string');
  expect(registered.user?.email).toBe(accountEmail);
  await expect(page.getByRole('heading', { name: /Unable to load setup|无法加载初始化配置/ })).toBeVisible();
  await expect(page.getByRole('alert')).not.toContainText('SERVICE_UNAVAILABLE');
  await page.getByRole('button', { name: /Retry|重试/ }).click();
  expect(firstUseFailureInjected).toBe(true);
  await expect(page).toHaveURL(/#\/onboarding/);
  await expect(page.getByTestId('onboarding-welcome')).toBeVisible();
  const initialCompletion = await page.request.get('/api/preferences/first-run-onboarding');
  expect(initialCompletion.status()).toBe(200);
  expect(await initialCompletion.json()).toEqual({ complete: false });

  const excludedAuthority = await page.evaluate(async () => {
    const detailResponse = await fetch('/api/assistants/synonbiomed%3Aoperon', { credentials: 'include' });
    if (!detailResponse.ok) throw new Error(`assistant authority failed: ${detailResponse.status}`);
    const detail = (await detailResponse.json()) as {
      data?: { capabilities?: { allowed_mcp_ids?: unknown; allowed_skill_ids?: unknown } };
    };
    const skillsResponse = await fetch('/api/skills/catalog', { credentials: 'include' });
    if (!skillsResponse.ok) throw new Error(`skill catalog failed: ${skillsResponse.status}`);
    const skillCatalog = (await skillsResponse.json()) as {
      skills?: Array<{ name?: unknown; userHidden?: unknown }>;
    };
    const visibleSkills = new Set(
      (skillCatalog.skills ?? [])
        .filter((skill) => skill.userHidden !== true && typeof skill.name === 'string')
        .map((skill) => skill.name as string)
    );
    const mcpIds = detail.data?.capabilities?.allowed_mcp_ids;
    const skillIds = detail.data?.capabilities?.allowed_skill_ids;
    if (!Array.isArray(mcpIds) || typeof mcpIds[0] !== 'string') throw new Error('no MCP authority fixture');
    if (!Array.isArray(skillIds)) throw new Error('no skill authority fixture');
    const connectorId = mcpIds[0];
    const skillName = skillIds.find((name): name is string => typeof name === 'string' && visibleSkills.has(name));
    if (!skillName) throw new Error('no detachable skill authority fixture');
    const connectorPath = `/api/agents/OPERON/connectors/${encodeURIComponent(connectorId)}`;
    const connectorResponse = await fetch(connectorPath, { method: 'DELETE', credentials: 'include' });
    if (!connectorResponse.ok) {
      throw new Error(
        `authority tombstone failed for ${connectorPath}: ${connectorResponse.status} ${await connectorResponse.text()}`
      );
    }
    const skillResponse = await fetch('/api/assistants/synonbiomed%3Aoperon', {
      method: 'PUT',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ disabled_builtin_skills: [skillName] }),
    });
    if (!skillResponse.ok) {
      throw new Error(`authority skill exclusion failed: ${skillResponse.status} ${await skillResponse.text()}`);
    }
    return {
      connectorId,
      skillName,
      connectorCount: mcpIds.length,
    };
  });
  await page.reload({ waitUntil: 'domcontentloaded' });
  await expect(page.getByTestId('onboarding-welcome')).toBeVisible();

  const continueButton = page.getByRole('button', { name: /Continue|继续/ });
  await page.keyboard.press('Tab');
  await expect(continueButton).toBeFocused();
  const focusStyle = await continueButton.evaluate((element) => {
    const style = getComputedStyle(element);
    return { outlineStyle: style.outlineStyle, outlineWidth: style.outlineWidth };
  });
  expect(focusStyle.outlineStyle).not.toBe('none');
  expect(Number.parseFloat(focusStyle.outlineWidth)).toBeGreaterThanOrEqual(2);

  await continueButton.click();
  await expect(page.getByTestId('onboarding-network')).toBeVisible();
  await continueButton.click();
  const capabilityStep = page.getByTestId('onboarding-capabilities');
  await expect(capabilityStep).toBeVisible();

  const panel = page.getByRole('tabpanel');
  await expect(page.getByRole('tab', { name: /Connectors|连接器/ })).toHaveAttribute('aria-selected', 'true');
  await expect(panel.getByRole('switch')).toHaveCount(excludedAuthority.connectorCount);
  await capabilityStep.hover();
  await page.mouse.wheel(0, 10_000);
  await expect(panel.getByRole('switch').last()).toBeInViewport();

  const skillsTab = page.getByRole('tab', { name: /Skills|技能/ });
  await skillsTab.click();
  const skillCount = panel.getByText(/^\d+ \/ \d+$/);
  await expect(skillCount).toBeVisible();
  const totalSkillCount = Number((await skillCount.textContent())?.split('/')[1]?.trim());
  expect(totalSkillCount).toBeGreaterThan(0);
  await expect(panel.getByRole('switch')).toHaveCount(totalSkillCount);
  await expect(page.getByText('BioMart', { exact: true })).toHaveCount(0);
  const lastSkill = panel.getByRole('switch').last();
  await lastSkill.focus();
  await expect(lastSkill).toBeFocused();
  await expect(lastSkill).toBeInViewport();

  await page.setViewportSize({ width: 390, height: 844 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth)).toBe(390);
  await capabilityStep.evaluate((element) => {
    element.scrollTop = 0;
  });
  const touchClient = await page.context().newCDPSession(page);
  await touchClient.send('Emulation.setTouchEmulationEnabled', { enabled: true, maxTouchPoints: 1 });
  const scrollBox = await capabilityStep.boundingBox();
  if (!scrollBox) throw new Error('capability scroll owner is not measurable');
  const x = Math.round(scrollBox.x + scrollBox.width / 2);
  const startY = Math.round(scrollBox.y + scrollBox.height - 28);
  const endY = Math.round(scrollBox.y + 44);
  for (let swipe = 0; swipe < 32; swipe += 1) {
    const lastVisible = await lastSkill.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      return rect.bottom > 0 && rect.top < window.innerHeight;
    });
    if (lastVisible) break;
    await touchClient.send('Input.dispatchTouchEvent', {
      type: 'touchStart',
      touchPoints: [{ x, y: startY }],
    });
    await touchClient.send('Input.dispatchTouchEvent', {
      type: 'touchMove',
      touchPoints: [{ x, y: endY }],
    });
    await touchClient.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  }
  await expect(lastSkill).toBeInViewport();
  expect(await capabilityStep.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
  expect(await page.evaluate(() => window.scrollY)).toBe(0);
  await page.setViewportSize({ width: 320, height: 568 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth)).toBe(320);
  await expect(page.getByRole('button', { name: /Continue|继续/ })).toBeInViewport();
  await page.setViewportSize({ width: 1600, height: 775 });

  await continueButton.click();
  const profile = page.getByTestId('onboarding-profile-summary');
  await profile.fill('Translational genomics researcher');
  await page.locator('input[type="file"]').setInputFiles({
    name: 'cohort.csv',
    mimeType: 'text/csv',
    buffer: Buffer.from('sample,outcome\nA,1\n'),
  });
  await expect(page.getByText('cohort.csv', { exact: true })).toBeVisible();

  await page.reload({ waitUntil: 'domcontentloaded' });
  await expect(page.getByTestId('onboarding-profile')).toBeVisible();
  await expect(page.getByTestId('onboarding-profile-summary')).toHaveValue('Translational genomics researcher');
  await expect(page.getByTestId('onboarding-restored-files')).toContainText('cohort.csv');
  await page.locator('input[type="file"]').setInputFiles({
    name: 'cohort.csv',
    mimeType: 'text/csv',
    buffer: Buffer.from('sample,outcome\nA,1\n'),
  });

  await continueButton.click();
  const task = 'Summarize the uploaded cohort design without external model calls';
  await page.getByTestId('onboarding-task-custom').fill(task);
  const launchOutcomePromise = Promise.race([
    page
      .waitForResponse((response) => {
        const url = new URL(response.url());
        return url.pathname === '/api/conversations' && response.request().method() === 'POST';
      })
      .then((response) => ({ kind: 'conversation' as const, response })),
    page
      .getByTestId('onboarding-launch-error')
      .waitFor({ state: 'visible' })
      .then(async () => ({
        kind: 'launch-error' as const,
        text: await page.getByTestId('onboarding-launch-error').innerText(),
      })),
  ]);
  await page.getByTestId('onboarding-start').click();

  const launchOutcome = await launchOutcomePromise;
  if (launchOutcome.kind === 'launch-error') {
    throw new Error(`onboarding launch failed before conversation creation: ${launchOutcome.text}`);
  }
  const conversationResponse = launchOutcome.response;
  expect(conversationResponse.status()).toBe(201);
  const createRequest = conversationResponse.request().postDataJSON() as {
    assistant?: { conversation_overrides?: { mcp_ids?: unknown; skill_ids?: unknown } };
  };
  expect(createRequest.assistant?.conversation_overrides?.mcp_ids).not.toContain(excludedAuthority.connectorId);
  expect(createRequest.assistant?.conversation_overrides).not.toHaveProperty('skill_ids');
  const created = (await conversationResponse.json()) as {
    id?: unknown;
    extra?: { project_id?: unknown };
  };
  expect(typeof created.id).toBe('string');
  expect(typeof created.extra?.project_id).toBe('string');
  const conversationId = created.id as string;
  const projectId = created.extra?.project_id as string;
  await expect(page).toHaveURL(/#\/conversation\//);
  expect(page.url()).toContain(`/conversation/${encodeURIComponent(conversationId)}`);
  const composer = page.getByRole('textbox', { name: /Send a message|发消息|输入你的问题/ });
  await expect(composer).toBeVisible();

  const persistedState = async () =>
    page.evaluate(
      async ({ currentConversationId, currentProjectId }) => {
        const getJSON = async (path: string) => {
          const response = await fetch(path, { credentials: 'include' });
          if (!response.ok) throw new Error(`GET ${path} failed: ${response.status}`);
          return response.json();
        };
        const [conversation, messages, project, artifacts] = await Promise.all([
          getJSON(`/api/conversations/${encodeURIComponent(currentConversationId)}`),
          getJSON(`/api/conversations/${encodeURIComponent(currentConversationId)}/messages?limit=50`),
          getJSON(`/api/projects/${encodeURIComponent(currentProjectId)}`),
          getJSON(`/api/projects/${encodeURIComponent(currentProjectId)}/artifacts`),
        ]);
        return { conversation, messages, project, artifacts };
      },
      { currentConversationId: conversationId, currentProjectId: projectId }
    );

  await expect.poll(async () => JSON.stringify((await persistedState()).messages), { timeout: 30_000 }).toContain(task);
  const beforeRefresh = await persistedState();
  expect(beforeRefresh.conversation).toMatchObject({ id: conversationId });
  expect(beforeRefresh.conversation.extra).toMatchObject({ project_id: projectId });
  expect(JSON.stringify(beforeRefresh.project)).toContain('Translational genomics researcher');
  expect(JSON.stringify(beforeRefresh.project)).toContain('cohort.csv');
  expect(JSON.stringify(beforeRefresh.artifacts)).toContain('cohort.csv');
  expect(JSON.stringify(beforeRefresh.artifacts)).toContain('onboarding-profile.md');
  const firstTaskMessage = (beforeRefresh.messages as { items?: Array<Record<string, unknown>> }).items?.find((item) =>
    JSON.stringify(item.content).includes(task)
  );
  const firstTaskRefs = firstTaskMessage?.artifact_refs as
    | Array<{
        artifact_id?: unknown;
        version_id?: unknown;
        filename?: unknown;
      }>
    | undefined;
  expect(firstTaskRefs).toHaveLength(2);
  expect(firstTaskRefs?.[0]).toMatchObject({ filename: 'onboarding-profile.md' });
  expect(firstTaskRefs?.[1]).toMatchObject({ filename: 'cohort.csv' });
  expect(typeof firstTaskRefs?.[0]?.artifact_id).toBe('string');
  expect(typeof firstTaskRefs?.[0]?.version_id).toBe('string');
  expect(typeof firstTaskRefs?.[1]?.artifact_id).toBe('string');
  expect(typeof firstTaskRefs?.[1]?.version_id).toBe('string');
  expect(JSON.stringify(beforeRefresh.artifacts)).toContain(String(firstTaskRefs?.[0]?.artifact_id));
  expect(JSON.stringify(beforeRefresh.artifacts)).toContain(String(firstTaskRefs?.[0]?.version_id));
  expect(JSON.stringify(beforeRefresh.artifacts)).toContain(String(firstTaskRefs?.[1]?.artifact_id));
  expect(JSON.stringify(beforeRefresh.artifacts)).toContain(String(firstTaskRefs?.[1]?.version_id));

  const conversationURL = page.url();
  await page.reload({ waitUntil: 'domcontentloaded' });
  await expect(page).toHaveURL(conversationURL);
  await expect(page.getByRole('textbox', { name: /Send a message|发消息|输入你的问题/ })).toBeVisible();
  const afterRefresh = await persistedState();
  expect(afterRefresh.conversation).toMatchObject({ id: conversationId });
  expect(afterRefresh.conversation.extra).toMatchObject({ project_id: projectId });
  expect(JSON.stringify(afterRefresh.messages)).toContain(task);
  expect(JSON.stringify(afterRefresh.artifacts)).toContain('cohort.csv');
  expect(JSON.stringify(afterRefresh.messages)).toContain(String(firstTaskRefs?.[0]?.version_id));
  expect(JSON.stringify(afterRefresh.artifacts)).toContain('onboarding-profile.md');
  const finalCompletion = await page.request.get('/api/preferences/first-run-onboarding');
  expect(finalCompletion.status()).toBe(200);
  expect(await finalCompletion.json()).toEqual({ complete: true });
});
