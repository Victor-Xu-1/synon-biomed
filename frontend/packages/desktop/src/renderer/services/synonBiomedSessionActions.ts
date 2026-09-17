import {
  isSynonBiomedHttpError,
  requestSynonBiomedJson,
  type SynonBiomedGatewayOptions,
} from '@/renderer/services/synonBiomedHttp';
import { invalidateSynonBiomedFrameReads } from './synonBiomedFrameReads';

export type SynonBiomedSessionUpdate = {
  name: string;
  taskSummary: string;
};

export async function updateSynonBiomedSession(
  frameId: string,
  update: SynonBiomedSessionUpdate,
  options: SynonBiomedGatewayOptions = {}
): Promise<void> {
  await requestSynonBiomedJson(
    `/api/frames/${encodeURIComponent(frameId)}`,
    {
      method: 'PATCH',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ name: update.name, task_summary: update.taskSummary }),
    },
    options
  );
}

export async function moveSynonBiomedSession(
  frameId: string,
  projectId: string,
  options: SynonBiomedGatewayOptions = {}
): Promise<void> {
  await requestSynonBiomedJson(
    `/api/frames/${encodeURIComponent(frameId)}/move`,
    {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ target_project_id: projectId }),
    },
    options
  );
}

export async function deleteSynonBiomedSession(
  frameId: string,
  options: SynonBiomedGatewayOptions = {}
): Promise<void> {
  try {
    await requestSynonBiomedJson(`/api/frames/${encodeURIComponent(frameId)}`, { method: 'DELETE' }, options);
  } catch (error) {
    // A realtime event or another client may have removed the frame after the
    // sidebar was rendered. Treat that already-absent state as an idempotent
    // success so a repeated click cannot leave a ghost row behind.
    if (!isSynonBiomedHttpError(error) || error.status !== 404) throw error;
  } finally {
    // A failed delete must also invalidate reads: the next refresh should
    // prove whether the frame still exists instead of replaying stale data.
    invalidateSynonBiomedFrameReads(frameId);
  }
}

export function getSynonBiomedSessionExportUrl(frameId: string): string {
  return `/api/frames/${encodeURIComponent(frameId)}/export`;
}

export function getSynonBiomedSessionArtifactsDownloadUrl(frameId: string): string {
  return `/api/frames/${encodeURIComponent(frameId)}/artifacts/download?include_metadata=true`;
}
