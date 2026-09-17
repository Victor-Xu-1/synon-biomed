import { expect, test, type APIRequestContext, type Page } from '@playwright/test';
import { randomUUID } from 'node:crypto';
import { csrfHeaders, loginToScientificWorkbench } from './synonBiomedScientificFixture';

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('persists memory notes and enforces the scientific category contract', async ({ page }) => {
      await loginToScientificWorkbench(page);
      const headers = await csrfHeaders(page);
      const initialMemoryEnabled = await readMemoryEnabled(page.request);
      const suffix = `${viewport.name}-${randomUUID().slice(0, 8)}`;
      const note = `Memory Chrome acceptance ${suffix}`;
      const editedNote = `${note} refreshed`;
      const categoryName = `Methods ${suffix}`;
      const categoryGuidance = `Recall experimental methods for ${suffix}.`;
      const createdMemoryIds: string[] = [];
      const createdCategoryIds: string[] = [];
      let testError: unknown;

      try {
        await page.goto('/#/settings/governance', { waitUntil: 'domcontentloaded' });
        await expect(page.getByTestId('memory-manager')).toBeVisible();

        const enabled = page.getByTestId('memory-enabled-toggle');
        await expect(enabled).toBeVisible();
        await expect.poll(() => enabled.getAttribute('aria-checked')).toMatch(/^(true|false)$/);
        if ((await enabled.getAttribute('aria-checked')) !== 'true') {
          await expect(page.getByTestId('memory-disabled-enable')).toBeVisible();
          await page.getByTestId('memory-disabled-enable').click();
          await expect(enabled).toHaveAttribute('aria-checked', 'true');
        }

        await page.getByTestId('memory-add').click();
        await page.getByTestId('memory-new-input').fill(note);
        await page.getByTestId('memory-new-save').click();
        await expect(page.getByText(note, { exact: true })).toBeVisible();

        const createdMemory = await findMemoryByBody(page.request, note);
        createdMemoryIds.push(createdMemory.id);
        await page.reload({ waitUntil: 'domcontentloaded' });
        await expect(page.getByText(note, { exact: true })).toBeVisible();

        await page.getByTestId(`memory-row-edit-${createdMemory.id}`).click();
        const editor = page.getByTestId(`memory-row-${createdMemory.id}`).getByRole('textbox');
        await editor.fill(editedNote);
        await page
          .getByTestId(`memory-row-${createdMemory.id}`)
          .getByRole('button', { name: /保存|Save/ })
          .click();
        await expect(page.getByText(editedNote, { exact: true })).toBeVisible();

        await enabled.click();
        await expect(enabled).toHaveAttribute('aria-checked', 'false');
        await expect(page.getByText(editedNote, { exact: true })).toBeVisible();
        await expect(page.getByTestId(`memory-row-edit-${createdMemory.id}`)).toBeVisible();
        await page.getByTestId('memory-disabled-enable').click();
        await expect(enabled).toHaveAttribute('aria-checked', 'true');

        await page.getByTestId('memory-category-add').click();
        await expect(page.getByTestId('memory-category-draft-auto-recall')).toHaveAttribute('aria-checked', 'true');
        await page.getByTestId('memory-category-name').fill(categoryName);
        await expect(page.locator('#memory-category-save')).toBeDisabled();
        await page.getByTestId('memory-category-guidance').fill(categoryGuidance);
        await expect(page.locator('#memory-category-save')).toBeEnabled();
        await page.locator('#memory-category-save').click();

        const createdCategory = await findCategoryByName(page.request, categoryName);
        createdCategoryIds.push(createdCategory.id);
        expect(createdCategory.guidance).toBe(categoryGuidance);
        expect(createdCategory.auto_recall).toBe(true);

        const existingCategories = await listCategories(page.request);
        for (let index = existingCategories.length; index < 10; index += 1) {
          const response = await page.request.post('/api/memory/categories', {
            data: {
              name: `Limit ${suffix} ${index + 1}`,
              guidance: `Limit guidance ${suffix} ${index + 1}`,
            },
            headers,
          });
          expect(response.status(), `category ${index + 1} creation`).toBe(201);
          const category = (await response.json()) as { id?: unknown; auto_recall?: unknown };
          expect(category.id).toEqual(expect.any(String));
          expect(category.auto_recall).toBe(true);
          createdCategoryIds.push(String(category.id));
        }

        await page.reload({ waitUntil: 'domcontentloaded' });
        await expect(page.getByTestId('memory-category-count')).toHaveText(/10\/10$/);
        await expect(page.getByTestId('memory-category-add')).toBeDisabled();
        await assertNoHorizontalPageOverflow(page);
      } catch (error) {
        testError = error;
      }

      const cleanupErrors: unknown[] = [];
      for (const cleanup of [
        () => cleanupMemories(page.request, headers, createdMemoryIds),
        () => cleanupCategories(page.request, headers, createdCategoryIds),
        () => restoreMemoryEnabled(page.request, headers, initialMemoryEnabled),
      ]) {
        try {
          await cleanup();
        } catch (error) {
          cleanupErrors.push(error);
        }
      }
      const failures = [...(testError === undefined ? [] : [testError]), ...cleanupErrors];
      if (testError !== undefined && cleanupErrors.length === 0) throw testError;
      if (testError === undefined && cleanupErrors.length === 1) throw cleanupErrors[0];
      if (failures.length > 0) throw new AggregateError(failures, 'memory acceptance failed');
    });
  });
}

async function listCategories(request: APIRequestContext) {
  const response = await request.get('/api/memory/categories');
  expect(response.status()).toBe(200);
  return (await response.json()) as Array<{
    id: string;
    name: string;
    guidance: string;
    auto_recall: boolean;
  }>;
}

async function readMemoryEnabled(request: APIRequestContext): Promise<boolean> {
  const response = await request.get('/api/memory/enabled');
  expect(response.status()).toBe(200);
  const payload = (await response.json()) as { enabled?: unknown };
  if (typeof payload.enabled !== 'boolean') throw new Error('memory enabled response is invalid');
  return payload.enabled;
}

async function restoreMemoryEnabled(
  request: APIRequestContext,
  headers: Record<string, string>,
  enabled: boolean
): Promise<void> {
  const response = await request.put('/api/memory/enabled', { data: { enabled }, headers });
  if (!response.ok()) throw new Error(`memory enabled cleanup failed with HTTP ${response.status()}`);
}

async function findCategoryByName(request: APIRequestContext, name: string) {
  const categories = await listCategories(request);
  const category = categories.find((candidate) => candidate.name === name);
  if (!category) throw new Error('created memory category is absent from the authoritative API');
  return category;
}

async function findMemoryByBody(request: APIRequestContext, body: string) {
  const response = await request.get('/api/memory/context');
  expect(response.status()).toBe(200);
  const payload = (await response.json()) as {
    entities?: Array<{ rows?: Array<{ id?: unknown; body?: unknown }> }>;
  };
  const memory = payload.entities?.flatMap((entity) => entity.rows ?? []).find((row) => row.body === body);
  if (!memory || typeof memory.id !== 'string') {
    throw new Error('created memory is absent from the authoritative API');
  }
  return { id: memory.id };
}

async function cleanupMemories(
  request: APIRequestContext,
  headers: Record<string, string>,
  memoryIds: string[]
): Promise<void> {
  for (const memoryId of memoryIds) {
    const response = await request.delete(`/api/memories/${encodeURIComponent(memoryId)}`, { headers });
    if (!response.ok() && response.status() !== 404) {
      throw new Error(`memory cleanup failed with HTTP ${response.status()}`);
    }
  }
}

async function cleanupCategories(
  request: APIRequestContext,
  headers: Record<string, string>,
  categoryIds: string[]
): Promise<void> {
  for (const categoryId of categoryIds.toReversed()) {
    const response = await request.delete(
      `/api/memory/categories/${encodeURIComponent(categoryId)}?delete_facts=false`,
      { headers }
    );
    if (!response.ok() && response.status() !== 404) {
      throw new Error(`memory category cleanup failed with HTTP ${response.status()}`);
    }
  }
}

async function assertNoHorizontalPageOverflow(page: Page): Promise<void> {
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
