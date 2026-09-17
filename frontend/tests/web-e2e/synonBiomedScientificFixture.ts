import { randomUUID } from 'node:crypto';
import { Buffer } from 'node:buffer';
import { expect, type Locator, type Page } from '@playwright/test';
import { webPassword, webUsername } from './synonGoWebCredentials';
import { appendSynonGoAssistantMessage, beginSynonGoTranscriptStream } from './synonGoFrameFixture';

export type ScientificWorkspaceFixture = {
  conversationId: string;
  projectId: string;
  projectName: string;
};

export type UploadedArtifact = {
  artifactId: string;
  versionId: string;
  conversationId: string;
  filename: string;
};

export async function loginToScientificWorkbench(page: Page) {
  await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
  await page.getByRole('combobox').selectOption('zh-CN');
  await page.locator('input[name="username"]').fill(webUsername);
  await page.locator('input[name="password"]').fill(webPassword);
  await page.locator('button[type="submit"]').click();
  await expect(page).toHaveURL(/#\/guid/);
}

export async function createScientificWorkspace(page: Page, label: string): Promise<ScientificWorkspaceFixture> {
  const suffix = `${label}-${randomUUID().slice(0, 8)}`;
  const projectName = `P5 scientific ${suffix}`;
  const headers = await csrfHeaders(page);
  const projectResponse = await page.request.post('/api/projects', {
    data: {
      name: projectName,
      description: 'Disposable self-contained P5 scientific browser fixture.',
      context: 'Use only artifacts created by this acceptance test.',
    },
    headers,
  });
  if (projectResponse.status() !== 201) {
    throw new Error(`Project fixture creation failed: ${projectResponse.status()} ${await projectResponse.text()}`);
  }
  const project = (await projectResponse.json()) as { project_id?: string };
  if (!project.project_id) throw new Error('Project fixture response did not include project_id');

  const conversationResponse = await page.request.post('/api/conversations', {
    data: {
      name: `P5 preview ${suffix}`,
      assistant: { id: 'synonbiomed:OPERON', locale: 'zh-CN', conversation_overrides: {} },
      extra: { project_id: project.project_id, project_name: projectName },
    },
    headers,
  });
  if (conversationResponse.status() !== 201) {
    await page.request.delete(`/api/projects/${encodeURIComponent(project.project_id)}`, {
      headers,
    });
    throw new Error(
      `Conversation fixture creation failed: ${conversationResponse.status()} ${await conversationResponse.text()}`
    );
  }
  const conversation = (await conversationResponse.json()) as { id?: string };
  if (!conversation.id) throw new Error('Conversation fixture response did not include id');
  return { conversationId: conversation.id, projectId: project.project_id, projectName };
}

export async function uploadScientificArtifact(
  page: Page,
  workspace: ScientificWorkspaceFixture,
  fixture: { filename: string; contentType: string; source: string | Uint8Array }
): Promise<UploadedArtifact> {
  const payload = Buffer.from(fixture.source);
  const headers = await csrfHeaders(page);
  const workspaceBase = `/api/conversations/${encodeURIComponent(workspace.conversationId)}/workspace`;
  const initResponse = await page.request.post(`${workspaceBase}/uploads`, {
    data: {
      filename: fixture.filename,
      total_size: payload.length,
      content_type: fixture.contentType,
      chunk_size: 1024 * 1024,
    },
    headers,
  });
  if (!initResponse.ok()) throw new Error(`Upload init failed: ${initResponse.status()} ${await initResponse.text()}`);
  const upload = (await initResponse.json()) as { upload_id?: string };
  if (!upload.upload_id) throw new Error('Upload init did not return upload_id');

  const chunkResponse = await page.request.post(
    `${workspaceBase}/uploads/${encodeURIComponent(upload.upload_id)}/chunks/0`,
    {
      data: payload,
      headers: { ...headers, 'content-type': 'application/octet-stream' },
    }
  );
  if (!chunkResponse.ok()) {
    throw new Error(`Chunk upload failed: ${chunkResponse.status()} ${await chunkResponse.text()}`);
  }

  const finalizeResponse = await page.request.post(
    `${workspaceBase}/uploads/${encodeURIComponent(upload.upload_id)}/finalize`,
    { headers }
  );
  if (!finalizeResponse.ok()) {
    throw new Error(`Upload finalize failed: ${finalizeResponse.status()} ${await finalizeResponse.text()}`);
  }
  const artifact = (await finalizeResponse.json()) as {
    artifact_id?: string;
    id?: string;
    version_id?: string;
  };
  const artifactId = artifact.artifact_id ?? artifact.id;
  const versionId = artifact.version_id;
  if (!artifactId || !versionId) throw new Error('Upload finalize did not return an artifact and version id');
  return {
    conversationId: workspace.conversationId,
    artifactId,
    versionId,
    filename: fixture.filename,
  };
}

export async function showArtifactReferenceAssistantMessage(
  page: Page,
  workspace: ScientificWorkspaceFixture,
  artifacts: Array<{ versionId: string; label: string }>
) {
  const content = artifacts.map((artifact) => `[${artifact.label}]({{artifact:${artifact.versionId}}})`).join('\n\n');
  const stream = beginSynonGoTranscriptStream(workspace.conversationId);
  await page.goto(`/#/conversation/${workspace.conversationId}`, { waitUntil: 'domcontentloaded' });
  await expect(page.getByTestId('message-list-scroller')).toBeVisible();
  appendSynonGoAssistantMessage(stream, content);
}

export async function removeScientificWorkspace(page: Page, fixture: ScientificWorkspaceFixture) {
  const headers = await csrfHeaders(page);
  const conversation = await page.request.delete(`/api/conversations/${encodeURIComponent(fixture.conversationId)}`, {
    headers,
  });
  const project = await page.request.delete(`/api/projects/${encodeURIComponent(fixture.projectId)}`, { headers });
  const failures = [
    ['conversation', conversation],
    ['project', project],
  ].filter(([, response]) => !(response as Awaited<typeof conversation>).ok());
  if (failures.length > 0) {
    const details = await Promise.all(
      failures.map(async ([name, response]) => {
        const typed = response as Awaited<typeof conversation>;
        return `${name}:${typed.status()} ${await typed.text()}`;
      })
    );
    throw new Error(`Scientific fixture cleanup failed: ${details.join('; ')}`);
  }
}

export async function openScientificArtifact(page: Page, artifactId: string) {
  await page.evaluate((nextArtifactId) => {
    window.location.hash = `/artifacts/${nextArtifactId}`;
  }, artifactId);
  await expect(page).toHaveURL(new RegExp(`#/artifacts/${artifactId}$`));
}

export async function inspectRenderedPixels(locator: Locator) {
  const screenshot = await locator.screenshot({ animations: 'disabled' });
  return locator.evaluate(async (_root, encodedPng) => {
    const binary = window.atob(encodedPng);
    const bytes = new Uint8Array(binary.length);
    for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index);
    const bitmap = await createImageBitmap(new Blob([bytes], { type: 'image/png' }));
    try {
      const scale = Math.min(1, 2048 / bitmap.width, 2048 / bitmap.height);
      const width = Math.max(1, Math.round(bitmap.width * scale));
      const height = Math.max(1, Math.round(bitmap.height * scale));
      const canvas = document.createElement('canvas');
      canvas.width = width;
      canvas.height = height;
      const context = canvas.getContext('2d', { willReadFrequently: true });
      if (!context) throw new Error('Visible-pixel inspection requires a 2D canvas context');
      context.fillStyle = '#ffffff';
      context.fillRect(0, 0, width, height);
      context.drawImage(bitmap, 0, 0, width, height);
      const data = context.getImageData(0, 0, width, height).data;
      let nonWhite = 0;
      let chromatic = 0;
      for (let index = 0; index + 3 < data.length; index += 4) {
        const red = data[index];
        const green = data[index + 1];
        const blue = data[index + 2];
        if (red < 245 || green < 245 || blue < 245) nonWhite += 1;
        if (Math.max(red, green, blue) - Math.min(red, green, blue) > 20) chromatic += 1;
      }
      return { width, height, nonWhite, chromatic };
    } finally {
      bitmap.close();
    }
  }, screenshot.toString('base64'));
}

export async function csrfHeaders(page: Page): Promise<Record<string, string>> {
  const cookies = await page.context().cookies();
  const token = cookies.find((cookie) => cookie.name === 'synon_csrf')?.value;
  if (!token) throw new Error('Authenticated browser context has no synon_csrf cookie');
  return { 'X-Synon-CSRF-Token': decodeURIComponent(token) };
}
