import { resolveSynonBiomedArtifactPreviewPlan } from '@/renderer/services/synonBiomedArtifactPreview';
import { loadSynonBiomedArtifact } from '@/renderer/services/synonBiomedGateway';
import { afterEach, describe, expect, it } from 'vitest';
import { createRealConversationFixture, type RealConversationFixture } from './synonbiomedRealConversationFixture';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
const fixture = [
  '##fileformat=VCFv4.2',
  '##contig=<ID=chr1,length=248956422>',
  '##INFO=<ID=DP,Number=1,Type=Integer,Description="Read depth">',
  '##FORMAT=<ID=GT,Number=1,Type=String,Description="Genotype">',
  '#CHROM\tPOS\tID\tREF\tALT\tQUAL\tFILTER\tINFO\tFORMAT\tSYNON_SAMPLE',
  'chr1\t1000000\trsSynonFixture\tA\tG\t99\tPASS\tDP=42\tGT\t0/1',
  '',
].join('\n');

let cleanup: { conversationId: string; artifactId: string } | null = null;
let conversationFixture: RealConversationFixture | null = null;

afterEach(async () => {
  if (cleanup) {
    const fetchImpl = await createSynonBiomedTestFetch(gatewayBaseUrl);
    const response = await fetchImpl(
      `${gatewayBaseUrl}/api/conversations/${encodeURIComponent(cleanup.conversationId)}/workspace/artifacts/${encodeURIComponent(cleanup.artifactId)}`,
      { method: 'DELETE' }
    );
    expect(response.ok, await response.text()).toBe(true);
    cleanup = null;
  }
  await conversationFixture?.dispose();
  conversationFixture = null;
});

describe('Synon Biomed genome preview gateway', () => {
  it('uploads, range-reads, classifies, and removes a real VCF artifact through SynonAI', async () => {
    conversationFixture = await createRealConversationFixture({ gatewayBaseUrl });
    const fetchImpl = conversationFixture.fetchImpl;
    const conversationId = conversationFixture.conversationId;

    const workspaceBase = `/api/conversations/${encodeURIComponent(conversationId)}/workspace`;
    const payload = Buffer.from(fixture);
    const uploadResponse = await fetchImpl(`${gatewayBaseUrl}${workspaceBase}/uploads`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        filename: `genome-preview-${Date.now()}.vcf`,
        total_size: payload.length,
        content_type: 'text/vcf',
        chunk_size: 1024 * 1024,
      }),
    });
    if (!uploadResponse.ok) throw new Error(`VCF upload init failed: ${await uploadResponse.text()}`);
    const upload = (await uploadResponse.json()) as { upload_id: string };

    const chunkResponse = await fetchImpl(
      `${gatewayBaseUrl}${workspaceBase}/uploads/${encodeURIComponent(upload.upload_id)}/chunks/0`,
      {
        method: 'POST',
        headers: { 'content-type': 'application/octet-stream' },
        body: payload,
      }
    );
    if (!chunkResponse.ok) throw new Error(`VCF chunk upload failed: ${await chunkResponse.text()}`);

    const finalizeResponse = await fetchImpl(
      `${gatewayBaseUrl}${workspaceBase}/uploads/${encodeURIComponent(upload.upload_id)}/finalize`,
      { method: 'POST' }
    );
    if (!finalizeResponse.ok) throw new Error(`VCF upload finalize failed: ${await finalizeResponse.text()}`);
    const artifact = (await finalizeResponse.json()) as { artifact_id?: string; id?: string };
    const artifactId = artifact.artifact_id ?? artifact.id;
    expect(artifactId).toBeTruthy();
    cleanup = { conversationId, artifactId: artifactId! };

    const metadata = await loadSynonBiomedArtifact(artifactId!, { baseUrl: gatewayBaseUrl, fetchImpl });
    expect(resolveSynonBiomedArtifactPreviewPlan(metadata)).toEqual({ type: 'genome', fetchText: false });

    const rangeResponse = await fetchImpl(`${gatewayBaseUrl}/api/artifacts/${encodeURIComponent(artifactId!)}`, {
      headers: { range: 'bytes=0-31' },
    });
    expect(rangeResponse.status).toBe(206);
    expect(rangeResponse.headers.get('accept-ranges')).toBe('bytes');
    expect(rangeResponse.headers.get('content-range')).toMatch(/^bytes 0-31\//);
    expect(await rangeResponse.text()).toBe(fixture.slice(0, 32));
  });
});
