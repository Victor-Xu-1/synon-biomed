import { resolveSynonBiomedArtifactPreviewPlan } from '@/renderer/services/synonBiomedArtifactPreview';
import { parseScientificAlignment } from '@/renderer/pages/conversation/Preview/components/viewers/scientificMsaModel';
import {
  getSynonBiomedArtifactContentUrl,
  loadSynonBiomedArtifact,
  loadSynonBiomedProjectWorkbench,
} from '@/renderer/services/synonBiomedGateway';
import { afterEach, describe, expect, it } from 'vitest';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';
import { createRealArtifactFixture, type RealArtifactFixture } from './synonbiomedRealArtifactFixture';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
let artifactFixture: RealArtifactFixture | null = null;

afterEach(async () => {
  await artifactFixture?.dispose();
  artifactFixture = null;
});

describe('Synon Biomed artifact preview gateway', () => {
  it('loads real artifact metadata and exposes the backend content URL without a local file bridge', async () => {
    const fetchImpl = await createSynonBiomedTestFetch(gatewayBaseUrl);
    artifactFixture = await createRealArtifactFixture({
      gatewayBaseUrl,
      filename: 'qc_metrics.png',
      contentType: 'image/png',
      content: Buffer.from(
        'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=',
        'base64'
      ),
    });
    const pngArtifactId = artifactFixture.artifactId;
    const artifact = await loadSynonBiomedArtifact(pngArtifactId, { baseUrl: gatewayBaseUrl, fetchImpl });
    const contentResponse = await fetchImpl(getSynonBiomedArtifactContentUrl(pngArtifactId, gatewayBaseUrl));

    expect(artifact.filename).toBe('qc_metrics.png');
    expect(artifact.contentType).toBe('image/png');
    expect(contentResponse.headers.get('content-type')).toContain('image/png');
  });

  it('loads real structure and table artifacts for the SynonAI scientific viewers', async () => {
    const fetchImpl = await createSynonBiomedTestFetch(gatewayBaseUrl);
    const workbench = await loadSynonBiomedProjectWorkbench('proj_example', { baseUrl: gatewayBaseUrl, fetchImpl });
    const structure = workbench.artifacts.find((artifact) => artifact.filename.endsWith('.pdb'));
    const table = workbench.artifacts.find((artifact) => artifact.filename.endsWith('.csv'));

    expect(structure).toBeDefined();
    expect(table).toBeDefined();
    expect(resolveSynonBiomedArtifactPreviewPlan(structure!)).toMatchObject({ type: 'structure', fetchText: false });
    expect(resolveSynonBiomedArtifactPreviewPlan(table!)).toMatchObject({ type: 'table', fetchText: false });

    const [structureResponse, tableResponse] = await Promise.all([
      fetchImpl(getSynonBiomedArtifactContentUrl(structure!.artifactId, gatewayBaseUrl)),
      fetchImpl(getSynonBiomedArtifactContentUrl(table!.artifactId, gatewayBaseUrl)),
    ]);
    const [structureText, tableText] = await Promise.all([structureResponse.text(), tableResponse.text()]);

    expect(structureResponse.ok).toBe(true);
    expect(structureText).toMatch(/^(?:HEADER|ATOM|HETATM|MODEL)/m);
    expect(tableResponse.ok).toBe(true);
    expect(tableText).toContain(',');
    expect(tableText.split(/\r?\n/).length).toBeGreaterThan(1);
  });

  it('loads a real JSON artifact through the text preview contract', async () => {
    const fetchImpl = await createSynonBiomedTestFetch(gatewayBaseUrl);
    const workbench = await loadSynonBiomedProjectWorkbench('proj_example', { baseUrl: gatewayBaseUrl, fetchImpl });
    const jsonArtifact = workbench.artifacts.find((artifact) => artifact.filename.endsWith('.json'));

    expect(jsonArtifact).toBeDefined();
    expect(resolveSynonBiomedArtifactPreviewPlan(jsonArtifact!)).toEqual({
      type: 'code',
      fetchText: true,
      language: 'json',
    });

    const response = await fetchImpl(getSynonBiomedArtifactContentUrl(jsonArtifact!.artifactId, gatewayBaseUrl), {
      headers: { accept: 'application/json, text/plain' },
    });
    const content = await response.text();

    expect(response.ok).toBe(true);
    expect(() => JSON.parse(content)).not.toThrow();
  });

  it('loads the real aligned FASTA artifact through the SynonAI MSA model', async () => {
    const fetchImpl = await createSynonBiomedTestFetch(gatewayBaseUrl);
    const workbench = await loadSynonBiomedProjectWorkbench('proj_example', { baseUrl: gatewayBaseUrl, fetchImpl });
    const alignment = workbench.artifacts.find((artifact) => artifact.filename === 'nif3_aligned.fasta');

    expect(alignment).toBeDefined();
    expect(resolveSynonBiomedArtifactPreviewPlan(alignment!)).toMatchObject({ type: 'msa', fetchText: false });

    const response = await fetchImpl(getSynonBiomedArtifactContentUrl(alignment!.artifactId, gatewayBaseUrl));
    const model = parseScientificAlignment(await response.text(), alignment!.filename);

    expect(response.ok).toBe(true);
    expect(model.sequences.length).toBeGreaterThan(10);
    expect(model.positionCount).toBeGreaterThan(100);
  });
});
