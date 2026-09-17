import React from 'react';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import RemoteToolDetailTree from '@/renderer/pages/conversation/Messages/toolDetails/RemoteToolDetailTree';
import { conversationDisclosureStore } from '@/renderer/services/runtime/conversationDisclosureStore';

const { loadPage } = vi.hoisted(() => ({ loadPage: vi.fn() }));

vi.mock('@/renderer/pages/conversation/Messages/toolDetails/toolDetailApi', () => ({
  loadRemoteToolDetailPage: (...args: unknown[]) => loadPage(...args),
}));

describe('RemoteToolDetailTree', () => {
  beforeEach(() => {
    conversationDisclosureStore.resetForTests();
    loadPage.mockReset();
    loadPage.mockImplementation(async (_reference, options: { path: string; cursor?: string }) => {
      if (options.path === '') {
        return {
          messageId: 'message-1',
          section: 'output',
          path: '',
          revision: 7,
          kind: 'object',
          total: 3,
          from: 0,
          items: [
            { path: '/token', kind: 'scalar', key: 'token', value: 'private-token' },
            { path: '/stdout', kind: 'scalar', key: 'stdout', value: 'complete remote evidence' },
            { path: '/records', kind: 'array', key: 'records', total: 105 },
          ],
          nextCursor: null,
        };
      }
      if (options.path === '/records' && !options.cursor) {
        return {
          messageId: 'message-1',
          section: 'output',
          path: '/records',
          revision: 7,
          kind: 'array',
          total: 105,
          from: 0,
          items: Array.from({ length: 100 }, (_, index) => ({
            path: `/records/${index}`,
            kind: 'object',
            index,
            total: 3,
          })),
          nextCursor: 'next-100',
        };
      }
      if (options.path === '/records' && options.cursor === 'next-100') {
        return {
          messageId: 'message-1',
          section: 'output',
          path: '/records',
          revision: 7,
          kind: 'array',
          total: 105,
          from: 100,
          items: Array.from({ length: 5 }, (_, offset) => ({
            path: `/records/${offset + 100}`,
            kind: 'object',
            index: offset + 100,
            total: 3,
          })),
          nextCursor: null,
        };
      }
      if (options.path === '/records/104') {
        return {
          messageId: 'message-1',
          section: 'output',
          path: '/records/104',
          revision: 7,
          kind: 'object',
          total: 3,
          from: 0,
          items: [
            { path: '/records/104/id', kind: 'scalar', key: 'id', value: 'REC-105' },
            {
              path: '/records/104/title',
              kind: 'scalar',
              key: 'title',
              value: 'Shared scientific title',
            },
            { path: '/records/104/measurement', kind: 'object', key: 'measurement', total: 2 },
          ],
          nextCursor: null,
        };
      }
      if (options.path === '/records/104/measurement') {
        return {
          messageId: 'message-1',
          section: 'output',
          path: '/records/104/measurement',
          revision: 7,
          kind: 'object',
          total: 2,
          from: 0,
          items: [
            { path: '/records/104/measurement/value', kind: 'scalar', key: 'value', value: 104.5 },
            { path: '/records/104/measurement/unit', kind: 'scalar', key: 'unit', value: 'nM' },
          ],
          nextCursor: null,
        };
      }
      throw new Error(`unexpected path ${options.path}`);
    });
  });

  it('pages all records and opens arbitrary nested fields without exposing private keys', async () => {
    render(
      <RemoteToolDetailTree
        reference={{
          conversationId: 'conversation-1',
          messageId: 'message-1',
          revision: 7,
          contentUrl: '/api/artifacts/large-tool-result-a/versions/ltr-a',
        }}
        operationId='operation-1'
        chinese={false}
        detailKind='analysis'
      />
    );

    fireEvent.click(screen.getByRole('button', { name: 'Full details' }));
    expect(screen.getByRole('link', { name: 'Download complete JSON' })).toHaveAttribute(
      'href',
      '/api/artifacts/large-tool-result-a/versions/ltr-a'
    );
    const records = await screen.findByRole('button', { name: 'Records, 105 items' });
    expect(screen.queryByText('private-token')).not.toBeInTheDocument();
    expect(screen.getByText('complete remote evidence')).toBeInTheDocument();
    fireEvent.click(records);
    expect(await screen.findByRole('button', { name: 'Item 100, 3 fields' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Show more' }));
    const finalRecord = await screen.findByRole('button', { name: 'Item 105, 3 fields' });
    fireEvent.click(finalRecord);
    expect(await screen.findByText('REC-105')).toBeInTheDocument();
    expect(screen.getByText('Shared scientific title')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Measurement, 2 fields' }));
    await waitFor(() => expect(screen.getByText('104.5')).toBeInTheDocument());
    expect(screen.getByText('nM')).toBeInTheDocument();
  });

  it('preserves already loaded evidence when a later page fails and retries that same cursor', async () => {
    const fallback = loadPage.getMockImplementation();
    let rejectNextPage = true;
    loadPage.mockImplementation(async (reference, options: { path: string; cursor?: string }) => {
      if (options.cursor === 'next-100' && rejectNextPage) {
        rejectNextPage = false;
        throw new Error('temporary page failure');
      }
      return fallback!(reference, options);
    });
    render(
      <RemoteToolDetailTree
        reference={{ conversationId: 'conversation-1', messageId: 'message-1', revision: 7 }}
        operationId='operation-1'
        chinese={false}
        detailKind='analysis'
      />
    );

    fireEvent.click(screen.getByRole('button', { name: 'Full details' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Records, 105 items' }));
    expect(await screen.findByRole('button', { name: 'Item 100, 3 fields' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Show more' }));

    expect(await screen.findByText('Unable to load the next detail page.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Item 100, 3 fields' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByRole('button', { name: 'Item 105, 3 fields' })).toBeInTheDocument();
  });
});
