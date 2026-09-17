import { afterEach, describe, expect, it } from 'vitest';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

const webBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';

type Project = { project_id: string };
type Artifact = { id: string; version_id: string; root_frame_id?: string; frame_id?: string };
type Annotation = {
  id: string;
  type?: string;
  text: string;
  content_checksum?: string | null;
  x_percent?: number | null;
  y_percent?: number | null;
  selection_text?: string | null;
  element_selector?: string | null;
  element_descriptor?: string | null;
};
type TranscriptAnnotation = {
  id: string;
  note: string;
  anchor_text?: string;
  start_offset?: number | null;
  end_offset?: number | null;
  read_at?: string | null;
};

let authenticatedFetch: Awaited<ReturnType<typeof createSynonBiomedTestFetch>>;
const artifactCleanup: Array<{ artifactId: string; versionId: string; annotationId: string }> = [];
let transcriptCleanup: { frameId: string; annotationId: string } | null = null;

async function request(path: string, init?: RequestInit): Promise<Response> {
  const response = await authenticatedFetch(`${webBaseUrl}${path}`, init);
  if (!response.ok) {
    throw new Error(`${init?.method ?? 'GET'} ${path} failed: ${response.status} ${await response.text()}`);
  }
  return response;
}

async function requestJson<T>(path: string, init?: RequestInit): Promise<T> {
  return (await request(path, init)).json() as Promise<T>;
}

async function findAnnotatableArtifact(projects: Project[], index = 0): Promise<Artifact | undefined> {
  const project = projects[index];
  if (!project) return undefined;

  const artifacts = await requestJson<Artifact[]>(`/api/projects/${encodeURIComponent(project.project_id)}/artifacts`);
  const artifact = artifacts.find((item) => item.version_id && (item.root_frame_id || item.frame_id));
  return artifact ?? findAnnotatableArtifact(projects, index + 1);
}

async function cleanup(): Promise<void> {
  await Promise.all(
    artifactCleanup
      .splice(0)
      .map(({ artifactId, versionId, annotationId }) =>
        request(
          `/api/artifacts/${encodeURIComponent(artifactId)}/versions/${encodeURIComponent(versionId)}/annotations/${encodeURIComponent(annotationId)}`,
          { method: 'DELETE' }
        ).catch(() => undefined)
      )
  );
  if (transcriptCleanup) {
    const { frameId, annotationId } = transcriptCleanup;
    await request(
      `/api/frames/${encodeURIComponent(frameId)}/transcript-annotations/${encodeURIComponent(annotationId)}`,
      { method: 'DELETE' }
    ).catch(() => undefined);
    transcriptCleanup = null;
  }
}

afterEach(cleanup);

describe('Synon Biomed real annotation lifecycle', () => {
  it('persists artifact and transcript annotations through the authenticated SynonAI WebHost', async () => {
    authenticatedFetch = await createSynonBiomedTestFetch(webBaseUrl);
    const projects = await requestJson<{ projects: Project[] }>('/api/projects');
    const artifact = await findAnnotatableArtifact(projects.projects);
    expect(artifact).toBeDefined();
    const frameId = artifact!.root_frame_id ?? artifact!.frame_id!;
    const suffix = Date.now().toString(36);

    try {
      const artifactAnnotation = await requestJson<Annotation>(
        `/api/artifacts/${encodeURIComponent(artifact!.id)}/versions/${encodeURIComponent(artifact!.version_id)}/annotations`,
        {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({
            type: 'point',
            x_percent: 17.5,
            y_percent: 42.5,
            text: `SynonAI artifact annotation ${suffix}`,
          }),
        }
      );
      artifactCleanup.push({
        artifactId: artifact!.id,
        versionId: artifact!.version_id,
        annotationId: artifactAnnotation.id,
      });
      expect(artifactAnnotation.content_checksum).toBeTruthy();

      const artifactRows = await requestJson<{ annotations: Annotation[] }>(
        `/api/artifacts/${encodeURIComponent(artifact!.id)}/versions/${encodeURIComponent(artifact!.version_id)}/annotations`
      );
      expect(artifactRows.annotations.some((item) => item.id === artifactAnnotation.id)).toBe(true);

      const updatedArtifact = await requestJson<Annotation>(
        `/api/annotations/${encodeURIComponent(artifactAnnotation.id)}`,
        {
          method: 'PATCH',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ text: `SynonAI artifact annotation updated ${suffix}` }),
        }
      );
      expect(updatedArtifact.text).toBe(`SynonAI artifact annotation updated ${suffix}`);

      const htmlElementAnnotation = await requestJson<Annotation>(
        `/api/artifacts/${encodeURIComponent(artifact!.id)}/versions/${encodeURIComponent(artifact!.version_id)}/annotations`,
        {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({
            type: 'html_element',
            x_percent: 61.25,
            y_percent: 37.5,
            selection_text: 'STAT6 result row',
            element_selector: '#result-table > tbody > tr:nth-of-type(2)',
            element_descriptor: 'tr — STAT6 result row',
            text: `SynonAI HTML element annotation ${suffix}`,
          }),
        }
      );
      artifactCleanup.push({
        artifactId: artifact!.id,
        versionId: artifact!.version_id,
        annotationId: htmlElementAnnotation.id,
      });
      expect(htmlElementAnnotation).toMatchObject({
        type: 'html_element',
        x_percent: 61.25,
        y_percent: 37.5,
        selection_text: 'STAT6 result row',
        element_selector: '#result-table > tbody > tr:nth-of-type(2)',
        element_descriptor: 'tr — STAT6 result row',
      });

      const transcriptAnnotation = await requestJson<TranscriptAnnotation>(
        `/api/frames/${encodeURIComponent(frameId)}/transcript-annotations`,
        {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({
            message_index: 0,
            block_index: 0,
            source: 'assistant',
            anchor_text: 'transcript annotation',
            start_offset: 7,
            end_offset: 28,
            kind: 'annotation',
            note: `SynonAI transcript bookmark ${suffix}`,
          }),
        }
      );
      transcriptCleanup = { frameId, annotationId: transcriptAnnotation.id };
      expect(transcriptAnnotation).toMatchObject({
        anchor_text: 'transcript annotation',
        start_offset: 7,
        end_offset: 28,
      });

      const transcriptRows = await requestJson<TranscriptAnnotation[]>(
        `/api/frames/${encodeURIComponent(frameId)}/transcript-annotations`
      );
      expect(transcriptRows.find((item) => item.id === transcriptAnnotation.id)).toMatchObject({
        anchor_text: 'transcript annotation',
        start_offset: 7,
        end_offset: 28,
      });

      const updatedTranscript = await requestJson<TranscriptAnnotation>(
        `/api/frames/${encodeURIComponent(frameId)}/transcript-annotations/${encodeURIComponent(transcriptAnnotation.id)}`,
        {
          method: 'PATCH',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ note: `SynonAI transcript bookmark updated ${suffix}`, read: true }),
        }
      );
      expect(updatedTranscript.note).toBe(`SynonAI transcript bookmark updated ${suffix}`);
      expect(updatedTranscript.read_at).toBeTruthy();

      await cleanup();

      const finalArtifactRows = await requestJson<{ annotations: Annotation[] }>(
        `/api/artifacts/${encodeURIComponent(artifact!.id)}/versions/${encodeURIComponent(artifact!.version_id)}/annotations`
      );
      expect(finalArtifactRows.annotations.some((item) => item.id === artifactAnnotation.id)).toBe(false);
      expect(finalArtifactRows.annotations.some((item) => item.id === htmlElementAnnotation.id)).toBe(false);
      const finalTranscriptRows = await requestJson<TranscriptAnnotation[]>(
        `/api/frames/${encodeURIComponent(frameId)}/transcript-annotations`
      );
      expect(finalTranscriptRows.some((item) => item.id === transcriptAnnotation.id)).toBe(false);
    } finally {
      await cleanup();
    }
  }, 30_000);
});
