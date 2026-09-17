import { parseNotebookDocument } from '@/renderer/pages/conversation/Preview/components/viewers/notebookModel';
import { parseSequenceDocument } from '@/renderer/pages/conversation/Preview/components/viewers/sequenceModel';
import { convertLatexDocumentToHtml } from '@/renderer/pages/artifact/latexDocument';
import { resolveSynonBiomedArtifactPreviewPlan } from '@/renderer/services/synonBiomedArtifactPreview';
import { loadSynonBiomedArtifact } from '@/renderer/services/synonBiomedGateway';
import { afterEach, describe, expect, it } from 'vitest';
import { createRealConversationFixture, type RealConversationFixture } from './synonbiomedRealConversationFixture';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
const genBankFixture = `LOCUS       SYNON001                 120 bp    DNA     circular SYN 11-JUL-2026
DEFINITION  Synthetic Synon Biomed test plasmid.
ACCESSION   SYNON001
VERSION     SYNON001.1
FEATURES             Location/Qualifiers
     source          1..120
                     /organism="synthetic construct"
     promoter        1..20
                     /label="synon_promoter"
     CDS             21..90
                     /gene="synA"
                     /label="synA CDS"
ORIGIN
        1 atgcatgcat gcatgcatgc atgcatgcat gcatgcatgc atgcatgcat gcatgcatgc
       61 atgcatgcat gcatgcatgc atgcatgcat gcatgcatgc atgcatgcat gcatgcatgc
//`;
const notebookFixture = JSON.stringify({
  nbformat: 4,
  nbformat_minor: 5,
  metadata: {
    kernelspec: { display_name: 'Python 3 (Synon)', language: 'python', name: 'python3' },
    language_info: { name: 'python' },
  },
  cells: [
    { cell_type: 'markdown', source: ['# STAT6 validation\n', 'Synon Biomed notebook fixture'] },
    {
      cell_type: 'code',
      execution_count: 7,
      source: ['print("validated")'],
      outputs: [{ output_type: 'stream', name: 'stdout', text: ['validated\n'] }],
    },
  ],
});
const latexFixture = String.raw`\documentclass{article}
\title{Synon Biomed Assay Report}
\begin{document}
\maketitle
\section{STAT6 response}
The normalized response is $R = \frac{I_{sample}}{I_{control}}$.
\begin{enumerate}
\item Validate the primary endpoint.
\item Preserve provenance for every result.
\end{enumerate}
\end{document}`;

const cleanup: Array<{ conversationId: string; artifactId: string }> = [];
let conversationFixture: RealConversationFixture | null = null;

afterEach(async () => {
  const fetchImpl = await createSynonBiomedTestFetch(gatewayBaseUrl);
  const results = await Promise.all(
    cleanup.splice(0).map(async (item) => {
      const response = await fetchImpl(
        `${gatewayBaseUrl}/api/conversations/${encodeURIComponent(item.conversationId)}/workspace/artifacts/${encodeURIComponent(item.artifactId)}`,
        { method: 'DELETE' }
      );
      return { ok: response.ok, body: await response.text() };
    })
  );
  for (const result of results) {
    expect(result.ok, result.body).toBe(true);
  }
  await conversationFixture?.dispose();
  conversationFixture = null;
});

describe('Synon Biomed sequence and notebook preview gateway', () => {
  it('uploads, classifies, reads, and parses real GenBank, ipynb, and LaTeX artifacts', async () => {
    conversationFixture = await createRealConversationFixture({ gatewayBaseUrl });
    const fetchImpl = conversationFixture.fetchImpl;
    const conversationId = conversationFixture.conversationId;

    const sequence = await uploadArtifact({
      fetchImpl,
      conversationId,
      filename: `sequence-preview-${Date.now()}.gbk`,
      contentType: 'chemical/seq-na-genbank',
      source: genBankFixture,
    });
    const notebook = await uploadArtifact({
      fetchImpl,
      conversationId,
      filename: `notebook-preview-${Date.now()}.ipynb`,
      contentType: 'application/x-ipynb+json',
      source: notebookFixture,
    });
    const latex = await uploadArtifact({
      fetchImpl,
      conversationId,
      filename: `latex-preview-${Date.now()}.tex`,
      contentType: 'application/x-tex',
      source: latexFixture,
    });
    cleanup.push(
      { conversationId, artifactId: sequence.artifactId },
      { conversationId, artifactId: notebook.artifactId },
      { conversationId, artifactId: latex.artifactId }
    );

    const sequenceMetadata = await loadSynonBiomedArtifact(sequence.artifactId, { baseUrl: gatewayBaseUrl, fetchImpl });
    const notebookMetadata = await loadSynonBiomedArtifact(notebook.artifactId, { baseUrl: gatewayBaseUrl, fetchImpl });
    const latexMetadata = await loadSynonBiomedArtifact(latex.artifactId, { baseUrl: gatewayBaseUrl, fetchImpl });
    expect(resolveSynonBiomedArtifactPreviewPlan(sequenceMetadata)).toEqual({ type: 'sequence', fetchText: false });
    expect(resolveSynonBiomedArtifactPreviewPlan(notebookMetadata)).toEqual({ type: 'notebook', fetchText: false });
    expect(resolveSynonBiomedArtifactPreviewPlan(latexMetadata)).toEqual({
      type: 'latex',
      fetchText: true,
      language: 'latex',
    });

    const sequenceResponse = await fetchImpl(
      `${gatewayBaseUrl}/api/artifacts/${encodeURIComponent(sequence.artifactId)}`
    );
    const notebookResponse = await fetchImpl(
      `${gatewayBaseUrl}/api/artifacts/${encodeURIComponent(notebook.artifactId)}`
    );
    const latexResponse = await fetchImpl(`${gatewayBaseUrl}/api/artifacts/${encodeURIComponent(latex.artifactId)}`);
    expect(sequenceResponse.ok).toBe(true);
    expect(notebookResponse.ok).toBe(true);
    expect(latexResponse.ok).toBe(true);

    const sequenceDocument = await parseSequenceDocument(await sequenceResponse.text(), sequence.filename);
    const notebookDocument = parseNotebookDocument(await notebookResponse.text());
    const latexDocument = convertLatexDocumentToHtml(await latexResponse.text());
    expect(sequenceDocument).toMatchObject({ name: 'SYNON001', seq: expect.any(String), type: 'dna' });
    expect(sequenceDocument.seq).toHaveLength(120);
    expect(sequenceDocument.annotations).toHaveLength(2);
    expect(notebookDocument).toMatchObject({
      nbformat: 4,
      nbformatMinor: 5,
      language: 'python',
      kernelLabel: 'Python 3 (Synon)',
    });
    expect(notebookDocument.cells).toHaveLength(2);
    expect(latexDocument.html).toContain('<h3>STAT6 response</h3>');
    expect(latexDocument.html).toContain('class="inline-math"');
    expect(latexDocument.html).toContain('<ol class="enumerate">');
  });
});

async function uploadArtifact({
  fetchImpl,
  conversationId,
  filename,
  contentType,
  source,
}: {
  fetchImpl: typeof fetch;
  conversationId: string;
  filename: string;
  contentType: string;
  source: string;
}): Promise<{ artifactId: string; filename: string }> {
  const payload = Buffer.from(source);
  const workspaceBase = `/api/conversations/${encodeURIComponent(conversationId)}/workspace`;
  const initResponse = await fetchImpl(`${gatewayBaseUrl}${workspaceBase}/uploads`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      filename,
      total_size: payload.length,
      content_type: contentType,
      chunk_size: 1024 * 1024,
    }),
  });
  if (!initResponse.ok) throw new Error(`Upload init failed: ${await initResponse.text()}`);
  const upload = (await initResponse.json()) as { upload_id: string };

  const chunkResponse = await fetchImpl(
    `${gatewayBaseUrl}${workspaceBase}/uploads/${encodeURIComponent(upload.upload_id)}/chunks/0`,
    { method: 'POST', headers: { 'content-type': 'application/octet-stream' }, body: payload }
  );
  if (!chunkResponse.ok) throw new Error(`Chunk upload failed: ${await chunkResponse.text()}`);

  const finalizeResponse = await fetchImpl(
    `${gatewayBaseUrl}${workspaceBase}/uploads/${encodeURIComponent(upload.upload_id)}/finalize`,
    { method: 'POST' }
  );
  if (!finalizeResponse.ok) throw new Error(`Upload finalize failed: ${await finalizeResponse.text()}`);
  const artifact = (await finalizeResponse.json()) as { artifact_id?: string; id?: string };
  const artifactId = artifact.artifact_id ?? artifact.id;
  if (!artifactId) throw new Error('Upload did not return an artifact id');
  return { artifactId, filename };
}
