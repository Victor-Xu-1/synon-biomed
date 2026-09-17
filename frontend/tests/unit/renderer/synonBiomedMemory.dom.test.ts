import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  clearSynonBiomedMemories,
  createSynonBiomedMemory,
  createSynonBiomedMemoryCategory,
  deleteSynonBiomedMemory,
  deleteSynonBiomedMemoryCategory,
  loadSynonBiomedMemoryContext,
  loadSynonBiomedSessionMemories,
  setSynonBiomedMemoryEnabled,
  setSynonBiomedProjectMemoryEnabled,
  updateSynonBiomedMemory,
  updateSynonBiomedMemoryCategory,
} from '@/renderer/services/synonBiomedMemory';

describe('Synon Biomed memory service', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('maps the real memory context and session response shapes', async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
      if (url === '/api/memory/context?project_id=proj_a') {
        return Response.json({
          entities: [
            {
              entity_key: 'profile',
              label: 'About you',
              project_id: null,
              rows: [
                {
                  id: 'mem_1',
                  body: '关注肿瘤免疫研究',
                  categoryId: 'memcat_1',
                  origin: 'user',
                  evidence: 'stated',
                  createdAt: '2026-07-10T00:00:00.000Z',
                  updatedAt: '2026-07-10T01:00:00.000Z',
                },
              ],
            },
          ],
          sessions: [
            {
              frame_id: 'frame_1',
              project_id: 'proj_a',
              label: 'CRBN 研究',
              row_count: 2,
              newest_at: '2026-07-10T02:00:00.000Z',
            },
          ],
          categories: [
            {
              id: 'memcat_1',
              name: '研究偏好',
              guidance: '用于研究规划',
              auto_recall: true,
              row_count: 1,
            },
          ],
          memory_enabled: true,
          total_user_rows: 3,
        });
      }
      if (url === '/api/memory/sessions/frame_1') {
        return Response.json({
          rows: [
            {
              id: 'mem_session',
              body: '本会话使用 CRBN 数据集',
              origin: 'assistant',
              evidence: 'inferred',
              createdAt: '2026-07-10T02:00:00.000Z',
              updatedAt: '2026-07-10T02:00:00.000Z',
            },
          ],
        });
      }
      return Response.json({ detail: 'not found' }, { status: 404 });
    });
    vi.stubGlobal('fetch', fetchMock);

    const context = await loadSynonBiomedMemoryContext({ projectId: 'proj_a' });
    expect(context).toMatchObject({
      enabled: true,
      totalRows: 3,
      entities: [
        {
          entityKey: 'profile',
          label: 'About you',
          projectId: null,
          rows: [{ id: 'mem_1', body: '关注肿瘤免疫研究', categoryId: 'memcat_1' }],
        },
      ],
      sessions: [{ frameId: 'frame_1', projectId: 'proj_a', rowCount: 2 }],
      categories: [{ id: 'memcat_1', name: '研究偏好', autoRecall: true, rowCount: 1 }],
    });
    const sessionRows = await loadSynonBiomedSessionMemories('frame_1');
    expect(sessionRows).toEqual([
      expect.objectContaining({ id: 'mem_session', body: '本会话使用 CRBN 数据集', origin: 'assistant' }),
    ]);
  });

  it('uses the native memory CRUD and category contracts', async () => {
    const observed: Array<{ url: string; method: string; body?: unknown }> = [];
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
      observed.push({
        url,
        method: init?.method ?? 'GET',
        body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined,
      });
      if (url === '/api/memories' && init?.method === 'POST') {
        return Response.json({ id: 'mem_created', body: '新记忆', origin: 'user' }, { status: 201 });
      }
      if (url === '/api/memory/categories' && init?.method === 'POST') {
        return Response.json(
          { id: 'memcat_created', name: '研究偏好', auto_recall: true, row_count: 0 },
          { status: 201 }
        );
      }
      return new Response(null, { status: 204 });
    });
    vi.stubGlobal('fetch', fetchMock);

    await setSynonBiomedMemoryEnabled(true);
    await createSynonBiomedMemory({ text: '新记忆', entity: 'profile', category: '研究偏好' });
    await updateSynonBiomedMemory('mem_created', { text: '更新后的记忆' });
    await deleteSynonBiomedMemory('mem_created');
    await clearSynonBiomedMemories();
    await createSynonBiomedMemoryCategory({ name: '研究偏好', guidance: '用于研究规划', autoRecall: true });
    await updateSynonBiomedMemoryCategory('memcat_created', {
      name: '研究方向',
      guidance: '更新后的说明',
      autoRecall: false,
    });
    await deleteSynonBiomedMemoryCategory('memcat_created', false);

    expect(observed).toEqual([
      { url: '/api/memory/enabled', method: 'PUT', body: { enabled: true } },
      {
        url: '/api/memories',
        method: 'POST',
        body: { text: '新记忆', entity: 'profile', category: '研究偏好' },
      },
      { url: '/api/memories/mem_created', method: 'PUT', body: { text: '更新后的记忆' } },
      { url: '/api/memories/mem_created', method: 'DELETE', body: undefined },
      { url: '/api/memories', method: 'DELETE', body: undefined },
      {
        url: '/api/memory/categories',
        method: 'POST',
        body: { name: '研究偏好', guidance: '用于研究规划', auto_recall: true },
      },
      {
        url: '/api/memory/categories/memcat_created',
        method: 'PUT',
        body: { name: '研究方向', guidance: '更新后的说明', auto_recall: false },
      },
      {
        url: '/api/memory/categories/memcat_created?delete_facts=false',
        method: 'DELETE',
        body: undefined,
      },
    ]);
  });

  it('writes project memory settings to the selected project endpoint', async () => {
    const observed: Array<{ url: string; method: string; body?: unknown }> = [];
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
      observed.push({
        url,
        method: init?.method ?? 'GET',
        body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined,
      });
      return new Response(null, { status: 204 });
    });
    vi.stubGlobal('fetch', fetchMock);

    await setSynonBiomedProjectMemoryEnabled('proj/a', true);

    expect(observed).toEqual([
      {
        url: '/api/projects/proj%2Fa/memory/enabled',
        method: 'PUT',
        body: { enabled: true },
      },
    ]);
    await expect(setSynonBiomedProjectMemoryEnabled('  ', true)).rejects.toThrow('Synon Biomed project id is required');
  });

  it('defaults category creation to automatic recall', async () => {
    const observed: unknown[] = [];
    const fetchMock = vi.fn(async (_input: string | URL | Request, init?: RequestInit) => {
      observed.push(typeof init?.body === 'string' ? JSON.parse(init.body) : undefined);
      return Response.json(
        {
          id: 'memcat_default',
          name: 'Methods',
          guidance: 'Recall for experimental methods.',
          auto_recall: true,
          row_count: 0,
        },
        { status: 201 }
      );
    });
    vi.stubGlobal('fetch', fetchMock);

    await createSynonBiomedMemoryCategory({
      name: 'Methods',
      guidance: 'Recall for experimental methods.',
    });

    expect(observed).toEqual([
      {
        name: 'Methods',
        guidance: 'Recall for experimental methods.',
        auto_recall: true,
      },
    ]);
  });
});
