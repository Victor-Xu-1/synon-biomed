import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import PreviewLoadingState from '@/renderer/components/media/PreviewLoadingState';
import SynonBiomedMcpAppViewer from './SynonBiomedMcpAppViewer';
import { ketcherMcpAppSaveContract } from './mcpAppSaveContracts';

const maxInteractiveArtifactBytes = 2 * 1024 * 1024;

type SynonBiomedMcpAppArtifactViewerProps = {
  filename: string;
  contentUrl: string;
  contentParam: 'ket' | 'rxn';
  rootFrameId?: string;
  frameId?: string;
  artifactId?: string;
};

const SynonBiomedMcpAppArtifactViewer: React.FC<SynonBiomedMcpAppArtifactViewerProps> = ({
  filename,
  contentUrl,
  contentParam,
  rootFrameId,
  frameId,
  artifactId,
}) => {
  const { t } = useTranslation();
  const [content, setContent] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    setContent(null);
    setFailed(false);
    void fetch(contentUrl, {
      credentials: 'include',
      signal: controller.signal,
      headers: { Accept: 'text/plain, application/json, chemical/*' },
    })
      .then(async (response) => {
        if (!response.ok) throw new Error('MCP_APP_ARTIFACT_UNAVAILABLE');
        return readBoundedArtifactText(response, maxInteractiveArtifactBytes);
      })
      .then((value) => {
        if (!controller.signal.aborted) setContent(value);
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true);
      });
    return () => controller.abort();
  }, [contentUrl]);

  if (failed) {
    return (
      <div className='size-full flex-center px-24px text-danger-6' role='alert'>
        {t('preview.scientific.mcpApp.artifactUnavailable')}
      </div>
    );
  }
  if (content === null) return <PreviewLoadingState label={t('preview.scientific.mcpApp.loadingArtifact')} />;
  return (
    <SynonBiomedMcpAppViewer
      serverId='bundled:ketcher-chemistry'
      serverName='Ketcher Chemistry'
      openTool='open_sketcher'
      resourceUri='ui://ketcher-chemistry/editor'
      docsUrl='https://github.com/epam/ketcher/blob/master/documentation/help.md#ketcher-overview'
      rootFrameId={rootFrameId}
      frameId={frameId}
      artifactId={artifactId}
      initialArguments={{ [contentParam]: content, filename }}
      saveContract={ketcherMcpAppSaveContract}
    />
  );
};

async function readBoundedArtifactText(response: Response, limit: number): Promise<string> {
  const declared = Number(response.headers.get('content-length') ?? 0);
  if (Number.isFinite(declared) && declared > limit) {
    await response.body?.cancel().catch((): void => {});
    throw new Error('MCP_APP_ARTIFACT_TOO_LARGE');
  }
  if (!response.body) return '';
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  try {
    while (true) {
      // oxlint-disable-next-line no-await-in-loop
      const { done, value } = await reader.read();
      if (done) break;
      if (!value) continue;
      total += value.byteLength;
      if (total > limit) throw new Error('MCP_APP_ARTIFACT_TOO_LARGE');
      chunks.push(value);
    }
  } finally {
    reader.releaseLock();
  }
  const bytes = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder('utf-8', { fatal: true }).decode(bytes);
}

export default SynonBiomedMcpAppArtifactViewer;
