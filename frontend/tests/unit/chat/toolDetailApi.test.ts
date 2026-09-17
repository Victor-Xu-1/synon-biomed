import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  clearRemoteToolDetailCache,
  loadRemoteToolDetailPage,
} from '@/renderer/pages/conversation/Messages/toolDetails/toolDetailApi';
import { setRendererAccountOwner } from '@/renderer/services/rendererAccountScope';

describe('tool detail API', () => {
  beforeEach(() => {
    setRendererAccountOwner('tool-detail-test-owner');
    clearRemoteToolDetailCache();
    vi.unstubAllGlobals();
  });

  it('does not publish an in-flight page across an account boundary', async () => {
    let resolveResponse!: (response: Response) => void;
    const pendingResponse = new Promise<Response>((resolve) => {
      resolveResponse = resolve;
    });
    vi.stubGlobal(
      'fetch',
      vi.fn(() => pendingResponse)
    );

    const request = loadRemoteToolDetailPage(
      { conversationId: 'conversation-1', messageId: 'message-1', revision: 7 },
      { path: '/records' }
    );
    setRendererAccountOwner('tool-detail-next-owner');
    resolveResponse(
      new Response(
        JSON.stringify({
          message_id: 'message-1',
          branch_id: 'br_deadbeef',
          section: 'output',
          path: '/records',
          revision: 7,
          kind: 'array',
          total: 0,
          from: 0,
          items: [],
          next_cursor: null,
        }),
        { status: 200 }
      )
    );

    await expect(request).rejects.toMatchObject({ name: 'AbortError' });
  });

  it('loads and validates an owner-scoped cursor page', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          message_id: 'message-1',
          branch_id: 'br_deadbeef',
          section: 'output',
          path: '/records',
          revision: 7,
          kind: 'array',
          total: 105,
          from: 0,
          items: [
            { path: '/records/0', kind: 'object', index: 0, total: 3 },
            { path: '/records/1', kind: 'object', index: 1, total: 3 },
          ],
          next_cursor: 'opaque-next',
        }),
        { status: 200, headers: { 'content-type': 'application/json' } }
      )
    );
    vi.stubGlobal('fetch', fetchMock);

    const page = await loadRemoteToolDetailPage(
      { conversationId: 'conversation-1', messageId: 'message-1', branchId: 'br_deadbeef', revision: 7 },
      { path: '/records' }
    );

    expect(page).toMatchObject({ total: 105, from: 0, nextCursor: 'opaque-next' });
    expect(page.items).toHaveLength(2);
    const requestedUrl = String(fetchMock.mock.calls[0][0]);
    expect(requestedUrl).toContain('/api/conversations/conversation-1/messages/message-1/tool-detail?');
    expect(requestedUrl).toContain('path=%2Frecords');
    expect(requestedUrl).toContain('revision=7');
    expect(requestedUrl).toContain('branch_id=br_deadbeef');
  });

  it('rejects a response that changes the message, path, or revision', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            message_id: 'other-message',
            branch_id: 'br_deadbeef',
            section: 'output',
            path: '/other',
            revision: 8,
            kind: 'array',
            total: 0,
            from: 0,
            items: [],
            next_cursor: null,
          }),
          { status: 200 }
        )
      )
    );

    await expect(
      loadRemoteToolDetailPage(
        { conversationId: 'conversation-1', messageId: 'message-1', revision: 7 },
        { path: '/records' }
      )
    ).rejects.toThrow('Tool detail response is invalid');
  });

  it('accepts an explicitly bounded scalar preview with its original byte count', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            message_id: 'message-1',
            branch_id: 'br_deadbeef',
            section: 'output',
            path: '/sequence',
            revision: 7,
            kind: 'scalar',
            total: 1,
            from: 0,
            items: [
              {
                path: '/sequence',
                kind: 'scalar',
                value: 'bounded preview…',
                truncated: true,
                original_bytes: 500_000,
              },
            ],
            next_cursor: null,
          }),
          { status: 200 }
        )
      )
    );

    const page = await loadRemoteToolDetailPage(
      { conversationId: 'conversation-1', messageId: 'message-1', revision: 7 },
      { path: '/sequence' }
    );

    expect(page.items[0]).toMatchObject({ truncated: true, originalBytes: 500_000 });
  });

  it.each([
    {
      name: 'array index and path mismatch',
      page: {
        message_id: 'message-1',
        branch_id: 'br_deadbeef',
        section: 'output',
        path: '/records',
        revision: 7,
        kind: 'array',
        total: 2,
        from: 0,
        items: [{ path: '/records/9', kind: 'scalar', index: 0, value: 'wrong path' }],
        next_cursor: 'cursor',
      },
    },
    {
      name: 'missing continuation cursor',
      page: {
        message_id: 'message-1',
        branch_id: 'br_deadbeef',
        section: 'output',
        path: '/records',
        revision: 7,
        kind: 'array',
        total: 2,
        from: 0,
        items: [{ path: '/records/0', kind: 'scalar', index: 0, value: 'first' }],
        next_cursor: null,
      },
    },
    {
      name: 'object key path mismatch',
      page: {
        message_id: 'message-1',
        branch_id: 'br_deadbeef',
        section: 'output',
        path: '',
        revision: 7,
        kind: 'object',
        total: 1,
        from: 0,
        items: [{ path: '/unescaped/key', kind: 'scalar', key: 'unescaped/key', value: true }],
        next_cursor: null,
      },
    },
    {
      name: 'scalar preview without a larger original byte count',
      page: {
        message_id: 'message-1',
        branch_id: 'br_deadbeef',
        section: 'output',
        path: '/sequence',
        revision: 7,
        kind: 'scalar',
        total: 1,
        from: 0,
        items: [
          {
            path: '/sequence',
            kind: 'scalar',
            value: 'preview',
            truncated: true,
            original_bytes: 1,
          },
        ],
        next_cursor: null,
      },
    },
  ])('rejects malformed pagination structure: $name', async ({ page }) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(page), { status: 200 })));

    await expect(
      loadRemoteToolDetailPage(
        { conversationId: 'conversation-1', messageId: 'message-1', revision: 7 },
        { path: page.path }
      )
    ).rejects.toThrow('Tool detail response is invalid');
  });
});
