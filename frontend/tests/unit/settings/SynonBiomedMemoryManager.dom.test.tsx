import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Message } from '@arco-design/web-react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { SynonBiomedMemoryManager } from '@/renderer/pages/settings/components/SynonBiomedMemoryManager';
import { renderWithI18n } from '../i18nTestUtils';

const memoryContext = {
  entities: [
    {
      entity_key: 'profile',
      label: '关于你',
      project_id: null,
      rows: [
        {
          id: 'mem_profile',
          body: '关注肿瘤免疫研究',
          categoryId: null,
          origin: 'user',
          evidence: 'stated',
          createdAt: '2026-07-10T00:00:00.000Z',
          updatedAt: '2026-07-10T00:00:00.000Z',
        },
      ],
    },
    {
      entity_key: 'project:proj_a',
      label: 'CRBN 项目',
      project_id: 'proj_a',
      rows: [
        {
          id: 'mem_project',
          body: '项目采用 CRBN 数据集',
          categoryId: 'memcat_1',
          origin: 'assistant',
          evidence: 'inferred',
          createdAt: '2026-07-10T01:00:00.000Z',
          updatedAt: '2026-07-10T01:00:00.000Z',
        },
      ],
    },
  ],
  sessions: [
    {
      frame_id: 'frame_1',
      project_id: 'proj_a',
      label: 'CRBN 结构分析',
      row_count: 1,
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
  memory_enabled: false,
  total_user_rows: 2,
};

const projectMemoryContext = {
  ...memoryContext,
  entities: [memoryContext.entities[1]],
  total_user_rows: 1,
};

const projectCatalog = {
  projects: [
    {
      project_id: 'proj_a',
      name: 'CRBN 项目',
      description: 'CRBN 结构分析与设计规则',
      context: null,
      conversation_count: 1,
      artifact_count: 0,
      created_at: '2026-07-10T00:00:00.000Z',
      updated_at: '2026-07-10T00:00:00.000Z',
      last_active_at: '2026-07-10T00:00:00.000Z',
    },
  ],
};

function createFetchMock(options: { failEnable?: boolean } = {}) {
  const observed: Array<{ url: string; method: string; body?: unknown }> = [];
  const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
    const method = init?.method ?? 'GET';
    observed.push({
      url,
      method,
      body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined,
    });
    if (url === '/api/projects' && method === 'GET') return Response.json(projectCatalog);
    if (url === '/api/memory/context' && method === 'GET') return Response.json(memoryContext);
    if (url === '/api/memory/context?project_id=proj_a' && method === 'GET') {
      return Response.json(projectMemoryContext);
    }
    if (url === '/api/memory/enabled' && method === 'GET') return Response.json({ enabled: false });
    if (url === '/api/memory/enabled' && method === 'PUT' && options.failEnable) {
      return Response.json({ error: 'memory unavailable' }, { status: 503 });
    }
    if (url === '/api/memory/sessions/frame_1' && method === 'GET') {
      return Response.json({
        rows: [
          {
            id: 'mem_session',
            body: '会话使用 CRBN 结构数据',
            origin: 'assistant',
            evidence: 'inferred',
            createdAt: '2026-07-10T02:00:00.000Z',
            updatedAt: '2026-07-10T02:00:00.000Z',
          },
        ],
      });
    }
    if (url === '/api/memories' && method === 'POST') {
      return Response.json({
        id: 'mem_created',
        body: '新的研究偏好',
        origin: 'user',
        evidence: 'stated',
        createdAt: '2026-07-10T03:00:00.000Z',
        updatedAt: '2026-07-10T03:00:00.000Z',
      });
    }
    if (url === '/api/memory/categories' && method === 'POST') {
      const body = typeof init?.body === 'string' ? JSON.parse(init.body) : {};
      return Response.json(
        {
          id: 'memcat_created',
          name: body.name,
          guidance: body.guidance,
          auto_recall: body.auto_recall,
          row_count: 0,
        },
        { status: 201 }
      );
    }
    return new Response(null, { status: 204 });
  });
  return { fetchMock, observed };
}

async function chooseMemoryScope(value: string) {
  const select = await screen.findByTestId('memory-scope-select');
  fireEvent.click(select.closest('.arco-select') ?? select);

  const labels: Record<string, string> = {
    'category:memcat_1': '研究偏好',
    'entity:project:proj_a': 'CRBN 项目',
    'session:frame_1': 'CRBN 结构分析',
  };

  const option = await waitFor(() => {
    const candidate = Array.from(document.querySelectorAll<HTMLElement>('.arco-select-option, [role="option"]')).find(
      (node) =>
        node.getAttribute('data-value') === value ||
        node.getAttribute('data-key') === value ||
        node.textContent?.includes(labels[value])
    );
    if (!candidate) {
      const options = Array.from(document.querySelectorAll<HTMLElement>('.arco-select-option, [role="option"]'))
        .map((node) => node.textContent?.trim())
        .join(', ');
      throw new Error(`Memory scope option not found: ${value}; options: ${options}`);
    }
    return candidate;
  });

  fireEvent.click(option);
}

async function waitForProjectLayer() {
  await waitFor(() => expect(screen.getByRole('heading', { level: 4 })).toHaveTextContent('CRBN 项目'));
}

describe('SynonBiomedMemoryManager', () => {
  beforeEach(() => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    vi.spyOn(Message, 'success').mockReturnValue({ close: () => undefined });
    vi.spyOn(Message, 'warning').mockReturnValue({ close: () => undefined });
    vi.spyOn(Message, 'error').mockReturnValue({ close: () => undefined });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('renders native profile, category, project and session scopes from the real context model', async () => {
    const { fetchMock } = createFetchMock();
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(<SynonBiomedMemoryManager onChanged={() => undefined} />, 'zh-CN');

    await waitFor(() => expect(screen.getByTestId('memory-manager')).toBeInTheDocument());
    expect(screen.getByTestId('memory-enabled-toggle')).toHaveAttribute('aria-checked', 'false');
    expect(screen.getByTestId('memory-scope-select')).toBeInTheDocument();
    expect(screen.queryByRole('navigation', { name: '记忆范围' })).not.toBeInTheDocument();
    expect(screen.getByTestId('memory-layer-project')).toHaveTextContent('项目记忆');
    expect(screen.getByTestId('memory-row-mem_profile')).toHaveTextContent('关注肿瘤免疫研究');

    await chooseMemoryScope('category:memcat_1');
    expect(await screen.findByTestId('memory-category-delete-memcat_1')).toBeInTheDocument();

    fireEvent.click(screen.getByTestId('memory-layer-project'));
    await waitForProjectLayer();
    await chooseMemoryScope('entity:project:proj_a');
    await chooseMemoryScope('session:frame_1');
    await waitFor(() =>
      expect(screen.getByTestId('memory-row-mem_session')).toHaveTextContent('会话使用 CRBN 结构数据')
    );
  });

  it('shows the project selector only when it offers a real project choice', async () => {
    const { fetchMock: baseFetch } = createFetchMock();
    const fetchMock = vi.fn((input: string | URL | Request, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
      if (url === '/api/projects') {
        return Promise.resolve(
          Response.json({
            projects: [
              ...projectCatalog.projects,
              {
                ...projectCatalog.projects[0],
                project_id: 'proj_b',
                name: 'EGFR 项目',
              },
            ],
          })
        );
      }
      return baseFetch(input, init);
    });
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(<SynonBiomedMemoryManager />, 'zh-CN');
    fireEvent.click(await screen.findByTestId('memory-layer-project'));

    await waitFor(() => expect(screen.getByTestId('memory-project-select')).toHaveTextContent('CRBN 项目'));
    expect(screen.getByTestId('memory-project-picker')).toBeInTheDocument();
  });

  it('keeps the selected project visible when a refresh removes its rows', async () => {
    let projectLoads = 0;
    let projectEnabled = false;
    const observed: Array<{ url: string; method: string; body?: unknown }> = [];
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
      const method = init?.method ?? 'GET';
      observed.push({
        url,
        method,
        body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined,
      });
      if (url === '/api/projects' && method === 'GET') return Response.json(projectCatalog);
      if (url === '/api/memory/context?project_id=proj_a' && method === 'GET') {
        projectLoads += 1;
        return Response.json({
          ...projectMemoryContext,
          memory_enabled: projectEnabled,
          entities: projectLoads === 1 ? projectMemoryContext.entities : [],
          sessions: projectLoads === 1 ? projectMemoryContext.sessions : [],
        });
      }
      if (url === '/api/memory/context' && method === 'GET') {
        return Response.json(memoryContext);
      }
      if (url === '/api/memory/enabled' && method === 'GET') return Response.json({ enabled: false });
      if (url === '/api/memory/enabled' && method === 'PUT') return new Response(null, { status: 204 });
      if (url === '/api/projects/proj_a/memory/enabled' && method === 'PUT') {
        projectEnabled = (typeof init?.body === 'string' ? JSON.parse(init.body) : {}).enabled === true;
        return Response.json({ enabled: projectEnabled });
      }
      return new Response(null, { status: 204 });
    });
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(<SynonBiomedMemoryManager />, 'zh-CN');
    fireEvent.click(await screen.findByTestId('memory-layer-project'));
    await waitForProjectLayer();
    await chooseMemoryScope('entity:project:proj_a');
    expect(screen.getByRole('heading', { level: 4 })).toHaveTextContent('CRBN');

    fireEvent.click(screen.getByTestId('memory-disabled-enable'));
    await waitFor(() =>
      expect(observed).toContainEqual({
        url: '/api/projects/proj_a/memory/enabled',
        method: 'PUT',
        body: { enabled: true },
      })
    );
    expect(observed).not.toContainEqual({ url: '/api/memory/enabled', method: 'PUT', body: { enabled: true } });

    await waitForProjectLayer();
    expect(screen.getByRole('heading', { level: 4 })).toHaveTextContent('CRBN 项目');
    expect(screen.getByText('当前项目还没有记忆。添加第一条项目规则、背景或工作要求。')).toBeInTheDocument();
  });

  it('shows one recoverable inline error instead of a misleading disabled-memory state', async () => {
    const { fetchMock: healthyFetch } = createFetchMock();
    let unavailable = true;
    const fetchMock = vi.fn((input: string | URL | Request, init?: RequestInit) =>
      unavailable ? Promise.resolve(Response.json({ detail: 'not found' }, { status: 404 })) : healthyFetch(input, init)
    );
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(<SynonBiomedMemoryManager />, 'zh-CN');

    expect(await screen.findByRole('alert')).toHaveTextContent('记忆数据暂时不可用。');
    expect(screen.queryByTestId('memory-enabled-toggle')).not.toBeInTheDocument();
    expect(Message.error).not.toHaveBeenCalled();

    unavailable = false;
    fireEvent.click(screen.getByRole('button', { name: '重试' }));
    await waitFor(() => expect(screen.getByTestId('memory-enabled-toggle')).toBeInTheDocument());
  });

  it('toggles memory and creates a note through native Synon Biomed write contracts', async () => {
    const { fetchMock, observed } = createFetchMock();
    vi.stubGlobal('fetch', fetchMock);
    const onChanged = vi.fn();

    await renderWithI18n(<SynonBiomedMemoryManager onChanged={onChanged} />, 'zh-CN');
    await waitFor(() => expect(screen.getByTestId('memory-row-mem_profile')).toBeInTheDocument());

    fireEvent.click(screen.getByTestId('memory-disabled-enable'));
    await waitFor(() =>
      expect(observed).toContainEqual({ url: '/api/memory/enabled', method: 'PUT', body: { enabled: true } })
    );

    fireEvent.click(screen.getByTestId('memory-add'));
    fireEvent.change(screen.getByTestId('memory-new-input'), { target: { value: '新的研究偏好' } });
    fireEvent.click(screen.getByTestId('memory-new-save'));

    await waitFor(() =>
      expect(observed).toContainEqual({
        url: '/api/memories',
        method: 'POST',
        body: { text: '新的研究偏好', entity: 'profile' },
      })
    );
    expect(onChanged).toHaveBeenCalled();
  });

  it('writes new memory to the selected project entity', async () => {
    const { fetchMock, observed } = createFetchMock();
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(<SynonBiomedMemoryManager />, 'zh-CN');
    fireEvent.click(await screen.findByTestId('memory-layer-project'));
    await waitForProjectLayer();
    await chooseMemoryScope('entity:project:proj_a');
    fireEvent.click(screen.getByTestId('memory-add'));
    fireEvent.change(screen.getByTestId('memory-new-input'), {
      target: { value: '项目必须保留可复现实验记录' },
    });
    fireEvent.click(screen.getByTestId('memory-new-save'));

    await waitFor(() =>
      expect(observed).toContainEqual({
        url: '/api/memories',
        method: 'POST',
        body: { text: '项目必须保留可复现实验记录', entity: 'project:proj_a' },
      })
    );
  });

  it('edits the selected project constraint inline', async () => {
    const { fetchMock, observed } = createFetchMock();
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(<SynonBiomedMemoryManager />, 'zh-CN');
    fireEvent.click(await screen.findByTestId('memory-layer-project'));
    await waitForProjectLayer();
    await chooseMemoryScope('entity:project:proj_a');
    fireEvent.click(await screen.findByTestId('memory-row-edit-mem_project'));

    const editor = document.querySelector<HTMLTextAreaElement>('textarea');
    expect(editor).not.toBeNull();
    fireEvent.change(editor!, { target: { value: '项目规则已更新，保留完整可复现实验记录' } });
    fireEvent.click(screen.getByRole('button', { name: '保存', exact: true }));

    await waitFor(() =>
      expect(observed).toContainEqual({
        url: '/api/memories/mem_project',
        method: 'PUT',
        body: { text: '项目规则已更新，保留完整可复现实验记录' },
      })
    );
  });

  it('updates category auto-recall with the native category contract', async () => {
    const { fetchMock, observed } = createFetchMock();
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(<SynonBiomedMemoryManager />, 'zh-CN');
    await chooseMemoryScope('category:memcat_1');
    await waitFor(() => expect(screen.queryByTestId('memory-row-mem_project')).toBeNull());
    fireEvent.click(await screen.findByTestId('memory-category-auto-recall-memcat_1'));

    await waitFor(() =>
      expect(observed).toContainEqual({
        url: '/api/memory/categories/memcat_1',
        method: 'PUT',
        body: { name: '研究偏好', guidance: '用于研究规划', auto_recall: false },
      })
    );
  });

  it('names the selected category in the delete confirmation', async () => {
    const { fetchMock } = createFetchMock();
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(<SynonBiomedMemoryManager />, 'zh-CN');
    await chooseMemoryScope('category:memcat_1');
    fireEvent.click(await screen.findByTestId('memory-category-delete-memcat_1'));

    expect(await screen.findByText('删除“研究偏好”后，可选择保留其中记忆或同时删除。')).toBeInTheDocument();
    expect(screen.queryByText(/\{\{name\}\}/)).not.toBeInTheDocument();
  });

  it('requires category guidance and defaults new categories to automatic recall', async () => {
    const { fetchMock, observed } = createFetchMock();
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(<SynonBiomedMemoryManager />, 'zh-CN');
    await screen.findByTestId('memory-category-add');
    fireEvent.click(screen.getByTestId('memory-category-add'));

    expect(screen.getByTestId('memory-category-draft-auto-recall')).toHaveAttribute('aria-checked', 'true');
    fireEvent.change(screen.getByTestId('memory-category-name'), { target: { value: 'Methods' } });
    const saveCategory = document.querySelector<HTMLButtonElement>('#memory-category-save');
    expect(saveCategory).not.toBeNull();
    expect(saveCategory).toBeDisabled();

    fireEvent.change(screen.getByTestId('memory-category-guidance'), {
      target: { value: 'Recall only for experimental methods and protocol decisions.' },
    });
    expect(saveCategory).toBeEnabled();
    fireEvent.click(saveCategory!);

    await waitFor(() =>
      expect(observed).toContainEqual({
        url: '/api/memory/categories',
        method: 'POST',
        body: {
          name: 'Methods',
          guidance: 'Recall only for experimental methods and protocol decisions.',
          auto_recall: true,
        },
      })
    );
  });

  it('disables category creation at the workspace limit of ten', async () => {
    const fullContext = {
      ...memoryContext,
      categories: Array.from({ length: 10 }, (_, index) => ({
        id: `memcat_${index + 1}`,
        name: `Category ${index + 1}`,
        guidance: `Guidance ${index + 1}`,
        auto_recall: true,
        row_count: 0,
      })),
    };
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
      if (url === '/api/memory/context') return Response.json(fullContext);
      if (url === '/api/memory/enabled') return Response.json({ enabled: true });
      return new Response(null, { status: 204 });
    });
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(<SynonBiomedMemoryManager />, 'zh-CN');

    expect(await screen.findByTestId('memory-category-count')).toHaveTextContent('10/10');
    expect(screen.getByTestId('memory-category-add')).toBeDisabled();
  });

  it('restores the memory toggle and reports an error when the backend write fails', async () => {
    const { fetchMock } = createFetchMock({ failEnable: true });
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(<SynonBiomedMemoryManager />, 'zh-CN');
    await waitFor(() => expect(screen.getByTestId('memory-disabled-enable')).toBeInTheDocument());

    fireEvent.click(screen.getByTestId('memory-disabled-enable'));

    await waitFor(() => expect(Message.error).toHaveBeenCalled());
    expect(screen.getByTestId('memory-enabled-toggle')).toHaveAttribute('aria-checked', 'false');
    expect(screen.getByTestId('memory-disabled-enable')).toBeInTheDocument();
  });

  it('shows a retryable session error and replaces it with rows after recovery', async () => {
    const { fetchMock: healthyFetch } = createFetchMock();
    let sessionAttempts = 0;
    const fetchMock = vi.fn((input: string | URL | Request, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
      if (url === '/api/memory/sessions/frame_1' && sessionAttempts++ === 0) {
        return Promise.resolve(Response.json({ detail: 'temporarily unavailable' }, { status: 503 }));
      }
      return healthyFetch(input, init);
    });
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(<SynonBiomedMemoryManager />, 'zh-CN');
    fireEvent.click(await screen.findByTestId('memory-layer-project'));
    await waitForProjectLayer();
    await chooseMemoryScope('entity:project:proj_a');
    await chooseMemoryScope('session:frame_1');

    const error = await screen.findByTestId('memory-session-error');
    expect(error).toHaveTextContent('无法加载会话记忆。');
    fireEvent.click(within(error).getByRole('button', { name: '重试' }));
    expect(await screen.findByTestId('memory-row-mem_session')).toHaveTextContent('会话使用 CRBN 结构数据');
  });

  it('does not report a successful write as failed when post-write refreshes fail', async () => {
    const { fetchMock: healthyFetch } = createFetchMock();
    let contextLoads = 0;
    const fetchMock = vi.fn((input: string | URL | Request, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
      if (url === '/api/memory/context' && contextLoads++ > 0) {
        return Promise.resolve(Response.json({ detail: 'refresh unavailable' }, { status: 503 }));
      }
      return healthyFetch(input, init);
    });
    vi.stubGlobal('fetch', fetchMock);
    const onChanged = vi.fn().mockRejectedValue(new Error('consumer refresh failed'));

    await renderWithI18n(<SynonBiomedMemoryManager onChanged={onChanged} />, 'zh-CN');
    await screen.findByTestId('memory-row-mem_profile');
    fireEvent.click(screen.getByTestId('memory-add'));
    fireEvent.change(screen.getByTestId('memory-new-input'), { target: { value: '新的研究偏好' } });
    fireEvent.click(screen.getByTestId('memory-new-save'));

    await waitFor(() => expect(Message.success).toHaveBeenCalledWith('记忆已保存。'));
    expect(Message.error).not.toHaveBeenCalled();
    expect(Message.warning).toHaveBeenCalledTimes(1);
    expect(screen.queryByTestId('memory-new-input')).not.toBeInTheDocument();
  });
});
