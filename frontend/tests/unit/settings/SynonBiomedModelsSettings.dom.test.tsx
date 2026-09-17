import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { SynonBiomedModelsSettingsContent } from '@/renderer/pages/settings/SynonBiomedModelsSettings';
import { renderWithI18n } from '../i18nTestUtils';

const mocks = vi.hoisted(() => ({
  load: vi.fn(),
  save: vi.fn(),
  activate: vi.fn(),
  remove: vi.fn(),
  test: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedLlm', () => ({
  loadSynonBiomedLlmProviders: mocks.load,
  saveSynonBiomedLlmProfile: mocks.save,
  activateSynonBiomedLlmProfile: mocks.activate,
  deleteSynonBiomedLlmProfile: mocks.remove,
  testSynonBiomedLlmProfile: mocks.test,
}));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: { ...actual.Message, success: mocks.success, error: mocks.error },
  };
});

const snapshot = {
  activeProfileId: 'profile-active',
  templates: [
    {
      provider: 'custom',
      label: 'Custom / OpenAI compatible',
      defaultBaseUrl: '',
      modelExamples: [],
      protocol: 'openai' as const,
    },
    {
      provider: 'deepseek',
      label: 'DeepSeek',
      defaultBaseUrl: 'https://api.deepseek.com/v1',
      modelExamples: ['deepseek-chat', 'deepseek-reasoner'],
      protocol: 'openai' as const,
    },
    {
      provider: 'ollama',
      label: 'Ollama',
      defaultBaseUrl: 'http://127.0.0.1:11434/v1',
      modelExamples: ['qwen3'],
      protocol: 'openai' as const,
    },
  ],
  profiles: [
    {
      id: 'profile-active',
      name: 'Production DeepSeek',
      provider: 'deepseek',
      baseUrl: 'https://api.deepseek.com/v1',
      model: 'deepseek-chat',
      temperature: 0.2,
      maxTokens: 2048,
      createdAt: '2026-07-10T00:00:00.000Z',
      updatedAt: '2026-07-10T00:00:00.000Z',
      hasApiKey: true,
      apiKeySource: 'stored',
    },
    {
      id: 'profile-spare',
      name: 'Reasoning',
      provider: 'deepseek',
      baseUrl: 'https://api.deepseek.com/v1',
      model: 'deepseek-reasoner',
      temperature: 0.1,
      maxTokens: 4096,
      createdAt: '2026-07-10T00:00:00.000Z',
      updatedAt: '2026-07-10T00:00:00.000Z',
      hasApiKey: true,
      apiKeySource: 'stored',
    },
  ],
};

describe('SynonBiomedModelsSettingsContent', () => {
  beforeEach(() => {
    mocks.load.mockResolvedValue(snapshot);
    mocks.save.mockResolvedValue({ ok: true });
    mocks.activate.mockResolvedValue({ ok: true });
    mocks.remove.mockResolvedValue({ ok: true });
    mocks.test.mockResolvedValue({
      text: 'Provider reachable.',
      model: 'deepseek-chat',
    });
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it('renders the real Synon LLM profiles and active credential state', async () => {
    await renderWithI18n(<SynonBiomedModelsSettingsContent />, 'zh-CN');

    expect(await screen.findByText('Production DeepSeek')).toBeInTheDocument();
    expect(screen.getByText('Reasoning')).toBeInTheDocument();
    expect(screen.getAllByText('deepseek-chat').length).toBeGreaterThan(0);
    const activeProfile = screen.getByTestId('synon-biomed-model-profile-profile-active');
    const spareProfile = screen.getByTestId('synon-biomed-model-profile-profile-spare');
    expect(within(activeProfile).getByText('密钥已保存')).toBeInTheDocument();
    expect(within(spareProfile).getByText('密钥已保存')).toBeInTheDocument();
    expect(activeProfile).toHaveTextContent('当前使用');
    expect(activeProfile).toHaveTextContent('回答风格：稳定');
    expect(activeProfile).not.toHaveTextContent('2048');
    expect(activeProfile).not.toHaveTextContent('0.2');
    expect(activeProfile).not.toHaveTextContent('回答长度');
    expect(mocks.load).toHaveBeenCalledTimes(1);

    const summary = document.querySelector('.settings-summary-strip');
    expect(summary).not.toBeNull();
    expect(Array.from(summary!.children).map((element) => element.tagName)).toEqual(['DIV', 'DIV', 'BUTTON']);
    const addModelAction = screen.getByTestId('synon-biomed-model-add');
    expect(addModelAction).toHaveAttribute('aria-label', '新增模型配置');
    expect(addModelAction.parentElement).toBe(summary);
    expect(within(summary!).getByRole('button', { name: '新增模型配置' })).toBe(addModelAction);
    expect(within(screen.getByTestId('models-header')).queryByRole('button', { name: '新增模型配置' })).toBeNull();
    expect(within(summary!).queryByText('密钥状态')).toBeNull();

    const profileMain = activeProfile.querySelector('.settings-model-profile__main');
    expect(profileMain).not.toBeNull();
    expect(Array.from(profileMain!.children).map((element) => element.className)).toEqual([
      'settings-model-profile__identity',
      'settings-model-profile__detail settings-model-profile__model-id',
      'settings-model-profile__detail settings-model-profile__base-url',
      'settings-model-profile__key',
      'settings-model-profile__actions',
    ]);
  });

  it('tests and activates a profile through the Synon LLM Core service', async () => {
    await renderWithI18n(<SynonBiomedModelsSettingsContent />, 'zh-CN');
    await screen.findByText('Production DeepSeek');

    fireEvent.click(screen.getByRole('button', { name: '测试 Production DeepSeek' }));
    await waitFor(() => expect(mocks.test).toHaveBeenCalledWith('profile-active'));
    expect(mocks.success).toHaveBeenCalledWith(expect.stringContaining('deepseek-chat'));

    fireEvent.click(screen.getByRole('button', { name: '将 Reasoning 设为当前模型' }));
    await waitFor(() => expect(mocks.activate).toHaveBeenCalledWith(snapshot.profiles[1]));
    expect(mocks.load).toHaveBeenCalledTimes(2);
  });

  it('reports the configured model when a provider returns a deployment alias', async () => {
    mocks.test.mockResolvedValueOnce({
      text: 'Provider reachable.',
      model: 'deepseek-v4-flash',
    });
    await renderWithI18n(<SynonBiomedModelsSettingsContent />, 'zh-CN');
    await screen.findByText('Production DeepSeek');

    const activeProfile = screen.getByTestId('synon-biomed-model-profile-profile-active');
    const testButton = Array.from(activeProfile.querySelectorAll<HTMLButtonElement>('button')).find((button) =>
      button.className.includes('arco-btn-secondary')
    );
    expect(testButton).not.toBeNull();
    fireEvent.click(testButton!);
    await waitFor(() => expect(mocks.test).toHaveBeenCalledWith('profile-active'));
    expect(mocks.success).toHaveBeenCalledWith(expect.stringContaining('deepseek-chat'));
    expect(mocks.success).not.toHaveBeenCalledWith(expect.stringContaining('deepseek-v4-flash'));
  });

  it('opens an SynonAI-native profile editor without rendering the legacy injected settings UI', async () => {
    await renderWithI18n(<SynonBiomedModelsSettingsContent />, 'zh-CN');
    await screen.findByText('Production DeepSeek');

    fireEvent.click(screen.getByRole('button', { name: '新增模型配置' }));

    expect(screen.getByRole('dialog', { name: '新增模型配置' })).toBeInTheDocument();
    expect(screen.getByLabelText('配置名称')).toHaveValue('');
    expect(screen.getByLabelText('提供方')).toHaveTextContent('DeepSeek');
    expect(screen.getByLabelText('模型 ID')).toHaveTextContent('deepseek-chat');
    expect(screen.getByLabelText('回答灵活度')).toHaveTextContent('稳定');
    expect(screen.getByText(/选择回答风格/)).toBeInTheDocument();
    expect(screen.queryByLabelText('单次回答长度')).toBeNull();
    expect(screen.queryByText('高级调用设置')).toBeNull();
    expect(screen.queryByLabelText('单次调用输出上限（tokens）')).toBeNull();
    expect(screen.getByLabelText('API Key')).toHaveAttribute('type', 'password');
    expect(screen.getByLabelText('API Key')).toHaveAttribute('autocomplete', 'new-password');
    expect(document.querySelector('script[src*="synon-llm-settings"]')).toBeNull();

    const providerSelect = screen.getByLabelText('提供方');
    const providerTrigger = providerSelect.querySelector<HTMLElement>('.arco-select-view');
    expect(providerTrigger).not.toBeNull();
    fireEvent.mouseDown(providerTrigger!);
    fireEvent.click(providerTrigger!);
    expect(
      await screen.findByRole('option', {
        name: '自定义 / OpenAI 兼容',
      })
    ).toBeInTheDocument();
    const providerListbox = screen.getByRole('listbox');
    expect(providerListbox.closest('.arco-modal-content')).toBeNull();
    expect(providerListbox.closest('.arco-trigger')?.parentElement?.parentElement).toBe(document.body);
    fireEvent.click(screen.getByRole('option', { name: 'Ollama（本地）' }));

    expect(screen.getByLabelText('提供方')).toHaveTextContent('Ollama（本地）');
    expect(screen.getByLabelText('Base URL')).toHaveValue('http://127.0.0.1:11434/v1');
    expect(screen.getByLabelText('模型 ID')).toHaveTextContent('qwen3');

    fireEvent.click(screen.getByTestId('synon-biomed-model-id-select'));
    expect(await screen.findByRole('option', { name: 'qwen3' })).toBeInTheDocument();
    expect(screen.queryByRole('option', { name: 'deepseek-reasoner' })).not.toBeInTheDocument();
  });

  it('changing answer style does not set an intermediate output budget', async () => {
    await renderWithI18n(<SynonBiomedModelsSettingsContent />, 'zh-CN');
    await screen.findByText('Production DeepSeek');

    fireEvent.click(screen.getByRole('button', { name: '新增模型配置' }));
    expect(await screen.findByRole('dialog', { name: '新增模型配置' })).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('配置名称'), { target: { value: 'Semantic options' } });

    const temperatureSelect = screen.getByTestId('synon-biomed-temperature-select');
    const temperatureTrigger = temperatureSelect.querySelector<HTMLElement>('.arco-select-view');
    expect(temperatureTrigger).not.toBeNull();
    fireEvent.mouseDown(temperatureTrigger!);
    fireEvent.click(temperatureTrigger!);
    fireEvent.click(await screen.findByRole('option', { name: '灵活' }));

    fireEvent.click(screen.getByRole('button', { name: '保存' }));
    await waitFor(() => expect(mocks.save).toHaveBeenCalledTimes(1));
    expect(mocks.save).toHaveBeenCalledWith(
      expect.objectContaining({
        temperature: 0.8,
      })
    );
    expect(mocks.save.mock.calls[0][0]).not.toHaveProperty('maxTokens');
  });

  it('does not silently persist a token limit when creating a model', async () => {
    await renderWithI18n(<SynonBiomedModelsSettingsContent />, 'zh-CN');
    await screen.findByText('Production DeepSeek');
    fireEvent.click(screen.getByRole('button', { name: '新增模型配置' }));
    fireEvent.change(screen.getByLabelText('配置名称'), { target: { value: 'Provider default' } });
    fireEvent.click(screen.getByRole('button', { name: '保存' }));
    await waitFor(() => expect(mocks.save).toHaveBeenCalledTimes(1));
    expect(mocks.save.mock.calls[0][0]).not.toHaveProperty('maxTokens');
  });

  it('editing a legacy model clears its stored cap for adaptive provider budgeting', async () => {
    await renderWithI18n(<SynonBiomedModelsSettingsContent />, 'zh-CN');
    await screen.findByText('Production DeepSeek');
    fireEvent.click(screen.getByRole('button', { name: '编辑 Production DeepSeek' }));
    expect(screen.queryByText('高级调用设置')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: '保存' }));
    await waitFor(() => expect(mocks.save).toHaveBeenCalledTimes(1));
    expect(mocks.save.mock.calls[0][0]).toMatchObject({
      id: snapshot.profiles[0].id,
      model: 'deepseek-chat',
      temperature: 0.2,
      maxTokens: null,
    });
  });
});
