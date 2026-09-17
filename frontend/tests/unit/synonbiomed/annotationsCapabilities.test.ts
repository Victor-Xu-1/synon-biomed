import { describe, expect, it, vi } from 'vitest';
import {
  applySynonBiomedArtifactEdit,
  createSynonBiomedArtifactAnnotation,
  createSynonBiomedTranscriptAnnotation,
  deleteSynonBiomedArtifactAnnotation,
  deleteSynonBiomedTranscriptAnnotation,
  drainSynonBiomedTranscriptAnnotations,
  loadSynonBiomedArtifactAnnotations,
  loadSynonBiomedFrameVerification,
  loadSynonBiomedTranscriptAnnotations,
  TRANSCRIPT_ANNOTATION_SETTLED_REUSE_MS,
  suggestSynonBiomedArtifactEdit,
  updateSynonBiomedTranscriptAnnotation,
} from '@/renderer/services/synonBiomedAnnotations';

describe('Synon Biomed annotations capability service', () => {
  it('coalesces only overlapping transcript annotation reads and revalidates after settlement', async () => {
    let resolveRequest!: (response: Response) => void;
    const fetchImpl = vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          resolveRequest = resolve;
        })
    );

    vi.useFakeTimers();
    const options = { ownerId: 'owner-a', fetchImpl };
    const first = loadSynonBiomedTranscriptAnnotations('frame-coalesced', options);
    const second = loadSynonBiomedTranscriptAnnotations('frame-coalesced', options);
    expect(second).toBe(first);
    expect(fetchImpl).toHaveBeenCalledTimes(1);

    resolveRequest(Response.json([]));
    await expect(first).resolves.toEqual([]);

    await expect(loadSynonBiomedTranscriptAnnotations('frame-coalesced', options)).resolves.toEqual([]);
    expect(fetchImpl).toHaveBeenCalledTimes(1);

    vi.advanceTimersByTime(TRANSCRIPT_ANNOTATION_SETTLED_REUSE_MS);
    fetchImpl.mockResolvedValueOnce(Response.json([]));
    await expect(loadSynonBiomedTranscriptAnnotations('frame-coalesced', options)).resolves.toEqual([]);
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    vi.useRealTimers();
  });

  it('does not retain a failed transcript annotation request', async () => {
    const fetchImpl = vi
      .fn()
      .mockRejectedValueOnce(new Error('temporary transcript failure'))
      .mockResolvedValueOnce(Response.json([]));

    const options = { ownerId: 'owner-retry', fetchImpl };
    await expect(loadSynonBiomedTranscriptAnnotations('frame-retry', options)).rejects.toThrow(
      'temporary transcript failure'
    );
    await expect(loadSynonBiomedTranscriptAnnotations('frame-retry', options)).resolves.toEqual([]);
    expect(fetchImpl).toHaveBeenCalledTimes(2);
  });

  it('never reuses transcript annotations across owners', async () => {
    const fetchImpl = vi.fn().mockImplementation(async () => Response.json([]));

    await loadSynonBiomedTranscriptAnnotations('frame-owner-scope', { ownerId: 'owner-a', fetchImpl });
    await loadSynonBiomedTranscriptAnnotations('frame-owner-scope', { ownerId: 'owner-b', fetchImpl });

    expect(fetchImpl).toHaveBeenCalledTimes(2);
  });

  it('does not retain a settled transcript read without an owner scope', async () => {
    const fetchImpl = vi.fn().mockImplementation(async () => Response.json([]));

    await loadSynonBiomedTranscriptAnnotations('frame-empty-owner', { ownerId: '', fetchImpl });
    await loadSynonBiomedTranscriptAnnotations('frame-empty-owner', { ownerId: '  ', fetchImpl });

    expect(fetchImpl).toHaveBeenCalledTimes(2);
  });

  it('invalidates settled transcript reads after create, update and delete mutations', async () => {
    let transcriptReads = 0;
    const annotationPayload = {
      id: 'transcript-cache',
      root_frame_id: 'frame-mutations',
      message_uuid: 'message-1',
      message_index: 1,
      block_index: 0,
      source: 'assistant',
      tool_name: null,
      anchor_text: 'Evidence',
      kind: 'bookmark',
      origin: 'user',
      note: 'Review',
      created_at: '2026-07-13T00:00:00.000Z',
      updated_at: '2026-07-13T00:00:00.000Z',
    };
    const fetchImpl = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const method = init?.method ?? 'GET';
      if (method === 'GET') {
        transcriptReads += 1;
        return Response.json([]);
      }
      if (method === 'DELETE') return new Response(null, { status: 204 });
      return Response.json(annotationPayload);
    });
    const options = { ownerId: 'owner-mutations', fetchImpl };

    await loadSynonBiomedTranscriptAnnotations('frame-mutations', options);
    await createSynonBiomedTranscriptAnnotation(
      'frame-mutations',
      { messageIndex: 1, source: 'assistant', anchorText: 'Evidence', kind: 'bookmark' },
      fetchImpl
    );
    await loadSynonBiomedTranscriptAnnotations('frame-mutations', options);
    await updateSynonBiomedTranscriptAnnotation('frame-mutations', 'transcript-cache', { note: 'Updated' }, fetchImpl);
    await loadSynonBiomedTranscriptAnnotations('frame-mutations', options);
    await deleteSynonBiomedTranscriptAnnotation('frame-mutations', 'transcript-cache', fetchImpl);
    await loadSynonBiomedTranscriptAnnotations('frame-mutations', options);

    expect(transcriptReads).toBe(4);
  });

  it('maps artifact annotations without dropping scientific anchors', async () => {
    const fetchImpl = vi.fn(async () =>
      Response.json({
        target_key: 'av:version-1',
        current_checksum: 'sha256-value',
        annotations: [
          {
            id: 'annotation-1',
            artifact_id: 'artifact-1',
            target_key: 'av:version-1',
            label: '①',
            content_checksum: 'sha256-value',
            type: 'text_selection',
            text: 'Check this claim',
            start_line: 4,
            start_col: 2,
            end_line: 5,
            end_col: 18,
            selection_text: 'STAT6 inhibition',
            page_number: 2,
            addressed_at: null,
            addressed_in_frame_id: null,
            created_at: '2026-07-13T00:00:00.000Z',
          },
        ],
      })
    );

    await expect(loadSynonBiomedArtifactAnnotations('artifact-1', 'version-1', fetchImpl)).resolves.toEqual({
      targetKey: 'av:version-1',
      currentChecksum: 'sha256-value',
      annotations: [
        expect.objectContaining({
          id: 'annotation-1',
          type: 'text_selection',
          text: 'Check this claim',
          startLine: 4,
          selectionText: 'STAT6 inhibition',
          pageNumber: 2,
        }),
      ],
    });
    expect(fetchImpl).toHaveBeenCalledWith('/api/artifacts/artifact-1/versions/version-1/annotations', {
      credentials: 'include',
    });
  });

  it('preserves exact create, update, drain and delete contracts', async () => {
    const artifactFetch = vi.fn(async () =>
      Response.json({
        id: 'annotation-1',
        artifact_id: 'artifact-1',
        label: '①',
        type: 'point',
        text: 'Review this result',
        x_percent: 25,
        y_percent: 40,
        created_at: '2026-07-13T00:00:00.000Z',
      })
    );
    await createSynonBiomedArtifactAnnotation(
      'artifact-1',
      'version-1',
      { text: 'Review this result', xPercent: 25, yPercent: 40 },
      artifactFetch
    );
    expect(artifactFetch).toHaveBeenCalledWith(
      '/api/artifacts/artifact-1/versions/version-1/annotations',
      expect.objectContaining({
        method: 'POST',
        credentials: 'include',
        body: expect.stringContaining('"x_percent":25'),
      })
    );

    const transcriptFetch = vi.fn(async () =>
      Response.json({
        id: 'transcript-1',
        root_frame_id: 'frame-1',
        message_uuid: 'message-1',
        message_index: 3,
        block_index: 1,
        source: 'tool_result',
        tool_name: 'web_search',
        anchor_text: 'Evidence excerpt',
        kind: 'bookmark',
        origin: 'user',
        note: 'Revisit this evidence',
        created_at: '2026-07-13T00:00:00.000Z',
        updated_at: '2026-07-13T00:00:00.000Z',
      })
    );
    await createSynonBiomedTranscriptAnnotation(
      'frame-1',
      {
        messageUuid: 'message-1',
        messageIndex: 3,
        blockIndex: 1,
        source: 'tool_result',
        toolName: 'web_search',
        anchorText: 'Evidence excerpt',
        kind: 'bookmark',
        note: 'Revisit this evidence',
      },
      transcriptFetch
    );
    expect(transcriptFetch).toHaveBeenCalledWith(
      '/api/frames/frame-1/transcript-annotations',
      expect.objectContaining({ method: 'POST', body: expect.stringContaining('"tool_name":"web_search"') })
    );

    transcriptFetch.mockClear();
    await updateSynonBiomedTranscriptAnnotation('frame-1', 'transcript-1', { read: true }, transcriptFetch);
    expect(transcriptFetch).toHaveBeenCalledWith(
      '/api/frames/frame-1/transcript-annotations/transcript-1',
      expect.objectContaining({ method: 'PATCH', body: '{"read":true}' })
    );

    const drainFetch = vi.fn(async () => Response.json({ deleted: 2 }));
    await expect(drainSynonBiomedTranscriptAnnotations('frame-1', ['a', 'b'], drainFetch)).resolves.toEqual({
      deleted: 2,
    });
    expect(drainFetch).toHaveBeenCalledWith(
      '/api/frames/frame-1/transcript-annotations/drain',
      expect.objectContaining({ method: 'POST', body: '{"ids":["a","b"]}' })
    );

    const deleteFetch = vi.fn(async () => new Response(null, { status: 204 }));
    await deleteSynonBiomedArtifactAnnotation('annotation-1', deleteFetch);
    expect(deleteFetch).toHaveBeenCalledWith('/api/annotations/annotation-1', {
      credentials: 'include',
      method: 'DELETE',
    });
  });

  it('preserves the v1.1 iterative suggestion and immutable apply-edit contracts', async () => {
    const suggestFetch = vi.fn(async () => Response.json({ suggestion: 'STAT6 inhibition reduced viability.' }));
    await expect(
      suggestSynonBiomedArtifactEdit(
        'artifact-1',
        'version-1',
        {
          selectedText: 'STAT6 inhibition changed viability.',
          annotationText: 'Make the direction precise.',
          currentIteration: 'STAT6 inhibition affected viability.',
          mode: 'edit',
        },
        suggestFetch
      )
    ).resolves.toBe('STAT6 inhibition reduced viability.');
    expect(suggestFetch).toHaveBeenCalledWith(
      '/api/artifacts/artifact-1/versions/version-1/suggest-edits',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({
          selected_text: 'STAT6 inhibition changed viability.',
          annotation_text: 'Make the direction precise.',
          current_iteration: 'STAT6 inhibition affected viability.',
          mode: 'edit',
        }),
      })
    );

    const applyFetch = vi.fn(async () =>
      Response.json(
        {
          version_id: 'version-2',
          version_number: 2,
          artifact_id: 'artifact-1',
          parent_version_id: 'version-1',
          carried_annotations: [
            {
              id: 'annotation-2',
              artifact_id: 'artifact-1',
              label: '①',
              type: 'text_selection',
              text: 'Make the direction precise.',
              selection_text: 'STAT6 inhibition reduced viability.',
              created_at: '2026-07-13T00:00:00.000Z',
            },
          ],
        },
        { status: 201 }
      )
    );
    await expect(
      applySynonBiomedArtifactEdit(
        'artifact-1',
        'version-1',
        {
          selectedText: 'STAT6 inhibition changed viability.',
          replacementText: 'STAT6 inhibition reduced viability.',
          contextBefore: 'Result: ',
          contextAfter: '\nConclusion',
        },
        applyFetch
      )
    ).resolves.toMatchObject({
      versionId: 'version-2',
      versionNumber: 2,
      artifactId: 'artifact-1',
      parentVersionId: 'version-1',
      carriedAnnotations: [expect.objectContaining({ id: 'annotation-2' })],
    });
    expect(applyFetch).toHaveBeenCalledWith(
      '/api/artifacts/artifact-1/versions/version-1/apply-edit',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({
          selected_text: 'STAT6 inhibition changed viability.',
          replacement_text: 'STAT6 inhibition reduced viability.',
          context_before: 'Result: ',
          context_after: '\nConclusion',
        }),
      })
    );
  });

  it('maps verification verdicts, source references and running audits', async () => {
    const fetchImpl = vi.fn(async () =>
      Response.json({
        checks: [
          {
            id: 'check-1',
            root_frame_id: 'frame-1',
            artifact_version_id: 'version-1',
            claim_id: 'claim-1',
            claim: 'STAT6 binding improved',
            verdict: 'warn',
            severity: 'medium',
            evidence: 'Assay variance exceeds threshold',
            rebuttal: null,
            reviewer_idx: 1,
            reviewer_model: 'reviewer-model',
            reviewer_frame_id: 'reviewer-frame',
            source_ref: { kind: 'artifact_version', version_id: 'version-1' },
            status: 'open',
            reflag_count: 1,
            created_at: '2026-07-13T00:00:00.000Z',
          },
        ],
        claims: [{ id: 'claim-1' }],
        running: [{ frame_id: 'audit-frame' }],
      })
    );

    const result = await loadSynonBiomedFrameVerification('frame-1', 'open', fetchImpl);
    expect(result.checks[0]).toMatchObject({
      id: 'check-1',
      verdict: 'warn',
      status: 'open',
      sourceRef: { kind: 'artifact_version', version_id: 'version-1' },
    });
    expect(result.claims).toHaveLength(1);
    expect(result.running).toHaveLength(1);
    expect(fetchImpl).toHaveBeenCalledWith('/api/frames/frame-1/verification?status=open', {
      credentials: 'include',
    });
  });
});
