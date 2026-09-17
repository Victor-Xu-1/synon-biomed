import { describe, expect, it, vi } from 'vitest';
import { BackendHttpError } from '@/common/adapter/httpBridge';
import { loadSynonBiomedLineageMessages } from '@/renderer/services/synonBiomedLineageMessages';

describe('Synon Biomed lineage messages model', () => {
  it('normalizes native conversation rows for the child-frame drawer', async () => {
    const fetchImpl = vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            items: [
              {
                id: 'user-1',
                type: 'text',
                position: 'right',
                content: { content: 'Review STAT6 evidence' },
                created_at: 1,
              },
              {
                id: 'thinking-1',
                type: 'thinking',
                position: 'left',
                content: { content: 'Checking sources', status: 'done' },
                created_at: 2,
              },
              {
                id: 'tool-1',
                type: 'tool_call',
                position: 'left',
                content: { name: 'web_search', description: 'Searching PubMed', status: 'completed' },
                created_at: 3,
              },
            ],
            has_more_before: true,
          }),
          { status: 200, headers: { 'content-type': 'application/json' } }
        )
    ) as unknown as typeof fetch;

    await expect(loadSynonBiomedLineageMessages('child/1', { fetchImpl })).resolves.toEqual({
      items: [
        { id: 'user-1', kind: 'text', position: 'right', content: 'Review STAT6 evidence', createdAt: 1 },
        {
          id: 'thinking-1',
          kind: 'thinking',
          position: 'left',
          content: 'Checking sources',
          createdAt: 2,
        },
        {
          id: 'tool-1',
          kind: 'tool',
          position: 'left',
          content: 'Searching PubMed · completed',
          createdAt: 3,
        },
      ],
      hasMoreBefore: true,
    });
    expect(fetchImpl).toHaveBeenCalledWith('/api/conversations/child%2F1/messages?limit=200', expect.any(Object));
  });

  it('retries transient HISTORY_NOT_READY and eventually loads lineage messages', async () => {
    vi.useFakeTimers();
    try {
      const makeNotReadyResponse = () =>
        new Response(JSON.stringify({ code: 'HISTORY_NOT_READY' }), {
          status: 503,
          headers: { 'content-type': 'application/json' },
        });
      const successResponse = new Response(
        JSON.stringify({
          items: [
            {
              id: 'resume-1',
              type: 'text',
              position: 'left',
              content: { content: 'Retry recovery complete' },
              created_at: 10,
            },
          ],
          has_more_before: false,
        }),
        { status: 200, headers: { 'content-type': 'application/json' } }
      );

      const fetchImpl = vi
        .fn()
        .mockResolvedValueOnce(makeNotReadyResponse())
        .mockResolvedValueOnce(makeNotReadyResponse())
        .mockResolvedValueOnce(successResponse) as unknown as typeof fetch;

      const resultPromise = loadSynonBiomedLineageMessages('child/2', { fetchImpl });
      await vi.runAllTimersAsync();
      const result = await resultPromise;

      expect(result).toEqual({
        items: [{ id: 'resume-1', kind: 'text', position: 'left', content: 'Retry recovery complete', createdAt: 10 }],
        hasMoreBefore: false,
      });
      expect(fetchImpl).toHaveBeenCalledTimes(3);
      expect(fetchImpl).toHaveBeenNthCalledWith(
        1,
        '/api/conversations/child%2F2/messages?limit=200',
        expect.any(Object)
      );
      expect(fetchImpl).toHaveBeenNthCalledWith(
        2,
        '/api/conversations/child%2F2/messages?limit=200',
        expect.any(Object)
      );
      expect(fetchImpl).toHaveBeenNthCalledWith(
        3,
        '/api/conversations/child%2F2/messages?limit=200',
        expect.any(Object)
      );
    } finally {
      vi.useRealTimers();
    }
  });

  it('retries on transcript-not-ready backend text without a structured code', async () => {
    vi.useFakeTimers();
    try {
      const makeNotReadyResponse = () => new Response('transcript runtime is not available', { status: 503 });
      const successResponse = new Response(
        JSON.stringify({
          items: [
            {
              id: 'resume-2',
              type: 'text',
              position: 'right',
              content: { content: 'Text-retry recovery complete' },
              created_at: 20,
            },
          ],
          has_more_before: false,
        }),
        { status: 200, headers: { 'content-type': 'application/json' } }
      );

      const fetchImpl = vi
        .fn()
        .mockResolvedValueOnce(makeNotReadyResponse())
        .mockResolvedValueOnce(successResponse) as unknown as typeof fetch;

      const resultPromise = loadSynonBiomedLineageMessages('child/4', { fetchImpl });
      await vi.runAllTimersAsync();
      const result = await resultPromise;

      expect(result).toEqual({
        items: [
          { id: 'resume-2', kind: 'text', position: 'right', content: 'Text-retry recovery complete', createdAt: 20 },
        ],
        hasMoreBefore: false,
      });
      expect(fetchImpl).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it('does not retry non-retriable backend failures', async () => {
    const fetchImpl = vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            code: 'INTERNAL_ERROR',
            error: 'lineage service failed',
          }),
          { status: 500, headers: { 'content-type': 'application/json' } }
        )
    ) as unknown as typeof fetch;

    await expect(loadSynonBiomedLineageMessages('child/3', { fetchImpl })).rejects.toBeInstanceOf(BackendHttpError);
    await expect(loadSynonBiomedLineageMessages('child/3', { fetchImpl })).rejects.toMatchObject({
      status: 500,
      code: 'INTERNAL_ERROR',
    });

    expect(fetchImpl).toHaveBeenCalledTimes(2);
  });
});
