import { Button, Message, Modal, Select, Spin, Switch } from '@arco-design/web-react';
import { Refresh } from '@icon-park/react';
import React, { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import {
  deleteSynonBiomedComputeProvider,
  loadSynonBiomedComputeJobs,
  loadSynonBiomedComputeProviders,
  loadSynonBiomedComputeGpuEnabled,
  loadSynonBiomedComputeGpuInfo,
  loadSynonBiomedManagedEndpoints,
  loadSynonBiomedSshAliases,
  probeSynonBiomedComputeProvider,
  setSynonBiomedComputeGpuEnabled,
  stopSynonBiomedManagedEndpoint,
  type SynonBiomedComputeProvider,
  type SynonBiomedComputeGpuEnabled,
  type SynonBiomedComputeGpuInfo,
  type SynonBiomedComputeJob,
  type SynonBiomedManagedEndpoint,
  type SynonBiomedSshAliasesSnapshot,
} from '@/renderer/services/synonBiomedCompute';
import { loadSynonBiomedProjects, type SynonBiomedProject } from '@/renderer/services/synonBiomedGateway';
import { BioNemoSettingsModal } from './components/compute/BioNemoSettingsModal';
import { ModalProviderSettingsPage } from './components/compute/ModalProviderSettingsPage';
import { ComputeJobDetailModal, ComputeJobRow, buildSessionProviderNames } from './components/compute/ComputeJobs';
import { ComputeProviderCard, ManagedEndpointRow } from './components/compute/ComputeProviders';
import { AddSshHostModal, EditProviderModal, InferenceEndpointModal } from './components/compute/ComputeProviderModals';
import { ComputeEmptyRow, ComputeSection, WorkbenchProductRow } from './components/compute/ComputeSettingsPrimitives';
import SettingsPageHeader from './components/SettingsPageHeader';
import SettingsPageWrapper from './components/SettingsPageWrapper';

export const ComputeSettingsContent: React.FC<{
  onNavigate?: (path: string, state?: Record<string, unknown>) => void;
}> = ({ onNavigate = () => undefined }) => {
  const { t } = useTranslation();
  const [providers, setProviders] = useState<SynonBiomedComputeProvider[]>([]);
  const [aliases, setAliases] = useState<SynonBiomedSshAliasesSnapshot>({
    aliases: [],
    configFound: false,
    configPath: '',
    wildcardCount: 0,
    isWsl: false,
  });
  const [loading, setLoading] = useState(true);
  const [gpuInfo, setGpuInfo] = useState<SynonBiomedComputeGpuInfo | null>(null);
  const [gpuEnabled, setGpuEnabled] = useState<SynonBiomedComputeGpuEnabled | null>(null);
  const [managedEndpoints, setManagedEndpoints] = useState<SynonBiomedManagedEndpoint[]>([]);
  const [gpuPending, setGpuPending] = useState(false);
  const [stopPendingName, setStopPendingName] = useState<string | null>(null);
  const [pendingName, setPendingName] = useState<string | null>(null);
  const [editingProvider, setEditingProvider] = useState<SynonBiomedComputeProvider | null>(null);
  const [sshVisible, setSshVisible] = useState(false);
  const [modalProviderVisible, setModalProviderVisible] = useState(false);
  const [inferenceVisible, setInferenceVisible] = useState(false);
  const [bioNemoVisible, setBioNemoVisible] = useState(false);
  const [projects, setProjects] = useState<SynonBiomedProject[]>([]);
  const [selectedProjectId, setSelectedProjectId] = useState<string>('');
  const [jobs, setJobs] = useState<SynonBiomedComputeJob[]>([]);
  const [jobsLoading, setJobsLoading] = useState(false);
  const [jobsFailed, setJobsFailed] = useState(false);
  const [selectedJob, setSelectedJob] = useState<SynonBiomedComputeJob | null>(null);

  const loadProviders = useCallback(async () => {
    setLoading(true);
    try {
      const [nextProviders, nextAliases, nextGpuInfo, nextGpuEnabled, nextManagedEndpoints] = await Promise.all([
        loadSynonBiomedComputeProviders(),
        loadSynonBiomedSshAliases().catch((error): SynonBiomedSshAliasesSnapshot | null => {
          console.warn('Failed to load SSH aliases:', error);
          return null;
        }),
        loadSynonBiomedComputeGpuInfo().catch((error): SynonBiomedComputeGpuInfo | null => {
          console.warn('Failed to load host GPU information:', error);
          return null;
        }),
        loadSynonBiomedComputeGpuEnabled().catch((error): SynonBiomedComputeGpuEnabled | null => {
          console.warn('Failed to load host GPU enabled state:', error);
          return null;
        }),
        loadSynonBiomedManagedEndpoints().catch((error): SynonBiomedManagedEndpoint[] => {
          console.warn('Failed to load managed compute endpoints:', error);
          return [];
        }),
      ]);
      setProviders(nextProviders);
      if (nextAliases) setAliases(nextAliases);
      setGpuInfo(nextGpuInfo);
      setGpuEnabled(nextGpuEnabled);
      setManagedEndpoints(nextManagedEndpoints);
    } catch (error) {
      console.error('Failed to fetch Synon Biomed compute providers:', error);
      Message.error(t('settings.computeFetchError'));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void loadProviders();
  }, [loadProviders]);

  useEffect(() => {
    let alive = true;
    void loadSynonBiomedProjects()
      .then((nextProjects) => {
        if (!alive) return;
        setProjects(nextProjects);
        setSelectedProjectId((current) => current || nextProjects[0]?.projectId || '');
      })
      .catch((error) => {
        console.error('Failed to load projects for the compute workbench:', error);
        if (alive) setJobsFailed(true);
      });
    return () => {
      alive = false;
    };
  }, []);

  const loadJobs = useCallback(async (projectId: string) => {
    if (!projectId) {
      setJobs([]);
      return;
    }
    setJobsLoading(true);
    setJobsFailed(false);
    try {
      setJobs(await loadSynonBiomedComputeJobs(projectId));
    } catch (error) {
      console.error('Failed to load Synon Biomed compute jobs:', error);
      setJobs([]);
      setJobsFailed(true);
    } finally {
      setJobsLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadJobs(selectedProjectId);
  }, [loadJobs, selectedProjectId]);

  const toggleGpu = useCallback(
    async (enabled: boolean) => {
      const previous = gpuEnabled;
      if (!previous) return;
      setGpuEnabled({ ...previous, enabled });
      setGpuPending(true);
      try {
        await setSynonBiomedComputeGpuEnabled(enabled);
        const [nextInfo, nextEnabled] = await Promise.all([
          loadSynonBiomedComputeGpuInfo(),
          loadSynonBiomedComputeGpuEnabled(),
        ]);
        setGpuInfo(nextInfo);
        setGpuEnabled(nextEnabled);
      } catch (error) {
        setGpuEnabled(previous);
        console.error('Failed to update host GPU state:', error);
        Message.error(t('settings.computeWorkspace.gpuUpdateFailed'));
      } finally {
        setGpuPending(false);
      }
    },
    [gpuEnabled, t]
  );

  const stopManagedEndpoint = useCallback(
    async (endpoint: SynonBiomedManagedEndpoint) => {
      setStopPendingName(endpoint.name);
      try {
        await stopSynonBiomedManagedEndpoint(endpoint.name);
        Message.success(t('settings.computeEndpointStopped'));
        await loadProviders();
      } catch (error) {
        console.error('Failed to stop managed endpoint:', error);
        Message.error(t('settings.computeWorkspace.endpointStopFailed'));
      } finally {
        setStopPendingName(null);
      }
    },
    [loadProviders, t]
  );

  const probeProvider = useCallback(
    async (provider: SynonBiomedComputeProvider) => {
      setPendingName(provider.name);
      try {
        await probeSynonBiomedComputeProvider(provider.name);
        Message.success(
          t('settings.computeProbeSuccess', {
            name: provider.displayName,
          })
        );
        await loadProviders();
      } catch (error) {
        console.error('Synon Biomed compute probe failed:', error);
        Message.error(t('settings.computeWorkspace.probeFailed'));
      } finally {
        setPendingName(null);
      }
    },
    [loadProviders, t]
  );

  const removeProvider = useCallback(
    (provider: SynonBiomedComputeProvider) => {
      Modal.confirm({
        title: t('settings.computeDeleteTitle'),
        content: t('settings.computeDeleteConfirm', {
          name: provider.displayName,
        }),
        okButtonProps: { status: 'danger' },
        onOk: async () => {
          setPendingName(provider.name);
          try {
            await deleteSynonBiomedComputeProvider(provider);
            Message.success(t('settings.computeDeleted'));
            await loadProviders();
          } catch (error) {
            console.error('Failed to delete Synon Biomed compute provider:', error);
            Message.error(t('settings.computeWorkspace.deleteFailed'));
          } finally {
            setPendingName(null);
          }
        },
      });
    },
    [loadProviders, t]
  );

  const sshProviders = providers.filter((provider) => provider.family === 'ssh');
  const cloudProviders = providers.filter((provider) => provider.family === 'byoc' || provider.family === 'proxy');
  const inferenceProviders = providers.filter((provider) => provider.family === 'infer');

  return (
    <div className='settings-compute-page flex min-h-0 flex-col gap-16px' data-testid='synon-biomed-compute-section'>
      <main className='w-full pb-72px'>
        <SettingsPageHeader
          data-testid='compute-header'
          title={t('settings.computeWorkspace.title')}
          description={t('settings.computeWorkspace.description')}
        />

        <div className='compute-section-stack flex flex-col'>
          {loading && providers.length === 0 ? (
            <div className='h-160px flex items-center justify-center'>
              <Spin />
            </div>
          ) : (
            <>
              <div className='compute-overview-grid'>
                <ComputeSection
                  title={t('settings.computeWorkspace.sshHosts')}
                  description={t('settings.computeWorkspace.sshHostsDescription')}
                  icon='compute'
                  action={
                    <Button type='secondary' size='small' onClick={() => setSshVisible(true)}>
                      {t('settings.computeWorkspace.addSshHost')}
                    </Button>
                  }
                >
                  {sshProviders.length > 0 ? (
                    sshProviders.map((provider) => (
                      <ComputeProviderCard
                        key={provider.name}
                        provider={provider}
                        pending={pendingName === provider.name}
                        onProbe={() => void probeProvider(provider)}
                        onEdit={() => setEditingProvider(provider)}
                        onDelete={() => removeProvider(provider)}
                      />
                    ))
                  ) : (
                    <ComputeEmptyRow text={t('settings.computeWorkspace.noSshHosts')} />
                  )}
                </ComputeSection>

                <ComputeSection title={t('settings.computeWorkspace.cloudProviders')} icon='compute'>
                  <WorkbenchProductRow
                    title='Modal'
                    description={t('settings.computeWorkspace.modalDescription')}
                    actionLabel={t('settings.computeWorkspace.configure')}
                    onAction={() => setModalProviderVisible(true)}
                  />
                  {cloudProviders.map((provider) => (
                    <ComputeProviderCard
                      key={provider.name}
                      provider={provider}
                      pending={pendingName === provider.name}
                      onProbe={() => void probeProvider(provider)}
                      onEdit={() => setEditingProvider(provider)}
                      onDelete={() => removeProvider(provider)}
                    />
                  ))}
                </ComputeSection>

                <ComputeSection
                  title={t('settings.computeWorkspace.modelEndpoints')}
                  description={t('settings.computeWorkspace.modelEndpointsDescription')}
                  icon='models'
                >
                  <WorkbenchProductRow
                    title='NVIDIA BioNeMo NIM'
                    description={t('settings.computeWorkspace.bioNemoDescription')}
                    actionLabel={t('settings.computeWorkspace.configure')}
                    onAction={() => setBioNemoVisible(true)}
                  />
                </ComputeSection>
              </div>

              {gpuInfo && gpuEnabled ? (
                <ComputeSection title={t('settings.computeHostGpu')} icon='compute'>
                  <div className='flex flex-col sm:flex-row sm:items-center justify-between gap-12px px-14px py-12px border border-arco-2 rd-6px'>
                    <div className='min-w-0'>
                      <div className='text-14px font-600 text-t-primary'>
                        {gpuInfo.name ?? gpuEnabled.name ?? t('settings.computeGpuUnavailable')}
                      </div>
                      <div className='text-12px text-t-tertiary mt-3px'>
                        {gpuInfo.available
                          ? `${gpuInfo.count} GPU · ${
                              gpuInfo.memoryMb ? `${Math.round(gpuInfo.memoryMb / 1024)} GB · ` : ''
                            }CUDA ${gpuInfo.cudaVersion ?? '-'}`
                          : t('settings.computeGpuUnavailableHint')}
                      </div>
                    </div>
                    <Switch
                      aria-label={t('settings.computeHostGpu')}
                      checked={gpuEnabled.enabled}
                      loading={gpuPending}
                      disabled={!gpuEnabled.present && !gpuInfo.available}
                      onChange={(checked) => void toggleGpu(checked)}
                    />
                  </div>
                </ComputeSection>
              ) : null}

              <ComputeSection
                title={t('settings.computeJobs')}
                description={t('settings.computeJobsHint')}
                icon='compute'
              >
                <div className='flex flex-col sm:flex-row sm:items-center justify-between gap-8px'>
                  <Select
                    aria-label={t('settings.computeProject')}
                    value={selectedProjectId || undefined}
                    onChange={setSelectedProjectId}
                    placeholder={t('settings.computeProjectPlaceholder')}
                    className='w-full sm:w-280px'
                  >
                    {projects.map((project) => (
                      <Select.Option key={project.projectId} value={project.projectId}>
                        {project.name}
                      </Select.Option>
                    ))}
                  </Select>
                  <Button
                    type='secondary'
                    size='small'
                    icon={<Refresh theme='outline' size='14' />}
                    loading={jobsLoading}
                    disabled={!selectedProjectId}
                    aria-label={t('settings.computeRefreshJobs')}
                    onClick={() => void loadJobs(selectedProjectId)}
                  >
                    {t('common.refresh')}
                  </Button>
                </div>
                {jobsLoading && jobs.length === 0 ? (
                  <div className='h-96px flex items-center justify-center'>
                    <Spin size={18} />
                  </div>
                ) : jobsFailed ? (
                  <div
                    role='alert'
                    className='px-14px py-12px border border-red-2 bg-red-1 rd-6px text-12px text-red-6'
                  >
                    {t('settings.computeWorkspace.jobsLoadFailed')}
                  </div>
                ) : jobs.length > 0 ? (
                  <div className='border border-arco-2 rd-6px overflow-hidden'>
                    {jobs.map((job) => (
                      <ComputeJobRow key={job.jobId} job={job} onOpen={() => setSelectedJob(job)} />
                    ))}
                  </div>
                ) : (
                  <ComputeEmptyRow
                    text={selectedProjectId ? t('settings.computeJobsEmpty') : t('settings.computeProjectsEmpty')}
                  />
                )}
              </ComputeSection>

              <ComputeSection
                title={t('settings.computeWorkspace.endpointManagement')}
                description={t('settings.computeWorkspace.endpointManagementDescription')}
                icon='compute'
                action={
                  <div className='flex flex-wrap items-center gap-8px'>
                    <Button
                      type='secondary'
                      size='small'
                      icon={<Refresh theme='outline' size={14} />}
                      loading={loading}
                      onClick={() => void loadProviders()}
                    >
                      {t('settings.computeWorkspace.refreshStatus')}
                    </Button>
                    <Button type='primary' size='small' onClick={() => setInferenceVisible(true)}>
                      {t('settings.computeWorkspace.addModelEndpoint')}
                    </Button>
                  </div>
                }
              >
                {managedEndpoints.map((endpoint) => (
                  <ManagedEndpointRow
                    key={endpoint.name}
                    endpoint={endpoint}
                    pending={stopPendingName === endpoint.name}
                    onStop={() => void stopManagedEndpoint(endpoint)}
                  />
                ))}
                {inferenceProviders.map((provider) => (
                  <ComputeProviderCard
                    key={provider.name}
                    provider={provider}
                    pending={pendingName === provider.name}
                    onProbe={() => void probeProvider(provider)}
                    onEdit={() => setEditingProvider(provider)}
                    onDelete={() => removeProvider(provider)}
                  />
                ))}
                {managedEndpoints.length === 0 && inferenceProviders.length === 0 ? (
                  <ComputeEmptyRow text={t('settings.computeWorkspace.noModelEndpoints')} />
                ) : null}
              </ComputeSection>
            </>
          )}
        </div>
      </main>

      <EditProviderModal
        provider={editingProvider}
        onClose={() => setEditingProvider(null)}
        onSaved={async () => {
          setEditingProvider(null);
          Message.success(t('settings.computeSaved'));
          await loadProviders();
        }}
      />
      <AddSshHostModal
        visible={sshVisible}
        aliases={aliases}
        onClose={() => setSshVisible(false)}
        onAdded={async () => {
          setSshVisible(false);
          Message.success(t('settings.computeWorkspace.sshAdded'));
          await loadProviders();
        }}
      />
      <ModalProviderSettingsPage
        visible={modalProviderVisible}
        onClose={() => setModalProviderVisible(false)}
        onOpenCredentials={() => {
          setModalProviderVisible(false);
          onNavigate('/settings/credentials');
        }}
        onTry={(prefillPrompt) => {
          setModalProviderVisible(false);
          onNavigate('/guid', { prefillPrompt });
        }}
      />
      <InferenceEndpointModal
        visible={inferenceVisible}
        providers={inferenceProviders}
        onClose={() => setInferenceVisible(false)}
        onAdded={async () => {
          setInferenceVisible(false);
          await loadProviders();
        }}
      />
      <BioNemoSettingsModal
        visible={bioNemoVisible}
        providers={inferenceProviders}
        onClose={() => setBioNemoVisible(false)}
        onChanged={loadProviders}
        onOpenCredentials={() => {
          setBioNemoVisible(false);
          onNavigate('/settings/credentials');
        }}
        onOpenSkills={() => {
          setBioNemoVisible(false);
          onNavigate('/settings/skills');
        }}
      />
      <ComputeJobDetailModal
        job={selectedJob}
        providerNames={buildSessionProviderNames(providers, managedEndpoints, selectedJob)}
        onClose={() => setSelectedJob(null)}
      />
    </div>
  );
};

const ComputeSettings: React.FC = () => {
  const navigate = useNavigate();
  return (
    <SettingsPageWrapper>
      <ComputeSettingsContent onNavigate={(path, state) => navigate(path, state ? { state } : undefined)} />
    </SettingsPageWrapper>
  );
};

export default ComputeSettings;
