import {
  classifyOnboardingLaunchFailure,
  createOnboardingProfileFile,
  ensureOnboardingProject,
  stageOnboardingTask,
  loadOnboardingSnapshot,
  prepareOnboardingSuggestionArtifacts,
  saveOnboardingCapabilities,
  type OnboardingArtifactCacheEntry,
  type OnboardingPendingUpload,
  type OnboardingScientificRuntimeOption,
  type OnboardingSnapshot,
} from '@/renderer/services/onboardingService';
import {
  cancelOnboardingSuggestionFrame,
  ONBOARDING_SUGGESTION_DEBOUNCE_MS,
  ONBOARDING_SUGGESTION_FALLBACK_MS,
  ONBOARDING_SUGGESTION_MIN_CHARACTERS,
  requestOnboardingTaskSuggestions,
  resolveOnboardingTaskSuggestion,
  type OnboardingSuggestionSession,
  type OnboardingTaskSuggestion,
} from '@/renderer/services/onboardingSuggestions';
import { clearOnboardingDraft, loadOnboardingDraft, saveOnboardingDraft } from '@/renderer/services/onboardingDraft';
import { confirmOnboardingCompletion } from '@/renderer/services/onboardingCompletionAuthority';
import { useAuth } from '@/renderer/hooks/context/AuthContext';
import { rememberCurrentAuthRoute } from '@/renderer/services/authSession';
import { Alert, Button, Input, Spin, Switch } from '@arco-design/web-react';
import { ArrowLeft, ArrowRight, Brain, Check, Config, Link, NetworkTree, Tool } from '@icon-park/react';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { uuid } from '@/common/utils/utils';
import OnboardingDropZone from './OnboardingDropZone';
import OnboardingElicitCard from './OnboardingElicitCard';
import OnboardingModelSetup from './OnboardingModelSetup';
import {
  disabledNetworkGroupIds,
  initialEnabledState,
  initialNetworkState,
  nextOnboardingStep,
  ONBOARDING_STEP_COUNT,
  toggleGroup,
  type OnboardingStep,
} from './onboardingModel';
import styles from './onboarding.module.css';
import { scientificRuntimePresentation } from '@/renderer/utils/scientificRuntimePresentation';

const PRODUCT_ICON = './pwa/icon-192.png?v=9b986028';
type CapabilityTab = 'connectors' | 'skills' | 'runtimes';

const OnboardingFlow: React.FC = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { user, refresh, clearAuthCache, logout } = useAuth();
  const fallbackTaskOptions = useMemo<OnboardingTaskSuggestion[]>(
    () => [
      {
        label: t('guid.onboarding.task.optionLiterature.label'),
        description: t('guid.onboarding.task.optionLiterature.description'),
      },
      {
        label: t('guid.onboarding.task.optionData.label'),
        description: t('guid.onboarding.task.optionData.description'),
      },
      {
        label: t('guid.onboarding.task.optionProtocol.label'),
        description: t('guid.onboarding.task.optionProtocol.description'),
      },
    ],
    [t]
  );
  const [step, setStep] = useState<OnboardingStep>(0);
  const [snapshot, setSnapshot] = useState<OnboardingSnapshot | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [networkEnabled, setNetworkEnabled] = useState<Record<string, boolean>>({});
  const [connectorEnabled, setConnectorEnabled] = useState<Record<string, boolean>>({});
  const [skillEnabled, setSkillEnabled] = useState<Record<string, boolean>>({});
  const [scientificRuntimeEnabled, setScientificRuntimeEnabled] = useState<Record<string, boolean>>({});
  const [profileSummary, setProfileSummary] = useState('');
  const [files, setFiles] = useState<File[]>([]);
  const [restoredAttachmentNames, setRestoredAttachmentNames] = useState<string[]>([]);
  const [selectedTask, setSelectedTask] = useState('');
  const [customTask, setCustomTask] = useState('');
  const [taskOptions, setTaskOptions] = useState<OnboardingTaskSuggestion[]>(fallbackTaskOptions);
  const [suggestionStatus, setSuggestionStatus] = useState<'idle' | 'loading' | 'ready' | 'fallback'>('idle');
  const [suggestionView, setSuggestionView] = useState<'fallback' | 'loading' | 'agent'>('fallback');
  const [suggestionRetryGeneration, setSuggestionRetryGeneration] = useState(0);
  const [launching, setLaunching] = useState(false);
  const [switchingAccount, setSwitchingAccount] = useState(false);
  const [launchError, setLaunchError] = useState<ReturnType<typeof classifyOnboardingLaunchFailure> | null>(null);
  const [activeCapabilityTab, setActiveCapabilityTab] = useState<CapabilityTab>('connectors');
  const [capabilityQuery, setCapabilityQuery] = useState('');
  const [draftReady, setDraftReady] = useState(false);
  const historyInitialized = useRef(false);
  const loadGeneration = useRef(0);
  const launchGeneration = useRef(0);
  const launchAbort = useRef<AbortController | null>(null);
  const suggestionGeneration = useRef(0);
  const suggestionAbort = useRef<AbortController | null>(null);
  const suggestionFrameId = useRef<string | null>(null);
  const suggestionSession = useRef<OnboardingSuggestionSession | null>(null);
  const suggestionSignature = useRef<string | null>(null);
  const suggestionAutoDispatched = useRef(false);
  const suggestionViewRef = useRef<'fallback' | 'loading' | 'agent'>('fallback');
  const suggestionTaskStepActive = useRef(false);
  const suggestionFileTokens = useRef(new WeakMap<File, number>());
  const nextSuggestionFileToken = useRef(0);
  const launchArtifacts = useRef(new Map<File, OnboardingArtifactCacheEntry>());
  const pendingUploads = useRef(new Map<File, OnboardingPendingUpload>());
  const profileDocument = useRef<{ signature: string; file: File } | null>(null);
  const launchIdentity = useRef<{ signature: string; conversationId: string | null } | null>(null);
  const currentOwner = useRef<string | null>(null);
  const loadedDraftOwner = useRef<string | null>(null);
  const capabilityStep = useRef<HTMLDivElement | null>(null);
  const currentStep = useRef<OnboardingStep>(step);
  currentOwner.current = user?.id ?? null;
  currentStep.current = step;

  const load = useCallback(() => {
    launchGeneration.current += 1;
    launchAbort.current?.abort();
    launchAbort.current = null;
    suggestionGeneration.current += 1;
    suggestionAbort.current?.abort();
    suggestionAbort.current = null;
    const retiredSuggestionFrame = suggestionFrameId.current;
    suggestionFrameId.current = null;
    suggestionSession.current = null;
    suggestionSignature.current = null;
    suggestionAutoDispatched.current = false;
    suggestionViewRef.current = 'fallback';
    suggestionTaskStepActive.current = false;
    if (retiredSuggestionFrame) {
      void cancelOnboardingSuggestionFrame(retiredSuggestionFrame).catch(() =>
        console.error('[OnboardingFlow] onboarding_suggestion_cleanup_failed')
      );
    }
    launchArtifacts.current.clear();
    pendingUploads.current.clear();
    profileDocument.current = null;
    launchIdentity.current = null;
    const generation = loadGeneration.current + 1;
    loadGeneration.current = generation;
    const ownerId = user?.id ?? null;
    loadedDraftOwner.current = null;
    historyInitialized.current = false;
    setLoadError(null);
    setDraftReady(false);
    setSnapshot(null);
    setLaunching(false);
    setFiles([]);
    setRestoredAttachmentNames([]);
    setTaskOptions(fallbackTaskOptions);
    setSuggestionStatus('idle');
    setSuggestionView('fallback');
    void loadOnboardingSnapshot()
      .then((next) => {
        if (loadGeneration.current !== generation) return;
        const allowedConnectors = new Set(next.allowedConnectorIds);
        const allowedSkills = new Set(next.allowedSkillNames);
        const defaultNetwork = initialNetworkState(next);
        const defaultConnectors = initialEnabledState(
          next.connectors,
          (connector) => connector.id,
          (connector) => allowedConnectors.has(connector.id) && connector.enabled
        );
        const defaultSkills = initialEnabledState(
          next.skills,
          (skill) => skill.name,
          (skill) => allowedSkills.has(skill.name) && skill.enabled
        );
        const defaultScientificRuntimes = Object.fromEntries(
          next.scientificRuntimes.map((runtime) => [runtime.id, runtime.selected])
        );
        const draft = ownerId ? loadOnboardingDraft(ownerId) : null;
        setSnapshot(next);
        setNetworkEnabled(mergeSelection(defaultNetwork, draft?.networkEnabled));
        setConnectorEnabled(mergeSelection(defaultConnectors, draft?.connectorEnabled, allowedConnectors));
        setSkillEnabled(mergeSelection(defaultSkills, draft?.skillEnabled, allowedSkills));
        setScientificRuntimeEnabled(mergeSelection(defaultScientificRuntimes, draft?.scientificRuntimeEnabled));
        setStep(draft?.step ?? 0);
        setProfileSummary(draft?.profileSummary ?? '');
        setSelectedTask(draft?.selectedTask ?? '');
        setCustomTask(draft?.customTask ?? '');
        setRestoredAttachmentNames(draft?.attachmentNames ?? []);
        loadedDraftOwner.current = ownerId;
        setDraftReady(true);
      })
      .catch(() => {
        if (loadGeneration.current !== generation) return;
        console.error('[OnboardingFlow] onboarding_snapshot_failed');
        setLoadError(t('guid.onboarding.error.loadDescription'));
      });
  }, [fallbackTaskOptions, t, user?.id]);

  useEffect(() => {
    load();
    return () => {
      loadGeneration.current += 1;
      launchGeneration.current += 1;
      launchAbort.current?.abort();
      launchAbort.current = null;
      suggestionGeneration.current += 1;
      suggestionAbort.current?.abort();
      suggestionAbort.current = null;
      const retiredSuggestionFrame = suggestionFrameId.current;
      suggestionFrameId.current = null;
      suggestionSession.current = null;
      if (retiredSuggestionFrame) {
        void cancelOnboardingSuggestionFrame(retiredSuggestionFrame).catch(() =>
          console.error('[OnboardingFlow] onboarding_suggestion_cleanup_failed')
        );
      }
      launchArtifacts.current.clear();
      pendingUploads.current.clear();
      profileDocument.current = null;
      launchIdentity.current = null;
      loadedDraftOwner.current = null;
    };
  }, [load]);

  useEffect(() => {
    if (!draftReady || !snapshot || !user?.id || loadedDraftOwner.current !== user.id) return;
    saveOnboardingDraft(user.id, {
      step,
      networkEnabled,
      connectorEnabled,
      skillEnabled,
      scientificRuntimeEnabled,
      profileSummary,
      attachmentNames: files.length > 0 ? files.map((file) => file.name) : restoredAttachmentNames,
      selectedTask,
      customTask,
    });
  }, [
    connectorEnabled,
    customTask,
    draftReady,
    files,
    networkEnabled,
    profileSummary,
    restoredAttachmentNames,
    selectedTask,
    skillEnabled,
    scientificRuntimeEnabled,
    snapshot,
    step,
    user?.id,
  ]);

  useEffect(() => {
    if (!draftReady || historyInitialized.current) return;
    historyInitialized.current = true;
    window.history.replaceState({ ...window.history.state, synonOnboardingStep: 0 }, '', window.location.href);
    for (let index = 1; index <= step; index += 1) {
      window.history.pushState({ ...window.history.state, synonOnboardingStep: index }, '', window.location.href);
    }
  }, [draftReady, step]);

  const attachmentSignature = useMemo(
    () =>
      files
        .map((file) => {
          let token = suggestionFileTokens.current.get(file);
          if (token === undefined) {
            token = nextSuggestionFileToken.current + 1;
            nextSuggestionFileToken.current = token;
            suggestionFileTokens.current.set(file, token);
          }
          return `${token}\u0000${file.name}\u0000${file.size}\u0000${file.type}\u0000${file.lastModified}`;
        })
        .join('\u0001'),
    [files]
  );

  useEffect(() => {
    const ownerId = user?.id?.trim() ?? '';
    const description = profileSummary.trim();
    const signature = JSON.stringify([description, attachmentSignature]);
    if (!draftReady || !snapshot || !ownerId) return;
    const hasInput = description !== '' || files.length > 0;
    const automatic = step < 4 && description.length >= ONBOARDING_SUGGESTION_MIN_CHARACTERS;
    const finalAboutStep = step === 4 && hasInput;
    if (!automatic && !finalAboutStep) {
      if (step === 4 && suggestionSignature.current !== signature) {
        suggestionSignature.current = signature;
        suggestionAbort.current?.abort();
        suggestionAbort.current = null;
        const previousFrameId = suggestionFrameId.current;
        suggestionFrameId.current = null;
        suggestionSession.current = null;
        setTaskOptions(fallbackTaskOptions);
        setSuggestionStatus('idle');
        if (previousFrameId) {
          void cancelOnboardingSuggestionFrame(previousFrameId).catch(() =>
            console.error('[OnboardingFlow] onboarding_suggestion_cleanup_failed')
          );
        }
      }
      return;
    }
    if (suggestionSignature.current === signature) return;
    if (automatic && suggestionAutoDispatched.current) return;

    if (finalAboutStep) {
      suggestionViewRef.current = 'loading';
      setSuggestionView('loading');
    }

    const timer = window.setTimeout(
      () => {
        suggestionAutoDispatched.current = true;
        suggestionSignature.current = signature;
        const generation = suggestionGeneration.current + 1;
        suggestionGeneration.current = generation;
        suggestionAbort.current?.abort();
        const previousFrameId = suggestionFrameId.current;
        suggestionFrameId.current = null;
        suggestionSession.current = null;
        if (previousFrameId) {
          void cancelOnboardingSuggestionFrame(previousFrameId).catch(() =>
            console.error('[OnboardingFlow] onboarding_suggestion_cleanup_failed')
          );
        }
        setTaskOptions(fallbackTaskOptions);
        setSuggestionStatus('loading');
        if (currentStep.current === 4) {
          suggestionViewRef.current = 'loading';
          setSuggestionView('loading');
        }
        const controller = new AbortController();
        suggestionAbort.current = controller;
        const current = () =>
          !controller.signal.aborted && suggestionGeneration.current === generation && currentOwner.current === ownerId;
        const assertAuthority = () => {
          if (!current()) throw new DOMException('onboarding suggestion authority changed', 'AbortError');
        };
        const fetchImpl: typeof fetch = (input, init) => {
          assertAuthority();
          return fetch(input, { ...init, signal: init?.signal ?? controller.signal });
        };
        void (async () => {
          try {
            const project = await ensureOnboardingProject(t('guid.onboarding.projectDefaultName'), { fetchImpl });
            assertAuthority();
            const attachments = await prepareOnboardingSuggestionArtifacts(project.projectId, files, {
              fetchImpl,
              assertAuthority,
              uploadedArtifacts: launchArtifacts.current,
              pendingUploads: pendingUploads.current,
              onUploadInitialized: (file, pending) => pendingUploads.current.set(file, pending),
              onUploadAbandoned: (file) => pendingUploads.current.delete(file),
              onArtifactUploaded: (file, entry) => {
                pendingUploads.current.delete(file);
                launchArtifacts.current.set(file, entry);
              },
            });
            assertAuthority();
            const frameId = uuid(36);
            suggestionFrameId.current = frameId;
            const session = await requestOnboardingTaskSuggestions(
              {
                projectId: project.projectId,
                frameId,
                description,
                attachments,
                intentId: uuid(36),
              },
              {
                fetchImpl,
                signal: controller.signal,
                onFrameCreated: (createdFrameId) => {
                  if (current() && createdFrameId === frameId) suggestionFrameId.current = createdFrameId;
                },
              }
            );
            if (!current()) {
              await cancelOnboardingSuggestionFrame(session.frameId).catch((): void => undefined);
              return;
            }
            suggestionSession.current = session;
            suggestionFrameId.current = session.frameId;
            setTaskOptions(session.suggestions);
            setSuggestionStatus('ready');
          } catch {
            if (!current()) return;
            const failedFrame = suggestionFrameId.current;
            suggestionFrameId.current = null;
            suggestionSession.current = null;
            // The single Harness flow permits an explicit Continue to retry a failed
            // automatic suggestion request. Keep a failed final-step request
            // latched so this effect cannot create an immediate retry loop.
            if (!finalAboutStep) {
              suggestionSignature.current = null;
              if (currentStep.current === 4) setSuggestionRetryGeneration((value) => value + 1);
            }
            if (failedFrame) {
              void cancelOnboardingSuggestionFrame(failedFrame).catch(() =>
                console.error('[OnboardingFlow] onboarding_suggestion_cleanup_failed')
              );
            }
            setTaskOptions(fallbackTaskOptions);
            setSuggestionStatus('fallback');
            console.error('[OnboardingFlow] onboarding_suggestion_failed');
          }
        })();
      },
      finalAboutStep ? 0 : ONBOARDING_SUGGESTION_DEBOUNCE_MS
    );
    return () => window.clearTimeout(timer);
  }, [
    attachmentSignature,
    draftReady,
    fallbackTaskOptions,
    files,
    profileSummary,
    snapshot,
    step,
    suggestionRetryGeneration,
    t,
    user?.id,
  ]);

  useEffect(() => {
    if (step !== 4) {
      suggestionTaskStepActive.current = false;
      suggestionViewRef.current = 'fallback';
      setSuggestionView('fallback');
      return;
    }
    const entering = !suggestionTaskStepActive.current;
    suggestionTaskStepActive.current = true;
    if (entering) {
      const initial =
        suggestionStatus === 'ready'
          ? 'agent'
          : suggestionStatus === 'loading' || suggestionViewRef.current === 'loading'
            ? 'loading'
            : 'fallback';
      suggestionViewRef.current = initial;
      setSuggestionView(initial);
    } else if (suggestionViewRef.current === 'loading') {
      if (suggestionStatus === 'ready') {
        suggestionViewRef.current = 'agent';
        setSuggestionView('agent');
      } else if (suggestionStatus === 'idle' || suggestionStatus === 'fallback') {
        suggestionViewRef.current = 'fallback';
        setSuggestionView('fallback');
      }
    }
    if (suggestionViewRef.current !== 'loading') return;
    const fallbackTimer = window.setTimeout(() => {
      if (suggestionTaskStepActive.current && suggestionViewRef.current === 'loading') {
        if (suggestionSession.current) {
          suggestionViewRef.current = 'agent';
          setSuggestionView('agent');
          return;
        }
        const frameId = suggestionFrameId.current;
        suggestionGeneration.current += 1;
        suggestionAbort.current?.abort();
        suggestionAbort.current = null;
        suggestionFrameId.current = null;
        suggestionSession.current = null;
        suggestionViewRef.current = 'fallback';
        setSuggestionStatus('fallback');
        setSuggestionView('fallback');
        if (frameId) {
          void cancelOnboardingSuggestionFrame(frameId).catch(() =>
            console.error('[OnboardingFlow] onboarding_suggestion_cleanup_failed')
          );
        }
      }
    }, ONBOARDING_SUGGESTION_FALLBACK_MS);
    return () => window.clearTimeout(fallbackTimer);
  }, [step, suggestionStatus]);

  const displayedTaskOptions = useMemo(
    () => (suggestionView === 'agent' ? taskOptions : fallbackTaskOptions),
    [fallbackTaskOptions, suggestionView, taskOptions]
  );

  useEffect(() => {
    if (selectedTask && !displayedTaskOptions.some((option) => option.label === selectedTask)) setSelectedTask('');
  }, [displayedTaskOptions, selectedTask]);

  useEffect(() => {
    if (!draftReady) return;
    const onPopState = (event: PopStateEvent) => {
      const next = event.state?.synonOnboardingStep;
      if (Number.isInteger(next) && next >= 0 && next < ONBOARDING_STEP_COUNT) setStep(next as OnboardingStep);
    };
    window.addEventListener('popstate', onPopState);
    return () => window.removeEventListener('popstate', onPopState);
  }, [draftReady]);

  useEffect(() => {
    if (!draftReady) return;
    const frame = window.requestAnimationFrame(() => {
      document.querySelector<HTMLElement>('[data-onboarding-heading]')?.focus({ preventScroll: true });
    });
    return () => window.cancelAnimationFrame(frame);
  }, [draftReady, step]);

  const finalTask = customTask.trim() || selectedTask;
  const mutableNetworkGroups = snapshot?.networkGroups.filter((group) => !group.locked) ?? [];
  const enabledNetworkCount = mutableNetworkGroups.filter((group) => networkEnabled[group.id] !== false).length;
  const allNetworksEnabled = mutableNetworkGroups.length === enabledNetworkCount;
  const someNetworksEnabled = enabledNetworkCount > 0 && !allNetworksEnabled;
  const stepTitle = t(
    [
      'guid.onboarding.welcome.title',
      'guid.onboarding.network.title',
      'guid.onboarding.capabilities.title',
      'guid.onboarding.profile.title',
      'guid.onboarding.task.title',
      'guid.onboarding.model.title',
    ][step]
  );

  const moveToStep = (next: OnboardingStep) => {
    setLaunchError(null);
    window.history.pushState({ ...window.history.state, synonOnboardingStep: next }, '', window.location.href);
    setStep(next);
  };

  const moveBack = () => {
    setLaunchError(null);
    window.history.back();
  };

  const switchAccount = useCallback(async () => {
    if (switchingAccount) return;

    loadGeneration.current += 1;
    launchGeneration.current += 1;
    launchAbort.current?.abort('onboarding_account_switch');
    launchAbort.current = null;
    suggestionGeneration.current += 1;
    suggestionAbort.current?.abort('onboarding_account_switch');
    suggestionAbort.current = null;
    const retiredSuggestionFrame = suggestionFrameId.current;
    suggestionFrameId.current = null;
    suggestionSession.current = null;
    suggestionSignature.current = null;
    suggestionAutoDispatched.current = false;
    suggestionViewRef.current = 'fallback';
    suggestionTaskStepActive.current = false;
    if (retiredSuggestionFrame) {
      void cancelOnboardingSuggestionFrame(retiredSuggestionFrame).catch(() =>
        console.error('[OnboardingFlow] onboarding_suggestion_cleanup_failed')
      );
    }
    launchArtifacts.current.clear();
    pendingUploads.current.clear();
    profileDocument.current = null;
    launchIdentity.current = null;
    setLaunchError(null);
    setLaunching(false);
    setSwitchingAccount(true);
    try {
      await logout();
      void navigate('/login', { replace: true });
    } catch (error) {
      console.error('[OnboardingFlow] account_switch_failed', error);
      setSwitchingAccount(false);
    }
  }, [logout, navigate, switchingAccount]);

  if (loadError) {
    return (
      <main className={styles.root} data-testid='onboarding-load-error'>
        <OnboardingHeader user={user} switchingAccount={switchingAccount} onSwitchAccount={switchAccount} t={t} />
        <div className={styles.state}>
          <Alert type='error' title={t('guid.onboarding.error.loadTitle')} content={loadError} />
          <Button type='primary' onClick={load}>
            {t('common.retry')}
          </Button>
        </div>
      </main>
    );
  }

  if (!snapshot) {
    return (
      <main className={styles.root} data-testid='onboarding-loading' aria-busy='true'>
        <OnboardingHeader user={user} switchingAccount={switchingAccount} onSwitchAccount={switchAccount} t={t} />
        <div className={styles.state}>
          <Spin size={28} />
        </div>
      </main>
    );
  }

  const selection = {
    disabledNetworkGroupIds: disabledNetworkGroupIds(snapshot, networkEnabled),
    connectorEnabled,
    skillEnabled,
    scientificRuntimeEnabled,
  };

  const finish = async () => {
    const ownerId = currentOwner.current;
    if (!ownerId || launching) return;
    const generation = launchGeneration.current + 1;
    launchGeneration.current = generation;
    launchAbort.current?.abort();
    const controller = new AbortController();
    launchAbort.current = controller;
    const authorityCurrent = () =>
      !controller.signal.aborted && launchGeneration.current === generation && currentOwner.current === ownerId;
    const assertAuthority = () => {
      if (!authorityCurrent()) throw new DOMException('onboarding launch authority changed', 'AbortError');
    };
    const fetchImpl: typeof fetch = (input, init) => {
      assertAuthority();
      return fetch(input, { ...init, signal: init?.signal ?? controller.signal });
    };
    setLaunching(true);
    setLaunchError(null);
    try {
      const activeSuggestion = suggestionSession.current;
      const activeSuggestionFrame = suggestionFrameId.current;
      if (finalTask && activeSuggestion && suggestionViewRef.current === 'agent' && selectedTask === finalTask) {
        await resolveOnboardingTaskSuggestion(activeSuggestion, finalTask, { fetchImpl, signal: controller.signal });
      } else if (activeSuggestionFrame) {
        await cancelOnboardingSuggestionFrame(activeSuggestionFrame, { fetchImpl, signal: controller.signal });
      }
      assertAuthority();
      suggestionSession.current = null;
      suggestionFrameId.current = null;
      await saveOnboardingCapabilities(selection, snapshot, { fetchImpl });
      assertAuthority();
      const project = await ensureOnboardingProject(t('guid.onboarding.projectDefaultName'), { fetchImpl });
      assertAuthority();
      const profileSignature = JSON.stringify({
        summary: profileSummary,
        filenames: files.map((file) => file.name),
        disabledNetworkGroupIds: selection.disabledNetworkGroupIds,
        connectorIds: Object.keys(selection.connectorEnabled).filter((id) => selection.connectorEnabled[id]),
        skillNames: Object.keys(selection.skillEnabled).filter((name) => selection.skillEnabled[name]),
      });
      if (profileDocument.current?.signature !== profileSignature) {
        profileDocument.current = {
          signature: profileSignature,
          file: createOnboardingProfileFile({
            summary: profileSummary,
            filenames: files.map((file) => file.name),
            disabledNetworkGroupIds: selection.disabledNetworkGroupIds,
            connectorIds: Object.keys(selection.connectorEnabled).filter((id) => selection.connectorEnabled[id]),
            skillNames: Object.keys(selection.skillEnabled).filter((name) => selection.skillEnabled[name]),
          }),
        };
      }
      const launchSignature = JSON.stringify({
        ownerId,
        projectId: project.projectId,
        assistantId: snapshot.assistantId,
        task: finalTask,
        profileSignature,
        files: files.map((file) => ({
          name: file.name,
          size: file.size,
          type: file.type,
          lastModified: file.lastModified,
        })),
      });
      if (launchIdentity.current?.signature !== launchSignature) {
        launchIdentity.current = { signature: launchSignature, conversationId: null };
      }
      const result = await stageOnboardingTask(
        {
          projectId: project.projectId,
          projectName: project.name,
          assistantId: snapshot.assistantId ?? '',
          assistantName: snapshot.assistantName ?? '',
          task: finalTask,
          profile: { summary: profileSummary },
          files,
          profileFile: profileDocument.current.file,
          capabilities: selection,
        },
        {
          fetchImpl,
          assertAuthority,
          uploadedArtifacts: launchArtifacts.current,
          pendingUploads: pendingUploads.current,
          onUploadInitialized: (file, pending) => pendingUploads.current.set(file, pending),
          onUploadAbandoned: (file) => pendingUploads.current.delete(file),
          onArtifactUploaded: (file, entry) => {
            pendingUploads.current.delete(file);
            launchArtifacts.current.set(file, entry);
          },
          conversationId: launchIdentity.current.conversationId ?? undefined,
          onConversationCreated: (conversationId) => {
            if (authorityCurrent() && launchIdentity.current?.signature === launchSignature) {
              launchIdentity.current.conversationId = conversationId;
            }
          },
        }
      );
      assertAuthority();
      confirmOnboardingCompletion(ownerId);
      clearOnboardingDraft(ownerId);
      launchArtifacts.current.clear();
      pendingUploads.current.clear();
      profileDocument.current = null;
      launchIdentity.current = null;
      if (result.conversationId) {
        void navigate(`/conversation/${encodeURIComponent(result.conversationId)}`, { replace: true });
      } else {
        void navigate('/guid', { replace: true });
      }
    } catch (error) {
      if (!authorityCurrent()) return;
      const failure = classifyOnboardingLaunchFailure(error);
      console.error(`[OnboardingFlow] onboarding_launch_${failure}`);
      if (failure === 'authenticationRequired') {
        rememberCurrentAuthRoute();
        void navigate('/login', { replace: true });
      } else if (failure === 'sessionInvalid') {
        rememberCurrentAuthRoute();
        clearAuthCache();
        await refresh();
      } else {
        setLaunchError(failure);
      }
    } finally {
      if (authorityCurrent()) {
        launchAbort.current = null;
        setLaunching(false);
      }
    }
  };

  return (
    <main className={styles.root} data-testid='onboarding-flow'>
      <OnboardingHeader user={user} switchingAccount={switchingAccount} onSwitchAccount={switchAccount} t={t} />
      <section className={styles.flow} aria-live='polite'>
        <div
          className={styles.progress}
          role='progressbar'
          aria-label={t('guid.onboarding.progressLabel')}
          aria-valuemin={1}
          aria-valuemax={ONBOARDING_STEP_COUNT}
          aria-valuenow={step + 1}
          aria-valuetext={t('guid.onboarding.progressValue', {
            current: step + 1,
            total: ONBOARDING_STEP_COUNT,
            title: stepTitle,
          })}
        >
          <strong>
            {t('guid.onboarding.progressValue', { current: step + 1, total: ONBOARDING_STEP_COUNT, title: stepTitle })}
          </strong>
          <span className={styles.progressBars} aria-hidden='true'>
            {Array.from({ length: ONBOARDING_STEP_COUNT }, (_, index) => (
              <span key={index} className={index === step ? styles.progressActive : ''} />
            ))}
          </span>
        </div>

        {step === 0 && (
          <div className={`${styles.step} ${styles.centered}`} data-testid='onboarding-welcome'>
            <img src={PRODUCT_ICON} alt='' aria-hidden='true' className={styles.welcomeMark} />
            <h1 data-onboarding-heading tabIndex={-1}>
              {t('guid.onboarding.welcome.title')}
            </h1>
            <p>{t('guid.onboarding.welcome.subtitle')}</p>
          </div>
        )}

        {step === 1 && (
          <div className={styles.step} data-testid='onboarding-network'>
            <StepHeading icon={<NetworkTree size={28} />} title={t('guid.onboarding.network.title')} />
            <p>{t('guid.onboarding.network.subtitle')}</p>
            <div className={styles.listHeader}>
              <span>
                {t('guid.onboarding.network.allowAll')}
                <small>
                  {t('guid.onboarding.network.enabledCount', {
                    enabled: enabledNetworkCount,
                    total: mutableNetworkGroups.length,
                  })}
                </small>
              </span>
              <Switch
                checked={allNetworksEnabled}
                aria-checked={someNetworksEnabled ? 'mixed' : allNetworksEnabled}
                aria-label={t('guid.onboarding.network.allowAll')}
                onChange={(checked) =>
                  setNetworkEnabled(
                    toggleGroup(
                      networkEnabled,
                      snapshot.networkGroups.filter((group) => !group.locked).map((group) => group.id),
                      checked
                    )
                  )
                }
              />
            </div>
            <div className={styles.optionList}>
              {snapshot.networkGroups.map((group) => (
                <label key={group.id}>
                  <span>
                    <strong>{group.label}</strong>
                    <small>{group.description}</small>
                  </span>
                  <Switch
                    checked={group.locked || networkEnabled[group.id] !== false}
                    disabled={group.locked}
                    aria-label={group.label}
                    onChange={(checked) => setNetworkEnabled((current) => ({ ...current, [group.id]: checked }))}
                  />
                </label>
              ))}
            </div>
          </div>
        )}

        {step === 2 && (
          <div ref={capabilityStep} className={styles.step} data-testid='onboarding-capabilities'>
            <StepHeading icon={<Tool size={28} />} title={t('guid.onboarding.capabilities.title')} />
            <p>{t('guid.onboarding.capabilities.subtitle')}</p>
            <CapabilityTabs
              active={activeCapabilityTab}
              onChange={(tab) => {
                setActiveCapabilityTab(tab);
                if (capabilityStep.current) capabilityStep.current.scrollTop = 0;
              }}
              t={t}
            />
            <label className={styles.searchLabel} htmlFor='onboarding-capability-search'>
              <span>{t('guid.onboarding.capabilities.searchLabel')}</span>
              <Input
                id='onboarding-capability-search'
                value={capabilityQuery}
                placeholder={t('guid.onboarding.capabilities.searchPlaceholder')}
                onChange={setCapabilityQuery}
              />
            </label>
            <p className={styles.capabilityNotice}>
              {activeCapabilityTab === 'runtimes'
                ? t('guid.onboarding.capabilities.runtimeNotice')
                : t('guid.onboarding.capabilities.externalNotice')}
            </p>
            <div
              id={`onboarding-${activeCapabilityTab}-panel`}
              role='tabpanel'
              aria-labelledby={`onboarding-${activeCapabilityTab}-tab`}
              className={styles.capabilityPanel}
            >
              {activeCapabilityTab === 'connectors' ? (
                <CapabilityList
                  emptyText={t('guid.onboarding.capabilities.noConnectors')}
                  unavailableText={t('guid.onboarding.capabilities.unavailable')}
                  query={capabilityQuery}
                  items={snapshot.connectors.map((connector) => ({
                    id: connector.id,
                    title: connector.displayName || connector.name,
                    description: connector.description,
                    enabled: connectorEnabled[connector.id] ?? false,
                    available: snapshot.allowedConnectorIds.includes(connector.id),
                  }))}
                  onChange={(id, enabled) => setConnectorEnabled((current) => ({ ...current, [id]: enabled }))}
                />
              ) : activeCapabilityTab === 'skills' ? (
                <CapabilityList
                  emptyText={t('guid.onboarding.capabilities.noSkills')}
                  unavailableText={t('guid.onboarding.capabilities.unavailable')}
                  query={capabilityQuery}
                  items={snapshot.skills.map((skill) => ({
                    id: skill.name,
                    title: skill.displayName || skill.name,
                    description: skill.description,
                    enabled: skillEnabled[skill.name] ?? false,
                    available: snapshot.allowedSkillNames.includes(skill.name),
                  }))}
                  onChange={(id, enabled) => setSkillEnabled((current) => ({ ...current, [id]: enabled }))}
                />
              ) : (
                <ScientificRuntimeList
                  query={capabilityQuery}
                  items={snapshot.scientificRuntimes}
                  enabled={scientificRuntimeEnabled}
                  onChange={(id, enabled) => setScientificRuntimeEnabled((current) => ({ ...current, [id]: enabled }))}
                  t={t}
                />
              )}
            </div>
          </div>
        )}

        {step === 3 && (
          <div className={styles.step} data-testid='onboarding-profile'>
            <StepHeading icon={<Brain size={28} />} title={t('guid.onboarding.profile.title')} />
            <p>{t('guid.onboarding.profile.subtitle')}</p>
            <label className={styles.fieldLabel} htmlFor='onboarding-profile-summary'>
              {t('guid.onboarding.profile.summaryLabel')}
            </label>
            <Input.TextArea
              id='onboarding-profile-summary'
              rows={5}
              value={profileSummary}
              data-testid='onboarding-profile-summary'
              placeholder={t('guid.onboarding.profile.placeholder')}
              onChange={setProfileSummary}
            />
            {restoredAttachmentNames.length > 0 && files.length === 0 && (
              <Alert
                type='warning'
                content={t('guid.onboarding.profile.reselectFiles', { files: restoredAttachmentNames.join(', ') })}
                data-testid='onboarding-restored-files'
              />
            )}
            <OnboardingDropZone
              files={files}
              onFiles={(next) => {
                setFiles(next);
                setRestoredAttachmentNames([]);
              }}
              label={t('guid.onboarding.profile.dropZone')}
              removeLabel={t('guid.onboarding.profile.removeFile')}
            />
            {files.length > 0 && (
              <p className={styles.capabilityNotice}>{t('guid.onboarding.profile.suggestionFiles')}</p>
            )}
          </div>
        )}

        {step === 4 && (
          <div className={styles.step} data-testid='onboarding-task'>
            <StepHeading icon={<Link size={28} />} title={t('guid.onboarding.task.title')} />
            <p>{t('guid.onboarding.task.subtitle')}</p>
            {suggestionView === 'loading' && (
              <div className={styles.suggestionStatus} aria-live='polite' data-testid='onboarding-suggestions-loading'>
                <Spin size={16} />
                <span>{t('guid.onboarding.task.personalizing')}</span>
              </div>
            )}
            {suggestionView === 'fallback' && profileSummary.trim().length >= ONBOARDING_SUGGESTION_MIN_CHARACTERS && (
              <p className={styles.suggestionStatus} data-testid='onboarding-suggestions-fallback'>
                {t('guid.onboarding.task.fallback')}
              </p>
            )}
            <OnboardingElicitCard
              options={displayedTaskOptions}
              selected={selectedTask}
              customValue={customTask}
              customLabel={t('guid.onboarding.task.customLabel')}
              customPlaceholder={t('guid.onboarding.task.customPlaceholder')}
              onSelect={(value) => {
                setLaunchError(null);
                if (suggestionViewRef.current !== 'agent' && suggestionFrameId.current) {
                  const frameId = suggestionFrameId.current;
                  suggestionGeneration.current += 1;
                  suggestionAbort.current?.abort();
                  suggestionAbort.current = null;
                  suggestionFrameId.current = null;
                  void cancelOnboardingSuggestionFrame(frameId).catch(() =>
                    console.error('[OnboardingFlow] onboarding_suggestion_cleanup_failed')
                  );
                }
                setSelectedTask(value);
              }}
              onCustomChange={(value) => {
                setLaunchError(null);
                if (value.trim() && suggestionFrameId.current) {
                  const frameId = suggestionFrameId.current;
                  suggestionGeneration.current += 1;
                  suggestionAbort.current?.abort();
                  suggestionAbort.current = null;
                  suggestionFrameId.current = null;
                  suggestionSession.current = null;
                  void cancelOnboardingSuggestionFrame(frameId).catch(() =>
                    console.error('[OnboardingFlow] onboarding_suggestion_cleanup_failed')
                  );
                }
                setCustomTask(value);
              }}
            />
            <p className={styles.suggestionStatus} data-testid='onboarding-task-draft-hint'>
              {t('guid.onboarding.task.draftHint')}
            </p>
          </div>
        )}

        {step === 5 && (
          <div className={styles.step} data-testid='onboarding-model'>
            <StepHeading icon={<Config size={28} />} title={t('guid.onboarding.model.title')} />
            <p>{t('guid.onboarding.model.subtitle')}</p>
            <OnboardingModelSetup />
          </div>
        )}

        {launchError && (
          <div className={styles.launchError}>
            <Alert
              type='error'
              content={t(`guid.onboarding.error.${launchError}`)}
              data-testid='onboarding-launch-error'
            />
            {launchError === 'permissionDenied' && (
              <Button onClick={() => moveToStep(2)}>{t('guid.onboarding.actions.reviewCapabilities')}</Button>
            )}
          </div>
        )}

        <footer className={styles.actions}>
          {step > 0 ? (
            <Button icon={<ArrowLeft />} disabled={launching} onClick={moveBack}>
              {t('guid.onboarding.actions.back')}
            </Button>
          ) : (
            <span />
          )}
          {step < ONBOARDING_STEP_COUNT - 1 ? (
            <Button type='primary' icon={<ArrowRight />} onClick={() => moveToStep(nextOnboardingStep(step))}>
              {t('guid.onboarding.actions.continue')}
            </Button>
          ) : (
            <Button
              type='primary'
              icon={<Check />}
              loading={launching}
              disabled={launching}
              data-testid='onboarding-finish'
              onClick={() => void finish()}
            >
              {t('guid.onboarding.actions.finish')}
            </Button>
          )}
        </footer>
      </section>
    </main>
  );
};

const StepHeading: React.FC<{ icon: React.ReactNode; title: string }> = ({ icon, title }) => (
  <div className={styles.stepHeading}>
    <span>{icon}</span>
    <h1 data-onboarding-heading tabIndex={-1}>
      {title}
    </h1>
  </div>
);

type OnboardingHeaderProps = {
  user: { username?: string } | null;
  switchingAccount: boolean;
  onSwitchAccount: () => void;
  t: ReturnType<typeof useTranslation>['t'];
};

const OnboardingHeader: React.FC<OnboardingHeaderProps> = ({ user, switchingAccount, onSwitchAccount, t }) => (
  <header className={styles.header}>
    <div className={styles.brand}>
      <img src={PRODUCT_ICON} alt='' aria-hidden='true' className={styles.brandMark} />
      <span>{t('guid.onboarding.brand')}</span>
    </div>
    <div className={styles.accountActions}>
      {user?.username && (
        <span className={styles.accountIdentity} title={user.username}>
          {t('guid.onboarding.account.current', { username: user.username })}
        </span>
      )}
      <button
        type='button'
        className={styles.accountSwitch}
        data-testid='onboarding-switch-account'
        disabled={switchingAccount}
        aria-busy={switchingAccount}
        onClick={onSwitchAccount}
      >
        {t(switchingAccount ? 'guid.onboarding.account.switching' : 'guid.onboarding.account.switch')}
      </button>
    </div>
  </header>
);

type CapabilityListProps = {
  emptyText: string;
  unavailableText: string;
  query: string;
  items: Array<{ id: string; title: string; description: string; enabled: boolean; available: boolean }>;
  onChange: (id: string, enabled: boolean) => void;
};

const CapabilityList: React.FC<CapabilityListProps> = ({ emptyText, unavailableText, query, items, onChange }) => {
  const normalizedQuery = query.trim().toLocaleLowerCase();
  const visible = normalizedQuery
    ? items.filter((item) => `${item.title} ${item.description}`.toLocaleLowerCase().includes(normalizedQuery))
    : items;
  if (visible.length === 0) return <p className={styles.empty}>{emptyText}</p>;
  const enabledCount = items.filter((item) => item.enabled).length;
  return (
    <div>
      <p className={styles.capabilityCount}>
        {enabledCount} / {items.length}
      </p>
      <div className={styles.optionList}>
        {visible.map((item) => (
          <label key={item.id}>
            <span>
              <strong>{item.title}</strong>
              <small>{item.description}</small>
              {!item.available && <small className={styles.unavailable}>{unavailableText}</small>}
            </span>
            <Switch
              checked={item.enabled}
              disabled={!item.available}
              aria-label={item.title}
              onChange={(checked) => onChange(item.id, checked)}
            />
          </label>
        ))}
      </div>
    </div>
  );
};

const CapabilityTabs: React.FC<{
  active: CapabilityTab;
  onChange: (tab: CapabilityTab) => void;
  t: ReturnType<typeof useTranslation>['t'];
}> = ({ active, onChange, t }) => {
  const tabs: CapabilityTab[] = ['connectors', 'skills', 'runtimes'];
  const select = (tab: CapabilityTab) => {
    onChange(tab);
    window.requestAnimationFrame(() => document.getElementById(`onboarding-${tab}-tab`)?.focus());
  };
  return (
    <div role='tablist' aria-label={t('guid.onboarding.capabilities.tabsLabel')} className={styles.tabList}>
      {tabs.map((tab, index) => (
        <button
          key={tab}
          id={`onboarding-${tab}-tab`}
          type='button'
          role='tab'
          tabIndex={active === tab ? 0 : -1}
          aria-selected={active === tab}
          aria-controls={`onboarding-${tab}-panel`}
          className={active === tab ? styles.activeTab : ''}
          onClick={() => select(tab)}
          onKeyDown={(event) => {
            if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
            event.preventDefault();
            const nextIndex =
              event.key === 'Home'
                ? 0
                : event.key === 'End'
                  ? tabs.length - 1
                  : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
            select(tabs[nextIndex]);
          }}
        >
          {t(`guid.onboarding.capabilities.${tab}`)}
        </button>
      ))}
    </div>
  );
};

const ScientificRuntimeList: React.FC<{
  query: string;
  items: OnboardingScientificRuntimeOption[];
  enabled: Record<string, boolean>;
  onChange: (id: string, enabled: boolean) => void;
  t: ReturnType<typeof useTranslation>['t'];
}> = ({ query, items, enabled, onChange, t }) => {
  const normalizedQuery = query.trim().toLocaleLowerCase();
  const visible = items.filter((item) => {
    const presentation = scientificRuntimePresentation(item.id, t);
    return (
      !normalizedQuery ||
      `${presentation.title} ${presentation.description} ${item.id}`.toLocaleLowerCase().includes(normalizedQuery)
    );
  });
  if (visible.length === 0) return <p className={styles.empty}>{t('guid.onboarding.capabilities.noRuntimes')}</p>;
  const selectedInstallMB = items.reduce(
    (total, item) => total + ((enabled[item.id] ?? item.selected) ? item.estimatedInstallMB : 0),
    0
  );
  return (
    <div data-testid='onboarding-scientific-runtimes'>
      <p className={styles.runtimeTotal} data-testid='onboarding-runtime-total'>
        {t('guid.onboarding.capabilities.runtimeTotal', {
          selected: formatScientificRuntimeSize(selectedInstallMB),
        })}
      </p>
      <div className={styles.optionList}>
        {visible.map((item) => {
          const presentation = scientificRuntimePresentation(item.id, t);
          const checked = enabled[item.id] ?? item.selected;
          const stateText =
            item.status === 'ready'
              ? t('guid.onboarding.capabilities.runtimeReady')
              : checked
                ? t('guid.onboarding.capabilities.runtimeQueued')
                : '';
          return (
            <label key={item.id}>
              <span>
                <strong>{presentation.title}</strong>
                <small>{presentation.description}</small>
                <small className={styles.runtimeMeta}>
                  {t('guid.onboarding.capabilities.runtimeSize', { size: item.estimatedInstallMB })}
                  {stateText ? ` · ${stateText}` : ''}
                </small>
                {!item.available && (
                  <small className={styles.unavailable}>{t('guid.onboarding.capabilities.runtimeUnavailable')}</small>
                )}
              </span>
              <Switch
                checked={checked}
                aria-label={presentation.title}
                onChange={(nextChecked) => onChange(item.id, nextChecked)}
              />
            </label>
          );
        })}
      </div>
    </div>
  );
};

function formatScientificRuntimeSize(sizeMB: number): string {
  if (sizeMB < 1024) return `${sizeMB} MB`;
  const sizeGiB = sizeMB / 1024;
  return `${Number.isInteger(sizeGiB) ? sizeGiB.toFixed(0) : sizeGiB.toFixed(1)} GiB`;
}

function mergeSelection(
  defaults: Record<string, boolean>,
  restored?: Record<string, boolean>,
  allowed?: Set<string>
): Record<string, boolean> {
  const result = { ...defaults };
  if (!restored) return result;
  for (const key of Object.keys(result)) {
    if (allowed && !allowed.has(key)) {
      result[key] = false;
    } else if (typeof restored[key] === 'boolean') {
      result[key] = restored[key];
    }
  }
  return result;
}

export default OnboardingFlow;
