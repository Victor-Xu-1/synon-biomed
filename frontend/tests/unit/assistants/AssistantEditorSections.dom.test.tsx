import React from 'react';
import type { i18n } from 'i18next';
import { I18nextProvider } from 'react-i18next';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { ConfigProvider } from '@arco-design/web-react';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import AssistantEditorSections from '@/renderer/pages/settings/SynonBiomedExpertsSettings/AssistantEditorSections';
import type { AssistantEditorViewModel } from '@/renderer/pages/settings/SynonBiomedExpertsSettings/types';
import { createTestI18n } from '../i18nTestUtils';

let mockManagedAgentRuntimeCatalog: Array<{
  id: string;
  available_modes?: unknown;
  config_options?: unknown;
}> = [];
const showOpenInvokeMock = vi.fn();
const getImageBase64InvokeMock = vi.fn();
let testI18n: i18n;

vi.mock('@/renderer/hooks/synonBiomed/runtime/useManagedAgents', () => ({
  useManagedAgentRuntimeCatalog: () => mockManagedAgentRuntimeCatalog,
}));

vi.mock('@/renderer/components/chat/EmojiPicker', () => ({
  default: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

vi.mock('@/renderer/components/Markdown', () => ({
  default: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    dialog: {
      showOpen: {
        invoke: (...args: unknown[]) => showOpenInvokeMock(...args),
      },
    },
    fs: {
      getImageBase64: {
        invoke: (...args: unknown[]) => getImageBase64InvokeMock(...args),
      },
    },
  },
}));

const renderWithProviders = (ui: React.ReactElement) =>
  render(
    <I18nextProvider i18n={testI18n}>
      <MemoryRouter>
        <ConfigProvider>{ui}</ConfigProvider>
      </MemoryRouter>
    </I18nextProvider>
  );

const createEditor = (overrides: Partial<AssistantEditorViewModel> = {}): AssistantEditorViewModel => {
  const base: AssistantEditorViewModel = {
    isCreating: true,
    profile: {
      name: 'Writer',
      setName: vi.fn(),
      description: 'desc',
      setDescription: vi.fn(),
      avatar: '✍️',
      setAvatar: vi.fn(),
      setAvatarPreview: vi.fn(),
      builtinAvatarOptions: [],
    },
    agent: {
      value: 'claude',
      setValue: vi.fn(),
      availableBackends: [],
    },
    prompts: {
      text: '',
      setText: vi.fn(),
    },
    defaults: {
      model: { mode: 'auto', setMode: vi.fn(), value: '', setValue: vi.fn() },
      permission: { mode: 'auto', setMode: vi.fn(), value: '', setValue: vi.fn() },
      thoughtLevel: { mode: 'auto', setMode: vi.fn(), value: '', setValue: vi.fn() },
      skills: { mode: 'fixed', setMode: vi.fn() },
      mcps: { mode: 'fixed', setMode: vi.fn(), availableServers: [], selectedIds: [], setSelectedIds: vi.fn() },
    },
    rules: {
      content: 'rules',
      setContent: vi.fn(),
      viewMode: 'preview',
      setViewMode: vi.fn(),
    },
    skills: {
      availableSkills: [],
      selectedSkills: [],
      setSelectedSkills: vi.fn(),
      pendingSkills: [],
      setDeletePendingSkillName: vi.fn(),
      setDeleteCustomSkillName: vi.fn(),
      builtinAutoSkills: [],
      disabledBuiltinSkills: [],
      setDisabledBuiltinSkills: vi.fn(),
    },
    actions: {
      save: vi.fn(),
      requestDelete: vi.fn(),
      duplicate: vi.fn(),
    },
  };

  return {
    ...base,
    ...overrides,
    profile: { ...base.profile, ...overrides.profile },
    agent: { ...base.agent, ...overrides.agent },
    prompts: { ...base.prompts, ...overrides.prompts },
    defaults: {
      ...base.defaults,
      ...overrides.defaults,
      model: { ...base.defaults.model, ...overrides.defaults?.model },
      permission: { ...base.defaults.permission, ...overrides.defaults?.permission },
      thoughtLevel: { ...base.defaults.thoughtLevel, ...overrides.defaults?.thoughtLevel },
      skills: { ...base.defaults.skills, ...overrides.defaults?.skills },
      mcps: { ...base.defaults.mcps, ...overrides.defaults?.mcps },
    },
    rules: { ...base.rules, ...overrides.rules },
    skills: { ...base.skills, ...overrides.skills },
    actions: { ...base.actions, ...overrides.actions },
  };
};

const backendOption = (id: string, runtimeKey: string, name = runtimeKey) => ({
  id,
  name,
  runtimeKey,
  isExtension: false,
  modelOptions: [],
});

describe('AssistantEditorSections', () => {
  beforeAll(async () => {
    testI18n = await createTestI18n('en-US');
  });

  beforeEach(async () => {
    await testI18n.changeLanguage('en-US');
    showOpenInvokeMock.mockReset();
    getImageBase64InvokeMock.mockReset();
    getImageBase64InvokeMock.mockResolvedValue('data:image/png;base64,preview');
    mockManagedAgentRuntimeCatalog = [
      {
        id: 'agent-codex',
        available_modes: {
          current_mode_id: 'auto',
          available_modes: [
            { id: 'read-only', name: 'Read Only' },
            { id: 'auto', name: 'Auto' },
            { id: 'full-access', name: 'Full Access' },
          ],
        },
      },
    ];
  });

  it('renders all default configuration rows in a single card', () => {
    renderWithProviders(
      <AssistantEditorSections
        editor={createEditor({
          prompts: { text: 'Prompt one\nPrompt two', setText: vi.fn() },
          defaults: {
            mcps: {
              mode: 'fixed',
              setMode: vi.fn(),
              availableServers: [],
              selectedIds: ['filesystem'],
              setSelectedIds: vi.fn(),
            },
          },
          skills: {
            availableSkills: [
              { name: 'browse', description: 'Browse the web', location: '', is_custom: false, source: 'builtin' },
            ],
            selectedSkills: ['browse'],
            setSelectedSkills: vi.fn(),
            pendingSkills: [],
            setDeletePendingSkillName: vi.fn(),
            setDeleteCustomSkillName: vi.fn(),
            builtinAutoSkills: [],
            disabledBuiltinSkills: [],
            setDisabledBuiltinSkills: vi.fn(),
          },
        })}
        activeAssistant={null}
      />
    );

    const defaultsCard = screen.getByTestId('assistant-card-defaults');
    const defaultsScope = within(defaultsCard);
    expect(defaultsScope.getByText('Model')).toBeInTheDocument();
    expect(defaultsScope.getByText('Permission')).toBeInTheDocument();
    expect(defaultsScope.getByText('Skills')).toBeInTheDocument();
    expect(defaultsScope.getByText('MCP')).toBeInTheDocument();
    expect(
      defaultsScope.getByText(
        'Remember last used only takes effect after this assistant has recorded a previous selection.'
      )
    ).toBeInTheDocument();
  });

  it('renders auto defaults consistently for model, permission, skills, and MCP', () => {
    renderWithProviders(
      <AssistantEditorSections
        editor={createEditor({
          defaults: {
            model: { mode: 'auto', setMode: vi.fn(), value: '', setValue: vi.fn() },
            permission: { mode: 'auto', setMode: vi.fn(), value: '', setValue: vi.fn() },
            skills: { mode: 'auto', setMode: vi.fn() },
            mcps: {
              mode: 'auto',
              setMode: vi.fn(),
              availableServers: [{ id: 'mcp-a', name: 'Server A', enabled: true } as any],
              selectedIds: [],
              setSelectedIds: vi.fn(),
            },
          },
          skills: {
            availableSkills: [
              { name: 'browse', description: 'Browse the web', location: '', is_custom: false, source: 'builtin' },
            ],
            selectedSkills: [],
            setSelectedSkills: vi.fn(),
            pendingSkills: [],
            setDeletePendingSkillName: vi.fn(),
            setDeleteCustomSkillName: vi.fn(),
            builtinAutoSkills: [],
            disabledBuiltinSkills: [],
            setDisabledBuiltinSkills: vi.fn(),
          },
        })}
        activeAssistant={null}
      />
    );

    expect(screen.getByTestId('select-assistant-default-model')).toHaveTextContent('Remember last used automatically');
    expect(screen.getByTestId('select-assistant-default-permission')).toHaveTextContent(
      'Remember last used automatically'
    );
    expect(screen.getByTestId('select-assistant-default-skills')).toHaveTextContent('Remember last used automatically');
    expect(screen.getByTestId('select-assistant-default-mcp')).toHaveTextContent('Remember last used automatically');
    expect(screen.getByTestId('select-assistant-default-skills').className).toMatch(/summarySelect/);
    expect(screen.getByTestId('select-assistant-default-mcp').className).toMatch(/summarySelect/);
  });

  it('refreshes default permission labels when the language changes', async () => {
    const editor = createEditor({
      agent: {
        value: 'agent-codex',
        setValue: vi.fn(),
        availableBackends: [backendOption('agent-codex', 'codex', 'Codex')],
      },
    });

    renderWithProviders(<AssistantEditorSections editor={editor} activeAssistant={null} />);

    fireEvent.click(screen.getByTestId('select-assistant-default-permission'));
    expect(screen.getByText('Read Only')).toBeInTheDocument();
    expect(screen.getByText('Auto')).toBeInTheDocument();
    expect(screen.getByText('Full Access')).toBeInTheDocument();

    await act(async () => {
      await testI18n.changeLanguage('zh-CN');
    });

    fireEvent.click(screen.getByTestId('select-assistant-default-permission'));
    await waitFor(() => {
      expect(screen.getByText('只读')).toBeInTheDocument();
      expect(screen.getByText('自动')).toBeInTheDocument();
      expect(screen.getByText('完全访问')).toBeInTheDocument();
    });
  });

  it('renders localized default permission options on initial non-English render', async () => {
    await testI18n.changeLanguage('zh-CN');

    renderWithProviders(
      <AssistantEditorSections
        editor={createEditor({
          agent: {
            value: 'agent-codex',
            setValue: vi.fn(),
            availableBackends: [backendOption('agent-codex', 'codex', 'Codex')],
          },
        })}
        activeAssistant={null}
      />
    );

    fireEvent.click(screen.getByTestId('select-assistant-default-permission'));
    await waitFor(() => {
      expect(screen.getByText('只读')).toBeInTheDocument();
      expect(screen.getByText('自动')).toBeInTheDocument();
      expect(screen.getByText('完全访问')).toBeInTheDocument();
    });
  });

  it('uses the active language resources on an initial Chinese render', async () => {
    await testI18n.changeLanguage('zh-CN');

    renderWithProviders(
      <AssistantEditorSections
        editor={createEditor({
          agent: {
            value: 'agent-codex',
            setValue: vi.fn(),
            availableBackends: [backendOption('agent-codex', 'codex', 'Codex')],
          },
        })}
        activeAssistant={null}
      />
    );

    fireEvent.click(screen.getByTestId('select-assistant-default-permission'));
    await waitFor(() => {
      expect(screen.getByText('只读')).toBeInTheDocument();
      expect(screen.getByText('自动')).toBeInTheDocument();
      expect(screen.getByText('完全访问')).toBeInTheDocument();
    });
  });

  it('refreshes default permission labels across consecutive language changes', async () => {
    const editor = createEditor({
      agent: {
        value: 'agent-codex',
        setValue: vi.fn(),
        availableBackends: [backendOption('agent-codex', 'codex', 'Codex')],
      },
    });

    renderWithProviders(<AssistantEditorSections editor={editor} activeAssistant={null} />);

    fireEvent.click(screen.getByTestId('select-assistant-default-permission'));
    expect(screen.getByText('Read Only')).toBeInTheDocument();
    expect(screen.getByText('Auto')).toBeInTheDocument();
    expect(screen.getByText('Full Access')).toBeInTheDocument();

    await act(async () => {
      await testI18n.changeLanguage('zh-CN');
    });

    fireEvent.click(screen.getByTestId('select-assistant-default-permission'));
    await waitFor(() => {
      expect(screen.getByText('只读')).toBeInTheDocument();
      expect(screen.getByText('自动')).toBeInTheDocument();
      expect(screen.getByText('完全访问')).toBeInTheDocument();
    });
  });

  it('keeps builtin and disabled MCP servers in the default MCP summary', () => {
    renderWithProviders(
      <AssistantEditorSections
        editor={createEditor({
          isCreating: false,
          defaults: {
            mcps: {
              mode: 'fixed',
              setMode: vi.fn(),
              availableServers: [
                { id: 'mcp-user', name: 'User MCP', enabled: true, builtin: false } as any,
                { id: 'mcp-disabled', name: 'Disabled MCP', enabled: false, builtin: false } as any,
                { id: 'mcp-builtin', name: 'Builtin MCP', enabled: false, builtin: true } as any,
              ],
              selectedIds: ['mcp-user', 'mcp-disabled', 'mcp-builtin'],
              setSelectedIds: vi.fn(),
            },
          },
        })}
        activeAssistant={{
          id: 'builtin-assistant',
          source: 'builtin',
          name: 'Builtin assistant',
          description: '',
          avatar: '🤖',
          enabled: true,
          sort_order: 1,
          agent_id: 'agent-claude',
          agent: { type: 'acp', source: 'builtin', acp_backend: 'claude' },
        }}
      />
    );

    const defaultsCard = screen.getByTestId('assistant-card-defaults');
    expect(within(defaultsCard).getByText('User MCP, Disabled MCP, Builtin MCP')).toBeInTheDocument();
  });

  it('uses runtime config_options for default model options when assistant models are empty', () => {
    mockManagedAgentRuntimeCatalog = [
      {
        id: 'agent-codex',
        config_options: {
          config_options: [
            {
              id: 'model',
              category: 'model',
              type: 'select',
              currentValue: 'gpt-5.5',
              options: [
                { value: 'gpt-5.5', name: 'GPT-5.5' },
                { value: 'gpt-5.2', name: 'gpt-5.2' },
              ],
            },
          ],
        },
        available_models: {
          current_model_id: 'legacy-model',
          available_models: [{ id: 'legacy-model', label: 'Legacy Model' }],
        },
      },
    ];

    renderWithProviders(
      <AssistantEditorSections
        editor={createEditor({
          agent: {
            value: 'agent-codex',
            setValue: vi.fn(),
            availableBackends: [backendOption('agent-codex', 'codex', 'Codex')],
          },
          defaults: {
            model: { mode: 'fixed', setMode: vi.fn(), value: 'gpt-5.2', setValue: vi.fn() },
          },
        })}
        activeAssistant={null}
      />
    );

    fireEvent.click(screen.getByTestId('select-assistant-default-model'));

    expect(screen.getByText('GPT-5.5')).toBeInTheDocument();
    expect(screen.getAllByText('gpt-5.2').length).toBeGreaterThan(0);
    expect(screen.queryByText('Legacy Model')).toBeNull();
  });

  it('renders thought level defaults only when the selected agent advertises thought_level options', () => {
    const setDefaultThoughtLevelMode = vi.fn();
    const setDefaultThoughtLevelValue = vi.fn();
    mockManagedAgentRuntimeCatalog = [
      {
        id: 'agent-codex',
        config_options: {
          config_options: [
            {
              id: 'reasoning_effort',
              category: 'thought_level',
              type: 'select',
              current_value: 'medium',
              options: [
                { value: 'low', name: 'Low' },
                { value: 'medium', name: 'Medium' },
                { value: 'high', name: 'High' },
              ],
            },
          ],
        },
      },
    ];

    renderWithProviders(
      <AssistantEditorSections
        editor={
          createEditor({
            agent: {
              value: 'agent-codex',
              setValue: vi.fn(),
              availableBackends: [backendOption('agent-codex', 'codex', 'Codex')],
            },
            defaults: {
              thoughtLevel: {
                mode: 'fixed',
                setMode: setDefaultThoughtLevelMode,
                value: 'high',
                setValue: setDefaultThoughtLevelValue,
              },
            } as any,
          }) as any
        }
        activeAssistant={null}
      />
    );

    expect(screen.getByText('Thought Level')).toBeInTheDocument();
    expect(screen.getByTestId('select-assistant-default-thought-level')).toHaveTextContent('High');

    fireEvent.click(screen.getByTestId('select-assistant-default-thought-level'));
    fireEvent.click(screen.getByText('Low'));

    expect(setDefaultThoughtLevelMode).toHaveBeenCalledWith('fixed');
    expect(setDefaultThoughtLevelValue).toHaveBeenCalledWith('low');
  });

  it('hides thought level defaults when the selected agent has no thought_level catalog', () => {
    renderWithProviders(
      <AssistantEditorSections
        editor={
          createEditor({
            agent: {
              value: 'agent-codex',
              setValue: vi.fn(),
              availableBackends: [backendOption('agent-codex', 'codex', 'Codex')],
            },
          }) as any
        }
        activeAssistant={null}
      />
    );

    expect(screen.queryByTestId('select-assistant-default-thought-level')).not.toBeInTheDocument();
  });

  it('renders only the auto thought level default when the thought_level catalog has no concrete option values', () => {
    mockManagedAgentRuntimeCatalog = [
      {
        id: 'agent-codex',
        config_options: {
          config_options: [
            {
              id: 'reasoning_effort',
              category: 'thought_level',
              type: 'select',
              current_value: '',
              options: [{ value: '', name: '' }],
            },
          ],
        },
      },
    ];

    renderWithProviders(
      <AssistantEditorSections
        editor={
          createEditor({
            agent: {
              value: 'agent-codex',
              setValue: vi.fn(),
              availableBackends: [backendOption('agent-codex', 'codex', 'Codex')],
            },
          }) as any
        }
        activeAssistant={null}
      />
    );

    expect(screen.getByText('Thought Level')).toBeInTheDocument();
    expect(screen.getByTestId('select-assistant-default-thought-level')).toHaveTextContent(
      'Remember last used automatically'
    );
  });

  it('uses unsupportedRuntime runtime catalog for default permission options', async () => {
    mockManagedAgentRuntimeCatalog = [
      {
        id: 'agent-unsupportedRuntime',
        available_modes: {
          current_mode_id: 'default',
          available_modes: [
            { id: 'default', name: 'Default' },
            { id: 'auto_edit', name: 'Auto Edit' },
            { id: 'yolo', name: 'YOLO' },
          ],
        },
        config_options: {
          config_options: [
            {
              id: 'mode',
              category: 'mode',
              type: 'select',
              current_value: 'default',
              options: [
                { value: 'default', name: 'Default' },
                { value: 'auto_edit', name: 'Auto Edit' },
                { value: 'yolo', name: 'YOLO' },
              ],
            },
          ],
        },
      },
    ];

    renderWithProviders(
      <AssistantEditorSections
        editor={createEditor({
          agent: {
            value: 'agent-unsupportedRuntime',
            setValue: vi.fn(),
            availableBackends: [backendOption('agent-unsupportedRuntime', 'unsupportedRuntime', 'Unsupported runtime')],
          },
        })}
        activeAssistant={null}
      />
    );

    fireEvent.click(screen.getByTestId('select-assistant-default-permission'));
    await waitFor(() => {
      expect(screen.getByText('Default')).toBeInTheDocument();
      expect(screen.getByText('Auto Edit')).toBeInTheDocument();
      expect(screen.getByText('YOLO')).toBeInTheDocument();
    });
  });

  it('renders recommended prompts as a list with actions', () => {
    renderWithProviders(
      <AssistantEditorSections
        editor={createEditor({ prompts: { text: 'Prompt one\nPrompt two', setText: vi.fn() } })}
        activeAssistant={null}
      />
    );

    const promptCard = screen.getByTestId('assistant-card-prompts');
    const promptScope = within(promptCard);
    expect(promptScope.getByText('Prompt one')).toBeInTheDocument();
    expect(promptScope.getByText('Prompt two')).toBeInTheDocument();
    expect(promptScope.getByRole('button', { name: 'Add' })).toBeInTheDocument();
  });

  it('keeps existing recommended prompts above the new prompt input while adding', () => {
    renderWithProviders(
      <AssistantEditorSections
        editor={createEditor({ prompts: { text: 'Prompt one\nPrompt two', setText: vi.fn() } })}
        activeAssistant={null}
      />
    );

    const promptCard = screen.getByTestId('assistant-card-prompts');
    fireEvent.click(within(promptCard).getByRole('button', { name: 'Add' }));

    const promptPanel = promptCard.querySelector('.bg-fill-1');
    const firstPrompt = within(promptCard).getByText('Prompt one');
    const newPromptInput = within(promptCard).getByTestId('input-assistant-recommended-prompt-new');

    expect(promptPanel).not.toBeNull();
    expect(firstPrompt.compareDocumentPosition(newPromptInput) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('does not render an empty prompts panel when there are no recommended prompts', () => {
    renderWithProviders(
      <AssistantEditorSections
        editor={createEditor({ prompts: { text: '', setText: vi.fn() } })}
        activeAssistant={null}
      />
    );

    const promptCard = screen.getByTestId('assistant-card-prompts');
    expect(promptCard.querySelector('.bg-fill-1')).toBeNull();
    expect(within(promptCard).getByRole('button', { name: 'Add' })).toBeInTheDocument();
  });

  it('lets users pick an avatar image from the file dialog', async () => {
    const setEditAvatar = vi.fn();
    const setEditAvatarPreview = vi.fn();
    showOpenInvokeMock.mockResolvedValue(['/tmp/avatar.png']);

    renderWithProviders(
      <AssistantEditorSections
        editor={createEditor({
          profile: {
            avatar: '✍️',
            setAvatar: setEditAvatar,
            setAvatarPreview: setEditAvatarPreview,
            name: 'Writer',
            setName: vi.fn(),
            description: 'desc',
            setDescription: vi.fn(),
          },
          defaults: {
            mcps: { mode: 'auto', setMode: vi.fn(), availableServers: [], selectedIds: [], setSelectedIds: vi.fn() },
          },
        })}
        activeAssistant={null}
      />
    );

    fireEvent.click(screen.getByTestId('btn-assistant-avatar-upload'));

    expect(showOpenInvokeMock).toHaveBeenCalled();
    await waitFor(() => {
      expect(setEditAvatar).toHaveBeenCalledWith('/tmp/avatar.png');
      expect(getImageBase64InvokeMock).toHaveBeenCalledWith({ path: '/tmp/avatar.png' });
      expect(setEditAvatarPreview).toHaveBeenCalledWith('data:image/png;base64,preview');
    });
  });

  it('locks builtin default model and permission while showing managed prompts as read-only content', () => {
    const { container } = renderWithProviders(
      <AssistantEditorSections
        editor={createEditor({
          isCreating: false,
          profile: {
            name: 'Cowork',
            setName: vi.fn(),
            description: 'Builtin desc',
            setDescription: vi.fn(),
            avatar: '🤝',
            setAvatar: vi.fn(),
            setAvatarPreview: vi.fn(),
          },
          prompts: { text: 'Prompt one\nPrompt two', setText: vi.fn() },
          defaults: {
            model: { mode: 'fixed', setMode: vi.fn(), value: 'gemini-2.5-pro', setValue: vi.fn() },
            permission: { mode: 'fixed', setMode: vi.fn(), value: 'default', setValue: vi.fn() },
            mcps: {
              mode: 'fixed',
              setMode: vi.fn(),
              availableServers: [{ id: 'mcp-a', name: 'Server A', enabled: true } as any],
              selectedIds: ['mcp-a'],
              setSelectedIds: vi.fn(),
            },
          },
          rules: { content: 'builtin rules', setContent: vi.fn(), viewMode: 'preview', setViewMode: vi.fn() },
          skills: {
            availableSkills: [
              { name: 'browse', description: 'Browse the web', location: '', is_custom: false, source: 'builtin' },
            ],
            selectedSkills: ['browse'],
            setSelectedSkills: vi.fn(),
            pendingSkills: [],
            setDeletePendingSkillName: vi.fn(),
            setDeleteCustomSkillName: vi.fn(),
            builtinAutoSkills: [],
            disabledBuiltinSkills: [],
            setDisabledBuiltinSkills: vi.fn(),
          },
          agent: {
            value: 'agent-claude',
            setValue: vi.fn(),
            availableBackends: [backendOption('agent-claude', 'claude', 'Claude')],
          },
        })}
        activeAssistant={{
          id: 'cowork',
          name: 'Cowork',
          sort_order: 1,
          source: 'builtin',
          enabled: true,
          agent_id: 'agent-claude',
          agent: { type: 'acp', source: 'builtin', acp_backend: 'claude' },
        }}
      />
    );

    const defaultsCard = screen.getByTestId('assistant-card-defaults');
    expect(within(defaultsCard).getByText('Model')).toBeInTheDocument();
    expect(within(defaultsCard).getByText('Permission')).toBeInTheDocument();

    const modelSelect = container.querySelector('[data-testid="select-assistant-default-model"]');
    const permissionSelect = container.querySelector('[data-testid="select-assistant-default-permission"]');
    expect(modelSelect?.className).toContain('arco-select-disabled');
    expect(permissionSelect?.className).toContain('arco-select-disabled');
    expect(screen.queryByTestId('select-assistant-default-skills')).not.toBeInTheDocument();
    expect(screen.queryByTestId('select-assistant-default-mcp')).not.toBeInTheDocument();
    expect(screen.getByText('browse')).toBeInTheDocument();
    expect(screen.getByText('Server A')).toBeInTheDocument();

    const promptCard = screen.getByTestId('assistant-card-prompts');
    const promptScope = within(promptCard);
    expect(promptScope.getByText('Prompt one')).toBeInTheDocument();
    expect(promptScope.getByText('Prompt two')).toBeInTheDocument();
    expect(promptScope.queryByRole('button', { name: 'Add' })).not.toBeInTheDocument();
  });

  it('renders Synon Biomed builtin experts with locked identity and read-only managed content', () => {
    const { container } = renderWithProviders(
      <AssistantEditorSections
        editor={createEditor({
          isCreating: false,
          profile: {
            name: 'Droid',
            setName: vi.fn(),
            description: 'Bare assistant',
            setDescription: vi.fn(),
            avatar: '🤖',
            setAvatar: vi.fn(),
            setAvatarPreview: vi.fn(),
          },
          prompts: { text: 'Prompt one\nPrompt two', setText: vi.fn() },
          defaults: {
            model: { mode: 'fixed', setMode: vi.fn(), value: 'gemini-2.5-pro', setValue: vi.fn() },
            permission: { mode: 'fixed', setMode: vi.fn(), value: 'default', setValue: vi.fn() },
            mcps: {
              mode: 'fixed',
              setMode: vi.fn(),
              availableServers: [{ id: 'mcp-a', name: 'Server A', enabled: true } as any],
              selectedIds: ['mcp-a'],
              setSelectedIds: vi.fn(),
            },
          },
          rules: { content: 'bare rules', setContent: vi.fn(), viewMode: 'preview', setViewMode: vi.fn() },
          skills: {
            availableSkills: [
              { name: 'browse', description: 'Browse the web', location: '', is_custom: false, source: 'builtin' },
            ],
            selectedSkills: ['browse'],
            setSelectedSkills: vi.fn(),
            pendingSkills: [],
            setDeletePendingSkillName: vi.fn(),
            setDeleteCustomSkillName: vi.fn(),
            builtinAutoSkills: [],
            disabledBuiltinSkills: [],
            setDisabledBuiltinSkills: vi.fn(),
          },
          agent: {
            value: 'synonbiomed:operon',
            setValue: vi.fn(),
            availableBackends: [backendOption('synonbiomed:operon', 'operon', 'synonbiomed')],
          },
        })}
        activeAssistant={{
          id: 'synonbiomed:operon',
          name: 'OPERON',
          sort_order: 1,
          source: 'builtin',
          enabled: true,
          agent_id: 'OPERON',
          agent: { type: 'synonbiomed', source: 'builtin', acp_backend: 'synonbiomed' },
        }}
      />
    );

    expect(screen.getByTestId('input-assistant-name')).toBeDisabled();
    expect(screen.getByTestId('input-assistant-desc')).toBeDisabled();

    const agentSelect = container.querySelector('[data-testid="select-assistant-agent"]');
    const modelSelect = container.querySelector('[data-testid="select-assistant-default-model"]');
    const permissionSelect = container.querySelector('[data-testid="select-assistant-default-permission"]');

    expect(agentSelect?.className).toContain('arco-select-disabled');
    expect(modelSelect?.className).toContain('arco-select-disabled');
    expect(permissionSelect?.className).toContain('arco-select-disabled');
    expect(screen.queryByTestId('select-assistant-default-skills')).not.toBeInTheDocument();
    expect(screen.queryByTestId('select-assistant-default-mcp')).not.toBeInTheDocument();
    expect(screen.getByText('browse')).toBeInTheDocument();
    expect(screen.getByText('Server A')).toBeInTheDocument();
    expect(
      within(screen.getByTestId('assistant-card-prompts')).queryByRole('button', { name: 'Add' })
    ).not.toBeInTheDocument();
    expect(
      within(screen.getByTestId('assistant-card-rules')).queryByRole('button', { name: 'Edit' })
    ).not.toBeInTheDocument();
    expect(
      within(screen.getByTestId('assistant-card-rules')).getByRole('button', { name: 'Expand' })
    ).toBeInTheDocument();
  });

  it('renders single default-skill and default-mcp controls with hub links', () => {
    renderWithProviders(
      <AssistantEditorSections
        editor={createEditor({
          defaults: {
            mcps: {
              mode: 'fixed',
              setMode: vi.fn(),
              availableServers: [{ id: 'mcp-a', name: 'Server A', enabled: true } as any],
              selectedIds: ['mcp-a'],
              setSelectedIds: vi.fn(),
            },
          },
          skills: {
            availableSkills: [
              { name: 'browse', description: 'Browse the web', location: '', is_custom: false, source: 'builtin' },
            ],
            selectedSkills: ['browse'],
            setSelectedSkills: vi.fn(),
            pendingSkills: [],
            setDeletePendingSkillName: vi.fn(),
            setDeleteCustomSkillName: vi.fn(),
            builtinAutoSkills: [],
            disabledBuiltinSkills: [],
            setDisabledBuiltinSkills: vi.fn(),
          },
        })}
        activeAssistant={null}
      />
    );

    expect(screen.queryByTestId('select-assistant-default-skills-mode')).not.toBeInTheDocument();
    expect(screen.queryByTestId('select-assistant-default-mcp-mode')).not.toBeInTheDocument();
    expect(screen.getByTestId('btn-open-skills-settings')).toBeInTheDocument();
    expect(screen.getByTestId('btn-open-mcp-settings')).toBeInTheDocument();
  });

  it('switches default skills from auto to fixed when selecting a concrete skill', async () => {
    const setDefaultSkillsMode = vi.fn();
    const setSelectedSkills = vi.fn();

    renderWithProviders(
      <AssistantEditorSections
        editor={createEditor({
          defaults: {
            skills: { mode: 'auto', setMode: setDefaultSkillsMode },
          },
          skills: {
            availableSkills: [
              { name: 'browse', description: 'Browse the web', location: '', is_custom: false, source: 'builtin' },
            ],
            selectedSkills: [],
            setSelectedSkills,
            pendingSkills: [],
            setDeletePendingSkillName: vi.fn(),
            setDeleteCustomSkillName: vi.fn(),
            builtinAutoSkills: [],
            disabledBuiltinSkills: [],
            setDisabledBuiltinSkills: vi.fn(),
          },
        })}
        activeAssistant={null}
      />
    );

    fireEvent.click(screen.getByTestId('select-assistant-default-skills'));
    fireEvent.click(await screen.findByText('browse'));

    expect(setDefaultSkillsMode).toHaveBeenCalledWith('fixed');
    expect(setSelectedSkills).toHaveBeenCalledWith(['browse']);
  });

  it('uses stronger contrast classes for applies-immediately badges', () => {
    renderWithProviders(<AssistantEditorSections editor={createEditor()} activeAssistant={null} />);

    const legend = screen.getAllByText('Applies immediately')[0];
    expect(legend.className).toContain('border');
    expect(legend.className).toContain('font-600');
    expect(legend.className).toContain('text-white');
  });

  it('does not autofocus the rules textarea when edit mode is visible', () => {
    renderWithProviders(
      <AssistantEditorSections
        editor={createEditor({
          defaults: {
            skills: { mode: 'auto', setMode: vi.fn() },
            mcps: { mode: 'auto', setMode: vi.fn(), availableServers: [], selectedIds: [], setSelectedIds: vi.fn() },
          },
          rules: { content: 'rules', setContent: vi.fn(), viewMode: 'edit', setViewMode: vi.fn() },
        })}
        activeAssistant={null}
      />
    );

    const textarea = screen.getByPlaceholderText('Enter rules in Markdown format...');
    expect(document.activeElement).not.toBe(textarea);
  });
});
