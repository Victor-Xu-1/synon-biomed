import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';
import { BackendHttpError } from '@/common/adapter/httpBridge';

const mocks = vi.hoisted(() => ({
  navigate: vi.fn(),
  loadSnapshot: vi.fn(),
  saveCapabilities: vi.fn(),
  ensureProject: vi.fn(),
  prepareSuggestions: vi.fn(),
  requestSuggestions: vi.fn(),
  resolveSuggestion: vi.fn(),
  cancelSuggestion: vi.fn(),
  stageTask: vi.fn(),
  loadLlmProviders: vi.fn(),
  saveLlmProfile: vi.fn(),
  activateLlmProfile: vi.fn(),
  testLlmProfile: vi.fn(),
  confirmCompletion: vi.fn(),
  refresh: vi.fn(),
  clearAuthCache: vi.fn(),
  rememberCurrentAuthRoute: vi.fn(),
  authUser: { id: 'local', username: 'local' } as { id: string; username: string },
}));

vi.mock('react-router', () => ({ useNavigate: () => mocks.navigate }));
vi.mock('@/renderer/hooks/context/AuthContext', () => ({
  useAuth: () => ({
    user: mocks.authUser,
    status: 'authenticated',
    ready: true,
    refresh: mocks.refresh,
    clearAuthCache: mocks.clearAuthCache,
  }),
}));
vi.mock('@/renderer/services/authSession', () => ({ rememberCurrentAuthRoute: mocks.rememberCurrentAuthRoute }));
vi.mock('@/renderer/services/onboardingCompletionAuthority', () => ({
  confirmOnboardingCompletion: mocks.confirmCompletion,
}));
vi.mock('@icon-park/react', () => {
  const Icon = (props: Record<string, unknown>) => <span {...props} />;
  return {
    ArrowLeft: Icon,
    ArrowRight: Icon,
    Brain: Icon,
    Check: Icon,
    Config: Icon,
    Link: Icon,
    NetworkTree: Icon,
    Plus: Icon,
    Tool: Icon,
    UploadOne: Icon,
  };
});
vi.mock('@arco-design/web-react', () => {
  const Button = ({
    icon: _icon,
    loading: _loading,
    children,
    ...props
  }: React.ButtonHTMLAttributes<HTMLButtonElement> & { icon?: unknown; loading?: boolean }) => (
    <button {...props}>{children}</button>
  );
  const Switch = ({
    checked,
    onChange,
    ...props
  }: Omit<React.InputHTMLAttributes<HTMLInputElement>, 'onChange'> & { onChange?: (checked: boolean) => void }) => (
    <input type='checkbox' checked={checked} onChange={(event) => onChange?.(event.target.checked)} {...props} />
  );
  const Input = ({
    onChange,
    ...props
  }: Omit<React.InputHTMLAttributes<HTMLInputElement>, 'onChange'> & { onChange?: (value: string) => void }) => (
    <input {...props} onChange={(event) => onChange?.(event.target.value)} />
  );
  Input.TextArea = ({
    onChange,
    ...props
  }: Omit<React.TextareaHTMLAttributes<HTMLTextAreaElement>, 'onChange'> & { onChange?: (value: string) => void }) => (
    <textarea {...props} onChange={(event) => onChange?.(event.target.value)} />
  );
  Input.Password = Input;
  const Select = ({
    value,
    onChange,
    children,
    ...props
  }: Omit<React.SelectHTMLAttributes<HTMLSelectElement>, 'onChange'> & { onChange?: (value: string) => void }) => (
    <select value={value} onChange={(event) => onChange?.(event.target.value)} {...props}>
      {children}
    </select>
  );
  Select.Option = ({ value, children }: { value: string; children?: React.ReactNode }) => (
    <option value={value}>{children}</option>
  );
  return {
    Alert: ({ content }: { content?: React.ReactNode }) => <div>{content}</div>,
    Button,
    Input,
    Message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
    Select,
    Spin: () => <div>loading</div>,
    Switch,
  };
});
vi.mock('@/renderer/services/synonBiomedLlm', () => ({
  loadSynonBiomedLlmProviders: mocks.loadLlmProviders,
  saveSynonBiomedLlmProfile: mocks.saveLlmProfile,
  activateSynonBiomedLlmProfile: mocks.activateLlmProfile,
  testSynonBiomedLlmProfile: mocks.testLlmProfile,
}));
vi.mock('@/renderer/services/onboardingService', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/renderer/services/onboardingService')>()),
  loadOnboardingSnapshot: mocks.loadSnapshot,
  saveOnboardingCapabilities: mocks.saveCapabilities,
  ensureOnboardingProject: mocks.ensureProject,
  prepareOnboardingSuggestionArtifacts: mocks.prepareSuggestions,
  stageOnboardingTask: mocks.stageTask,
}));
vi.mock('@/renderer/services/onboardingSuggestions', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/renderer/services/onboardingSuggestions')>()),
  requestOnboardingTaskSuggestions: mocks.requestSuggestions,
  resolveOnboardingTaskSuggestion: mocks.resolveSuggestion,
  cancelOnboardingSuggestionFrame: mocks.cancelSuggestion,
}));

import OnboardingFlow from '@/renderer/pages/onboarding/OnboardingFlow';

function onboardingSnapshot() {
  return {
    complete: false,
    assistantId: 'synonbiomed:operon',
    assistantName: 'OPERON',
    allowedConnectorIds: ['pubmed'],
    allowedSkillNames: ['literature'],
    networkGroups: [
      { id: 'required', label: 'Required network', description: 'Core services', locked: true, domains: [] },
      { id: 'literature', label: 'Literature', description: 'Publication services', locked: false, domains: [] },
    ],
    disabledNetworkGroupIds: [],
    connectors: [{ id: 'pubmed', displayName: 'PubMed', name: 'pubmed', description: 'Search', enabled: true }],
    skills: [{ name: 'literature', displayName: 'Literature review', description: 'Review evidence', enabled: true }],
    scientificRuntimes: [
      {
        id: 'common-structure-toolkit',
        estimatedInstallBytes: 32 * 1024 * 1024,
        estimatedInstallMB: 32,
        defaultEnabled: true,
        selected: true,
        available: true,
        status: 'waiting_for_selection',
      },
      {
        id: 'autodock-vina',
        estimatedInstallBytes: 900 * 1024 * 1024,
        estimatedInstallMB: 900,
        defaultEnabled: true,
        selected: true,
        available: false,
        status: 'waiting_for_selection',
      },
    ],
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((settle, fail) => {
    resolve = settle;
    reject = fail;
  });
  return { promise, reject, resolve };
}

async function renderOnboarding(locale = 'en-US') {
  let view!: Awaited<ReturnType<typeof renderWithI18n>>;
  await act(async () => {
    view = await renderWithI18n(<OnboardingFlow />, locale);
    await Promise.resolve();
  });
  return view;
}

describe('OnboardingFlow', () => {
  afterEach(() => vi.useRealTimers());

  beforeEach(() => {
    vi.clearAllMocks();
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => {
      callback(0);
      return 1;
    });
    vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => undefined);
    window.localStorage.clear();
    mocks.authUser = { id: 'local', username: 'local' };
    mocks.loadSnapshot.mockResolvedValue(onboardingSnapshot());
    mocks.saveCapabilities.mockResolvedValue(undefined);
    mocks.ensureProject.mockResolvedValue({ projectId: 'project-1', name: 'Getting started' });
    mocks.prepareSuggestions.mockResolvedValue([]);
    mocks.requestSuggestions.mockImplementation(() => new Promise(() => undefined));
    mocks.resolveSuggestion.mockResolvedValue(undefined);
    mocks.cancelSuggestion.mockResolvedValue(undefined);
    mocks.stageTask.mockResolvedValue({ conversationId: 'conversation-1', projectId: 'project-1' });
    mocks.loadLlmProviders.mockResolvedValue({ profiles: [], templates: [] });
    mocks.refresh.mockResolvedValue(undefined);
  });

  it('latches the exact six-second fallback and never installs a late model result', async () => {
    const late = deferred<{
      frameId: string;
      question: string;
      request: Record<string, unknown>;
      suggestions: Array<{ label: string; description: string }>;
    }>();
    mocks.requestSuggestions.mockReturnValueOnce(late.promise);
    const user = userEvent.setup();
    const view = await renderOnboarding();
    expect(await screen.findByTestId('onboarding-welcome')).toBeInTheDocument();
    for (let index = 0; index < 3; index += 1) {
      await user.click(screen.getByRole('button', { name: 'Continue' }));
    }
    vi.useFakeTimers();
    const summary = screen.getByTestId('onboarding-profile-summary');
    await act(async () => {
      fireEvent.change(summary, { target: { value: 'x'.repeat(40) } });
      await vi.advanceTimersByTimeAsync(800);
    });
    expect(mocks.requestSuggestions).toHaveBeenCalledTimes(1);
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
      await vi.advanceTimersByTimeAsync(6_000);
    });
    expect(screen.getByTestId('onboarding-suggestions-fallback')).toBeInTheDocument();
    expect(mocks.cancelSuggestion).toHaveBeenCalledTimes(1);
    await act(async () => {
      late.resolve({
        frameId: 'late-frame',
        question: 'Late?',
        request: {},
        suggestions: [{ label: 'LATE MODEL OPTION', description: 'must never render' }],
      });
      await Promise.resolve();
    });
    expect(screen.queryByText('LATE MODEL OPTION')).not.toBeInTheDocument();
    expect(screen.getByText('Review a research topic')).toBeInTheDocument();
    view.unmount();
    await act(async () => Promise.resolve());
    expect(mocks.cancelSuggestion).toHaveBeenCalled();
  });

  it('renders fallback task suggestions in the active locale', async () => {
    const user = userEvent.setup();
    await renderOnboarding('zh-CN');
    expect(await screen.findByTestId('onboarding-welcome')).toBeInTheDocument();
    for (let index = 0; index < 4; index += 1) {
      await user.click(screen.getByRole('button', { name: '继续' }));
    }
    expect(screen.getByText('调研一个科研主题')).toBeInTheDocument();
    expect(screen.getByText('分析一份数据集')).toBeInTheDocument();
    expect(screen.getByText('设计实验方案')).toBeInTheDocument();
    expect(screen.queryByText('Map the recent literature of your subfield')).not.toBeInTheDocument();
  });

  it('starts an explicit short-profile request before the task view chooses its initial state', async () => {
    const user = userEvent.setup();
    const view = await renderOnboarding();
    expect(await screen.findByTestId('onboarding-welcome')).toBeInTheDocument();
    for (let index = 0; index < 3; index += 1) await user.click(screen.getByRole('button', { name: 'Continue' }));
    vi.useFakeTimers();
    await act(async () => {
      fireEvent.change(screen.getByTestId('onboarding-profile-summary'), { target: { value: 'short profile' } });
      fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByTestId('onboarding-suggestions-loading')).toBeInTheDocument();
    expect(mocks.requestSuggestions).toHaveBeenCalledTimes(1);
    expect(mocks.requestSuggestions.mock.calls[0][0]).toMatchObject({ description: 'short profile' });
    view.unmount();
  });

  it('retries a failed automatic suggestion request when the user continues', async () => {
    const automatic = deferred<never>();
    mocks.requestSuggestions.mockReturnValueOnce(automatic.promise);
    const user = userEvent.setup();
    const view = await renderOnboarding();
    expect(await screen.findByTestId('onboarding-welcome')).toBeInTheDocument();
    for (let index = 0; index < 3; index += 1) await user.click(screen.getByRole('button', { name: 'Continue' }));
    vi.useFakeTimers();
    await act(async () => {
      fireEvent.change(screen.getByTestId('onboarding-profile-summary'), { target: { value: 'x'.repeat(40) } });
      await vi.advanceTimersByTimeAsync(800);
    });
    expect(mocks.requestSuggestions).toHaveBeenCalledTimes(1);
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(mocks.requestSuggestions).toHaveBeenCalledTimes(1);
    await act(async () => {
      automatic.reject(new Error('model unavailable'));
      await Promise.resolve();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(mocks.requestSuggestions).toHaveBeenCalledTimes(2);
    expect(screen.getByTestId('onboarding-suggestions-loading')).toBeInTheDocument();
    view.unmount();
  });

  it('redispatches on continue when a same-metadata filename is replaced by a different File object', async () => {
    const user = userEvent.setup();
    const view = await renderOnboarding();
    expect(await screen.findByTestId('onboarding-welcome')).toBeInTheDocument();
    for (let index = 0; index < 3; index += 1) await user.click(screen.getByRole('button', { name: 'Continue' }));
    const input = view.container.querySelector("input[type='file']") as HTMLInputElement;
    const first = new File(['AAAA'], 'notes.txt', { type: 'text/plain', lastModified: 7 });
    const replacement = new File(['BBBB'], 'notes.txt', { type: 'text/plain', lastModified: 7 });
    vi.useFakeTimers();
    await act(async () => {
      fireEvent.change(screen.getByTestId('onboarding-profile-summary'), { target: { value: 'x'.repeat(40) } });
      fireEvent.change(input, { target: { files: [first] } });
      await vi.advanceTimersByTimeAsync(800);
    });
    expect(mocks.requestSuggestions).toHaveBeenCalledTimes(1);
    await act(async () => {
      fireEvent.change(input, { target: { files: [replacement] } });
      fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(mocks.requestSuggestions).toHaveBeenCalledTimes(2);
    expect(mocks.prepareSuggestions.mock.calls.at(-1)?.[1]).toEqual([replacement]);
    view.unmount();
  });

  it('stages the exact custom task as an unsent draft across all six steps', async () => {
    const user = userEvent.setup();
    const { container } = await renderOnboarding();
    expect(await screen.findByTestId('onboarding-welcome')).toBeInTheDocument();

    const continueButton = () => screen.getByRole('button', { name: 'Continue' });
    await user.click(continueButton());
    expect(screen.getByTestId('onboarding-network')).toBeInTheDocument();
    await user.click(continueButton());
    expect(screen.getByTestId('onboarding-capabilities')).toBeInTheDocument();
    await user.click(continueButton());

    await user.type(screen.getByTestId('onboarding-profile-summary'), 'Translational genomics researcher');
    const file = new File(['cohort'], 'cohort.csv', { type: 'text/csv' });
    await user.upload(container.querySelector("input[type='file']") as HTMLInputElement, file);
    expect(screen.getByText('cohort.csv')).toBeInTheDocument();
    expect(screen.getByText(/Personalized suggestions read up to 20 UTF-8 text files/)).toBeInTheDocument();
    await user.click(continueButton());

    const customTask = 'Reproduce the survival analysis for this cohort';
    await user.type(screen.getByTestId('onboarding-task-custom'), customTask);
    expect(mocks.stageTask).not.toHaveBeenCalled();
    await user.click(continueButton());

    expect(screen.getByTestId('onboarding-model')).toBeInTheDocument();
    expect(await screen.findByTestId('onboarding-model-status')).toBeInTheDocument();
    await user.click(screen.getByTestId('onboarding-finish'));

    await waitFor(() => expect(mocks.stageTask).toHaveBeenCalledTimes(1));
    expect(mocks.ensureProject).toHaveBeenCalledWith('Getting started', { fetchImpl: expect.any(Function) });
    expect(mocks.stageTask).toHaveBeenCalledWith(
      expect.objectContaining({
        projectId: 'project-1',
        projectName: 'Getting started',
        assistantId: 'synonbiomed:operon',
        task: customTask,
        profile: { summary: 'Translational genomics researcher' },
        files: [file],
      }),
      expect.objectContaining({
        assertAuthority: expect.any(Function),
        fetchImpl: expect.any(Function),
        onArtifactUploaded: expect.any(Function),
        uploadedArtifacts: expect.any(Map),
      })
    );
    expect(mocks.navigate).toHaveBeenCalledWith('/conversation/conversation-1', { replace: true });
    expect(mocks.confirmCompletion).toHaveBeenCalledWith('local');
  });

  it('enters the workspace directly when no first task was selected', async () => {
    const user = userEvent.setup();
    mocks.stageTask.mockResolvedValueOnce({ projectId: 'project-1', conversationId: null });
    await renderOnboarding();
    expect(await screen.findByTestId('onboarding-welcome')).toBeInTheDocument();
    for (let index = 0; index < 5; index += 1) {
      await user.click(screen.getByRole('button', { name: 'Continue' }));
    }
    expect(screen.getByTestId('onboarding-model')).toBeInTheDocument();
    await user.click(screen.getByTestId('onboarding-finish'));

    await waitFor(() => expect(mocks.stageTask).toHaveBeenCalledTimes(1));
    expect(mocks.stageTask).toHaveBeenCalledWith(expect.objectContaining({ task: '' }), expect.anything());
    expect(mocks.navigate).toHaveBeenCalledWith('/guid', { replace: true });
    expect(mocks.confirmCompletion).toHaveBeenCalledWith('local');
  });

  it('shows the optional model setup step with the add affordance', async () => {
    const user = userEvent.setup();
    await renderOnboarding();
    expect(await screen.findByTestId('onboarding-welcome')).toBeInTheDocument();
    for (let index = 0; index < 5; index += 1) {
      await user.click(screen.getByRole('button', { name: 'Continue' }));
    }
    expect(await screen.findByTestId('onboarding-model-status')).toHaveTextContent('No model configured yet');
    expect(screen.getByTestId('onboarding-model-add')).toBeInTheDocument();
    expect(mocks.loadLlmProviders).toHaveBeenCalledTimes(1);
  });

  it('reuses the staged conversation after a lost completion response', async () => {
    const user = userEvent.setup();
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    mocks.stageTask
      .mockImplementationOnce(async (_input: unknown, options: { onConversationCreated?: (id: string) => void }) => {
        options.onConversationCreated?.('conversation-retry');
        throw new Error('connection closed after conversation creation');
      })
      .mockResolvedValueOnce({ conversationId: 'conversation-retry', projectId: 'project-1' });
    await renderOnboarding();
    expect(await screen.findByTestId('onboarding-welcome')).toBeInTheDocument();
    for (let index = 0; index < 4; index += 1) {
      await user.click(screen.getByRole('button', { name: 'Continue' }));
    }
    await user.type(screen.getByTestId('onboarding-task-custom'), 'Retry the exact first task');
    await user.click(screen.getByRole('button', { name: 'Continue' }));
    await user.click(screen.getByTestId('onboarding-finish'));
    await waitFor(() => expect(mocks.stageTask).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.getByTestId('onboarding-finish')).not.toBeDisabled());
    await user.click(screen.getByTestId('onboarding-finish'));
    await waitFor(() => expect(mocks.stageTask).toHaveBeenCalledTimes(2));

    const secondOptions = mocks.stageTask.mock.calls[1][1] as { conversationId?: string };
    expect(secondOptions.conversationId).toBe('conversation-retry');
    expect(mocks.navigate).toHaveBeenCalledWith('/conversation/conversation-retry', { replace: true });
    consoleError.mockRestore();
  });

  it('shows a localized recoverable load error without exposing backend details', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    mocks.loadSnapshot.mockRejectedValueOnce(new Error('backend unavailable: credential path'));

    await renderOnboarding();

    const errorView = await screen.findByTestId('onboarding-load-error');
    expect(errorView).toHaveTextContent('Setup data is temporarily unavailable. Retry to continue.');
    expect(errorView).not.toHaveTextContent('credential path');
    expect(consoleError).toHaveBeenCalled();
    consoleError.mockRestore();
  });

  it('unmounts the inactive capability panel and uses roving arrow-key tabs', async () => {
    const user = userEvent.setup();
    await renderOnboarding();
    expect(await screen.findByTestId('onboarding-welcome')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Continue' }));
    await user.click(screen.getByRole('button', { name: 'Continue' }));

    const connectors = screen.getByRole('tab', { name: 'Connectors' });
    const skills = screen.getByRole('tab', { name: 'Skills' });
    const runtimes = screen.getByRole('tab', { name: 'Local software' });
    expect(connectors).toHaveAttribute('tabindex', '0');
    expect(skills).toHaveAttribute('tabindex', '-1');
    expect(screen.getByText('PubMed')).toBeInTheDocument();
    expect(screen.queryByText('Literature review')).not.toBeInTheDocument();

    connectors.focus();
    await user.keyboard('{ArrowRight}');
    expect(skills).toHaveAttribute('aria-selected', 'true');
    expect(skills).toHaveAttribute('tabindex', '0');
    expect(screen.getByText('Literature review')).toBeInTheDocument();
    expect(screen.queryByText('PubMed')).not.toBeInTheDocument();

    skills.focus();
    await user.keyboard('{ArrowRight}');
    expect(runtimes).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByText('Common structure toolkit')).toBeInTheDocument();
    const vina = screen.getByRole('checkbox', { name: 'AutoDock Vina docking' });
    expect(vina).toBeChecked();
    expect(vina).toBeEnabled();
    expect(screen.getByText('Local scientific runtimes are unavailable on this system')).toBeInTheDocument();
    await user.click(vina);
    expect(vina).not.toBeChecked();
  });

  it('restores the owner-scoped step, text, and attachment reselect notice after reload', async () => {
    const user = userEvent.setup();
    window.localStorage.setItem(
      'synonbiomed.onboarding.draft.v1:local',
      JSON.stringify({
        version: 1,
        step: 4,
        networkEnabled: { required: true, literature: true },
        connectorEnabled: { pubmed: true },
        skillEnabled: { literature: true },
        profileSummary: 'Saved profile',
        attachmentNames: ['cohort.csv'],
        selectedTask: '',
        customTask: 'Saved first task',
      })
    );

    await renderOnboarding();
    expect(await screen.findByTestId('onboarding-task')).toBeInTheDocument();
    expect(screen.getByTestId('onboarding-task-custom')).toHaveValue('Saved first task');

    await user.click(screen.getByRole('button', { name: 'Back' }));
    await waitFor(() => expect(screen.getByText(/Select these files again/)).toHaveTextContent('cohort.csv'));
  });

  it('discards an older owner load that resolves after the current owner', async () => {
    const ownerA = deferred<ReturnType<typeof onboardingSnapshot>>();
    const ownerB = deferred<ReturnType<typeof onboardingSnapshot>>();
    mocks.authUser = { id: 'owner-a', username: 'owner-a' };
    mocks.loadSnapshot.mockReset();
    mocks.loadSnapshot.mockReturnValueOnce(ownerA.promise).mockReturnValueOnce(ownerB.promise);
    window.localStorage.setItem(
      'synonbiomed.onboarding.draft.v1:owner-b',
      JSON.stringify({
        version: 1,
        step: 4,
        networkEnabled: {},
        connectorEnabled: {},
        skillEnabled: {},
        profileSummary: 'Owner B profile',
        attachmentNames: [],
        selectedTask: '',
        customTask: 'Owner B task',
      })
    );
    const view = await renderOnboarding();
    mocks.authUser = { id: 'owner-b', username: 'owner-b' };
    view.rerender(<OnboardingFlow />);
    await act(async () => ownerB.resolve(onboardingSnapshot()));
    expect(await screen.findByTestId('onboarding-task-custom')).toHaveValue('Owner B task');
    await act(async () => ownerA.resolve({ ...onboardingSnapshot(), assistantName: 'STALE OWNER A' }));
    expect(screen.getByTestId('onboarding-task-custom')).toHaveValue('Owner B task');
    expect(window.localStorage.getItem('synonbiomed.onboarding.draft.v1:owner-a')).toBeNull();
  });

  it('clears selected File objects before a different owner draft becomes active', async () => {
    const user = userEvent.setup();
    mocks.authUser = { id: 'owner-a', username: 'owner-a' };
    const view = await renderOnboarding();
    expect(await screen.findByTestId('onboarding-welcome')).toBeInTheDocument();
    for (let index = 0; index < 3; index += 1) {
      await user.click(screen.getByRole('button', { name: 'Continue' }));
    }
    const fileInput = view.container.querySelector("input[type='file']") as HTMLInputElement;
    await user.upload(fileInput, new File(['owner-a'], 'owner-a.csv', { type: 'text/csv' }));
    expect(screen.getByText('owner-a.csv')).toBeInTheDocument();

    mocks.authUser = { id: 'owner-b', username: 'owner-b' };
    view.rerender(<OnboardingFlow />);
    expect(await screen.findByTestId('onboarding-welcome')).toBeInTheDocument();
    expect(screen.queryByText('owner-a.csv')).not.toBeInTheDocument();
    expect(window.localStorage.getItem('synonbiomed.onboarding.draft.v1:owner-b') ?? '').not.toContain('owner-a.csv');
  });

  it('invalidates an in-flight completion before a different owner can receive later side effects', async () => {
    const user = userEvent.setup();
    const uploadGate = deferred<void>();
    mocks.authUser = { id: 'owner-a', username: 'owner-a' };
    mocks.stageTask.mockImplementationOnce(async (_input: unknown, options: { assertAuthority: () => void }) => {
      await uploadGate.promise;
      options.assertAuthority();
      return { conversationId: 'should-not-exist', projectId: 'project-1' };
    });
    const view = await renderOnboarding();
    expect(await screen.findByTestId('onboarding-welcome')).toBeInTheDocument();
    for (let index = 0; index < 4; index += 1) {
      await user.click(screen.getByRole('button', { name: 'Continue' }));
    }
    await user.type(screen.getByTestId('onboarding-task-custom'), 'Owner A private task');
    await user.click(screen.getByRole('button', { name: 'Continue' }));
    await user.click(screen.getByTestId('onboarding-finish'));
    await waitFor(() => expect(mocks.stageTask).toHaveBeenCalledTimes(1));

    mocks.authUser = { id: 'owner-b', username: 'owner-b' };
    view.rerender(<OnboardingFlow />);
    await act(async () => uploadGate.resolve());

    expect(mocks.navigate).not.toHaveBeenCalledWith('/conversation/should-not-exist', expect.anything());
    expect(await screen.findByTestId('onboarding-welcome')).toBeInTheDocument();
  });

  it('refreshes a CSRF-invalid session without replaying the non-atomic completion', async () => {
    const user = userEvent.setup();
    mocks.saveCapabilities.mockRejectedValueOnce(
      new BackendHttpError({
        method: 'PUT',
        path: '/api/preferences/builtin-allowlist/disabled-groups',
        status: 403,
        body: { code: 'CSRF_INVALID', message: 'wording may change' },
      })
    );
    await renderOnboarding();
    expect(await screen.findByTestId('onboarding-welcome')).toBeInTheDocument();
    for (let index = 0; index < 4; index += 1) {
      await user.click(screen.getByRole('button', { name: 'Continue' }));
    }
    await user.type(screen.getByTestId('onboarding-task-custom'), 'Recover the security session');
    await user.click(screen.getByRole('button', { name: 'Continue' }));
    await user.click(screen.getByTestId('onboarding-finish'));
    await waitFor(() => expect(mocks.refresh).toHaveBeenCalledTimes(1));
    expect(mocks.rememberCurrentAuthRoute).toHaveBeenCalledTimes(1);
    expect(mocks.clearAuthCache).toHaveBeenCalledTimes(1);
    expect(mocks.stageTask).not.toHaveBeenCalled();
    expect(mocks.navigate).not.toHaveBeenCalledWith('/login', expect.anything());
  });
});
