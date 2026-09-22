import {
  attachSynonBiomedExpertConnector,
  deleteSynonBiomedExpertProfile,
  detachSynonBiomedExpertConnector,
  loadSynonBiomedExpertInstructions,
  loadSynonBiomedExpertProfilesWithRuntimeConnectors,
  saveSynonBiomedExpertInstructions,
  setSynonBiomedExpertProfileEnabled,
  updateSynonBiomedExpertProfile,
  updateSynonBiomedExpertSkills,
  type SynonBiomedExpertProfile,
} from '@/renderer/services/agents/synonBiomedExpertProfiles';
import {
  loadSynonBiomedMcpServers,
  loadSynonBiomedSkills,
  type SynonBiomedMcpServer,
  type SynonBiomedSkill,
} from '@/renderer/services/synonBiomedCapabilities';
import { localizeSynonBiomedExpertProfile } from '@/renderer/services/agents/synonBiomedExpertLocalization';
import SynonBiomedAvatar from '@/renderer/components/synonBiomed/SynonBiomedAvatar';
import {
  findSynonBiomedExpertUsage,
  loadSynonBiomedExpertUsage,
  type SynonBiomedExpertUsageByName,
} from '@/renderer/services/agents/synonBiomedExpertUsage';
import { Button, Empty, Input, Message, Modal, Select, Spin, Switch, Tabs } from '@arco-design/web-react';
import { Close, Search } from '@icon-park/react';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import SettingsPageHeader from '../components/SettingsPageHeader';
import SynonBiomedExpertProfileModal from './SynonBiomedExpertProfileModal';

import {
  SettingsGeneratedArtwork,
  SettingsGeneratedEmptyArtwork,
  SettingsGeneratedIcon,
  type SettingsGeneratedArtworkId,
  type SettingsGeneratedIconId,
} from '../components/SettingsGeneratedAsset';
import { SettingsToolbar } from '../components/SettingsPrimitives';
import { compactSettingsDescription } from '../components/settingsPresentation';

type ExpertWorkbenchProps = {
  /** When false, renders without the page-level header (used inside the merged library page). */
  withHeader?: boolean;
};

const createRequestedFromHash = (): boolean => {
  if (typeof window === 'undefined') return false;
  const query = window.location.hash.split('?', 2)[1] ?? '';
  return new URLSearchParams(query).get('create') === '1';
};

const clearCreateRequestFromHash = (): void => {
  if (typeof window === 'undefined' || !window.location.hash.includes('?')) return;
  const [route, query = ''] = window.location.hash.split('?', 2);
  const params = new URLSearchParams(query);
  if (!params.has('create')) return;
  params.delete('create');
  const suffix = params.size > 0 ? `?${params.toString()}` : '';
  window.history.replaceState(
    window.history.state,
    '',
    `${window.location.pathname}${window.location.search}${route}${suffix}`
  );
};

type ExpertDraft = {
  displayName: string;
  description: string;
  instructions: string;
  skillNames: string[];
  connectorIds: string[];
};

const { TabPane } = Tabs;

function sameSet(left: string[], right: string[]): boolean {
  return left.length === right.length && left.every((value) => right.includes(value));
}

function connectorIdsFor(profile: SynonBiomedExpertProfile, connectors: SynonBiomedMcpServer[]): string[] {
  const available = new Set(connectors.map((connector) => connector.id));
  const ids = new Set((profile.connectorIds ?? []).filter((id) => available.has(id)));
  for (const connector of connectors) {
    if (connector.attachedAgents.includes(profile.name)) {
      ids.add(connector.id);
    }
  }
  return [...ids];
}

function resolveExpertIcon(
  profile: Pick<SynonBiomedExpertProfile, 'name' | 'displayName' | 'description'>
): SettingsGeneratedIconId {
  const searchable = `${profile.name} ${profile.displayName} ${profile.description}`.toLowerCase();
  if (/variant|genom|dna|gene/.test(searchable)) return 'connector-dna';
  if (/pathway|network|interaction/.test(searchable)) return 'connector-regulation';
  if (/expression|transcript|cell/.test(searchable)) return 'connector-bars';
  if (/literature|research|review|publication/.test(searchable)) return 'connector-book';
  if (/chem|drug|molecule|compound/.test(searchable)) return 'connector-molecule';
  if (/rna/.test(searchable)) return 'connector-rna';
  return 'connector-protein';
}

function resolveExpertArtwork(
  profile: Pick<SynonBiomedExpertProfile, 'name' | 'displayName' | 'description'>
): SettingsGeneratedArtworkId {
  switch (resolveExpertIcon(profile)) {
    case 'connector-dna':
      return 'dna';
    case 'connector-regulation':
      return 'regulation';
    case 'connector-bars':
      return 'expression';
    case 'connector-book':
      return 'book';
    case 'connector-molecule':
      return 'chemistry';
    case 'connector-rna':
      return 'rna';
    default:
      return 'protein';
  }
}

const ExpertWorkbench: React.FC<ExpertWorkbenchProps> = ({ withHeader = true }) => {
  const { t } = useTranslation();
  const [message, messageContext] = Message.useMessage({ maxCount: 4 });
  const [profiles, setProfiles] = useState<SynonBiomedExpertProfile[]>([]);
  const [skills, setSkills] = useState<SynonBiomedSkill[]>([]);
  const [connectors, setConnectors] = useState<SynonBiomedMcpServer[]>([]);
  const [expertUsage, setExpertUsage] = useState<SynonBiomedExpertUsageByName | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');
  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState<'all' | 'personal' | 'builtin'>('all');
  const [selected, setSelected] = useState<SynonBiomedExpertProfile | null>(null);
  const [draft, setDraft] = useState<ExpertDraft | null>(null);
  const [baseline, setBaseline] = useState<ExpertDraft | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [pendingProfileName, setPendingProfileName] = useState<string | null>(null);
  const [createVisible, setCreateVisible] = useState(createRequestedFromHash());
  const detailGeneration = useRef(0);
  const catalogGeneration = useRef(0);
  const translationRef = useRef(t);
  translationRef.current = t;

  const localizedProfiles = useMemo(
    () => profiles.map((profile) => localizeSynonBiomedExpertProfile(profile, t)),
    [profiles, t]
  );
  const localizedSelected = useMemo(
    () => (selected ? localizeSynonBiomedExpertProfile(selected, t) : null),
    [selected, t]
  );

  const reloadCatalog = useCallback(async () => {
    const generation = ++catalogGeneration.current;
    setLoading(true);
    setLoadError('');
    setExpertUsage(null);

    let nextProfiles: SynonBiomedExpertProfile[];
    try {
      nextProfiles = await loadSynonBiomedExpertProfilesWithRuntimeConnectors();
      if (generation !== catalogGeneration.current) return;
      setProfiles(nextProfiles);
      setSelected((current) =>
        current ? (nextProfiles.find((profile) => profile.name === current.name) ?? null) : current
      );
    } catch (error) {
      if (generation !== catalogGeneration.current) return;
      console.error('Failed to load expert catalog:', error);
      setLoadError(translationRef.current('settings.expertsSettings.loadFailed'));
      setLoading(false);
      return;
    }

    // The profile list is the critical first paint. Skills, MCP metadata and
    // usage are secondary enrichment and must not keep the whole page blocked.
    if (generation === catalogGeneration.current) setLoading(false);
    void Promise.all([
      loadSynonBiomedSkills().catch((error: unknown): SynonBiomedSkill[] => {
        console.error('Failed to load expert skills:', error);
        return [];
      }),
      loadSynonBiomedMcpServers().catch((error: unknown): SynonBiomedMcpServer[] => {
        console.error('Failed to load expert connectors:', error);
        return [];
      }),
      loadSynonBiomedExpertUsage().catch((error: unknown): SynonBiomedExpertUsageByName | null => {
        console.error('Failed to load expert usage:', error);
        return null;
      }),
    ]).then(([nextSkills, nextConnectors, nextUsage]) => {
      if (generation !== catalogGeneration.current) return;
      setSkills(nextSkills);
      setConnectors(nextConnectors);
      setExpertUsage(nextUsage);
    });
  }, []);

  useEffect(() => {
    void reloadCatalog();
    return () => {
      catalogGeneration.current += 1;
    };
  }, [reloadCatalog]);

  const openProfile = useCallback(
    async (profile: SynonBiomedExpertProfile) => {
      const generation = ++detailGeneration.current;
      setSelected(profile);
      setDraft(null);
      setBaseline(null);
      setDetailLoading(true);
      try {
        const instructions = await loadSynonBiomedExpertInstructions(profile);
        if (generation !== detailGeneration.current) return;
        const nextDraft: ExpertDraft = {
          displayName: profile.displayName,
          description: profile.description,
          instructions,
          skillNames: [...profile.skillNames],
          connectorIds: connectorIdsFor(profile, connectors),
        };
        setDraft(nextDraft);
        setBaseline(nextDraft);
      } catch (error) {
        console.error('Failed to load expert details:', error);
        if (generation === detailGeneration.current) {
          message.error(translationRef.current('settings.expertsSettings.detailLoadFailed'));
          setSelected(null);
        }
      } finally {
        if (generation === detailGeneration.current) setDetailLoading(false);
      }
    },
    [connectors, message]
  );

  const closeDetail = useCallback(() => {
    detailGeneration.current += 1;
    setSelected(null);
    setDraft(null);
    setBaseline(null);
  }, []);

  const dirty = useMemo(() => {
    if (!draft || !baseline) return false;
    return (
      draft.displayName !== baseline.displayName ||
      draft.description !== baseline.description ||
      draft.instructions !== baseline.instructions ||
      !sameSet(draft.skillNames, baseline.skillNames) ||
      !sameSet(draft.connectorIds, baseline.connectorIds)
    );
  }, [baseline, draft]);

  const save = useCallback(async () => {
    if (!selected || !draft || !baseline || saving || pendingProfileName || !dirty) return;
    if (!draft.displayName.trim() || !draft.description.trim()) {
      message.error(translationRef.current('settings.expertsSettings.nameDescriptionRequired'));
      return;
    }
    setSaving(true);
    let mutationStarted = false;
    try {
      let nextProfile = selected;
      if (
        selected.source === 'user' &&
        (draft.displayName !== baseline.displayName || draft.description !== baseline.description)
      ) {
        mutationStarted = true;
        nextProfile = await updateSynonBiomedExpertProfile(selected.name, {
          name: selected.name,
          displayName: draft.displayName.trim(),
          description: draft.description.trim(),
          systemPrompt: draft.instructions,
          enabled: selected.enabled,
        });
      } else if (draft.instructions !== baseline.instructions) {
        mutationStarted = true;
        await saveSynonBiomedExpertInstructions(selected, draft.instructions);
      }

      if (!sameSet(draft.skillNames, baseline.skillNames)) {
        mutationStarted = true;
        nextProfile = await updateSynonBiomedExpertSkills(selected.name, baseline.skillNames, draft.skillNames);
      }

      const addedConnectors = draft.connectorIds.filter((id) => !baseline.connectorIds.includes(id));
      const removedConnectors = baseline.connectorIds.filter((id) => !draft.connectorIds.includes(id));
      if (addedConnectors.length > 0 || removedConnectors.length > 0) mutationStarted = true;
      await Promise.all([
        ...removedConnectors.map((serverId) => detachSynonBiomedExpertConnector(selected.name, serverId)),
        ...addedConnectors.map((serverId) => attachSynonBiomedExpertConnector(selected.name, serverId)),
      ]);

      const [nextProfiles, nextConnectors] = await Promise.all([
        loadSynonBiomedExpertProfilesWithRuntimeConnectors(),
        loadSynonBiomedMcpServers(),
      ]);
      setProfiles(nextProfiles);
      setConnectors(nextConnectors);
      nextProfile = nextProfiles.find((profile) => profile.name === nextProfile.name) ?? nextProfile;
      const nextDraft: ExpertDraft = {
        ...draft,
        displayName: nextProfile.displayName,
        description: nextProfile.description,
        skillNames: [...nextProfile.skillNames],
        connectorIds: connectorIdsFor(nextProfile, nextConnectors),
      };
      setSelected(nextProfile);
      setDraft(nextDraft);
      setBaseline(nextDraft);
      message.success(translationRef.current('settings.expertsSettings.saved'));
    } catch (error) {
      console.error('Failed to save expert settings:', error);
      if (mutationStarted) {
        message.warning(translationRef.current('settings.expertsSettings.savePartiallyApplied'));
        detailGeneration.current += 1;
        setSelected(null);
        setDraft(null);
        setBaseline(null);
        await reloadCatalog();
      } else {
        message.error(translationRef.current('settings.expertsSettings.saveFailed'));
      }
    } finally {
      setSaving(false);
    }
  }, [baseline, dirty, draft, message, pendingProfileName, reloadCatalog, saving, selected]);

  const toggleEnabled = useCallback(
    async (profile: SynonBiomedExpertProfile, enabled: boolean) => {
      if (profile.source !== 'user' || pendingProfileName) return;
      setPendingProfileName(profile.name);
      try {
        const updated = await setSynonBiomedExpertProfileEnabled(profile.name, enabled);
        setProfiles((current) => current.map((item) => (item.name === updated.name ? updated : item)));
        if (selected?.name === updated.name) setSelected(updated);
      } catch (error) {
        console.error('Failed to update expert state:', error);
        message.error(translationRef.current('settings.expertsSettings.statusUpdateFailed'));
      } finally {
        setPendingProfileName(null);
      }
    },
    [message, pendingProfileName, selected]
  );

  const removeProfile = useCallback(() => {
    if (!selected || selected.source !== 'user') return;
    Modal.confirm({
      title: translationRef.current('settings.expertsSettings.deleteTitle'),
      content: translationRef.current('settings.expertsSettings.deleteBody', { name: selected.displayName }),
      okButtonProps: { status: 'danger' },
      onOk: async () => {
        try {
          await deleteSynonBiomedExpertProfile(selected.name);
          closeDetail();
          await reloadCatalog();
        } catch (error) {
          console.error('Failed to delete expert:', error);
          message.error(translationRef.current('settings.expertsSettings.deleteFailed'));
        }
      },
    });
  }, [closeDetail, message, reloadCatalog, selected]);

  const closeCreate = useCallback(() => {
    setCreateVisible(false);
    clearCreateRequestFromHash();
  }, []);

  useEffect(() => {
    const handleHashChange = () => {
      if (createRequestedFromHash()) setCreateVisible(true);
    };
    window.addEventListener('hashchange', handleHashChange);
    return () => window.removeEventListener('hashchange', handleHashChange);
  }, []);

  const visibleProfiles = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase();
    return localizedProfiles.filter((profile) => {
      if (filter === 'personal' && profile.source !== 'user') return false;
      if (filter === 'builtin' && profile.source === 'user') return false;
      if (!normalizedQuery) return true;
      return [profile.name, profile.displayName, profile.description].some((value) =>
        value.toLowerCase().includes(normalizedQuery)
      );
    });
  }, [filter, localizedProfiles, query]);

  const groupedProfiles = useMemo(
    () => ({
      personal: visibleProfiles.filter((profile) => profile.source === 'user'),
      builtin: visibleProfiles.filter((profile) => profile.source !== 'user'),
    }),
    [visibleProfiles]
  );
  const availableSkillCount = skills.length;
  const availableConnectorCount = connectors.filter((connector) => connector.enabled).length;

  const detailModal = selected
    ? (() => {
        const selectedPresentation = localizedSelected ?? selected;
        const availableSkills = skills.filter((skill) => !draft?.skillNames.includes(skill.name));
        const availableConnectors = connectors.filter(
          (connector) => connector.enabled && !draft?.connectorIds.includes(connector.id)
        );
        return (
          <Modal
            visible
            title={selectedPresentation.displayName}
            onCancel={closeDetail}
            onOk={() => void save()}
            okText={t('settings.expertsSettings.saveChanges')}
            cancelText={t('common.cancel')}
            okButtonProps={{
              loading: saving,
              disabled: !dirty || !draft || !baseline || pendingProfileName !== null,
            }}
            unmountOnExit
            style={{ width: 'min(760px, 92vw)' }}
          >
            <div data-testid='expert-detail-modal' className='max-h-[min(70vh,640px)] overflow-y-auto pr-4px'>
              <div className='mx-auto w-full max-w-760px'>
                {detailLoading || !draft || !baseline ? (
                  <div className='flex min-h-240px items-center justify-center'>
                    <Spin />
                  </div>
                ) : (
                  <>
                    <header className='border-b border-arco-2 pb-22px'>
                      <div className='flex items-start gap-14px'>
                        <div className='flex size-42px flex-shrink-0 items-center justify-center rounded-8px bg-fill-2 text-t-secondary'>
                          <SynonBiomedAvatar size={22} />
                        </div>
                        <div className='min-w-0 flex-1'>
                          <div className='flex flex-wrap items-center gap-8px'>
                            <h1 className='m-0 text-22px font-650 text-t-primary'>
                              {selectedPresentation.displayName}
                            </h1>
                            <span className='rounded-4px bg-fill-2 px-7px py-2px text-11px text-t-secondary'>
                              {selected.source === 'user'
                                ? t('settings.expertsSettings.custom')
                                : t('settings.expertsSettings.builtin')}
                            </span>
                          </div>
                          <div className='mt-4px text-12px text-t-tertiary'>
                            {t('settings.expertsSettings.identifier', { id: selected.name })}
                          </div>
                          <p className='mb-0 mt-8px text-13px leading-20px text-t-secondary'>
                            {selectedPresentation.description}
                          </p>
                        </div>
                        <div className='flex shrink-0 items-center gap-10px'>
                          <Switch
                            aria-label={t('settings.expertsSettings.enableNamed', {
                              name: selectedPresentation.displayName,
                            })}
                            checked={selected.enabled}
                            loading={pendingProfileName === selected.name}
                            disabled={selected.source !== 'user' || pendingProfileName !== null}
                            onChange={(enabled) => void toggleEnabled(selected, enabled)}
                          />
                          {selected.source === 'user' ? (
                            <Button status='danger' type='text' className='!rounded-6px' onClick={removeProfile}>
                              {t('settings.expertsSettings.deleteExpert')}
                            </Button>
                          ) : null}
                        </div>
                      </div>
                    </header>

                    {selected.source === 'user' ? (
                      <section className='border-b border-arco-2 py-22px'>
                        <h2 className='m-0 text-15px font-600 text-t-primary'>
                          {t('settings.expertsSettings.identity')}
                        </h2>
                        <p className='mb-14px mt-4px text-12px text-t-tertiary'>
                          {t('settings.expertsSettings.identityDescription')}
                        </p>
                        <div className='grid gap-12px'>
                          <label className='grid gap-6px text-12px text-t-secondary'>
                            {t('settings.expertsSettings.name')}
                            <Input
                              value={draft.displayName}
                              onChange={(displayName) => setDraft({ ...draft, displayName })}
                            />
                          </label>
                          <label className='grid gap-6px text-12px text-t-secondary'>
                            {t('settings.expertsSettings.descriptionField')}
                            <Input.TextArea
                              value={draft.description}
                              onChange={(description) => setDraft({ ...draft, description })}
                              autoSize={{ minRows: 2, maxRows: 4 }}
                            />
                          </label>
                        </div>
                      </section>
                    ) : null}

                    <section className='border-b border-arco-2 py-22px'>
                      <h2 className='m-0 text-15px font-600 text-t-primary'>
                        {t('settings.expertsSettings.instructions')}
                      </h2>
                      <p className='mb-12px mt-4px text-12px text-t-tertiary'>
                        {t('settings.expertsSettings.instructionsDescription')}
                      </p>
                      <Input.TextArea
                        aria-label={t('settings.expertsSettings.instructions')}
                        value={draft.instructions}
                        onChange={(instructions) => setDraft({ ...draft, instructions })}
                        placeholder={t('settings.expertsSettings.instructionsPlaceholder')}
                        autoSize={{ minRows: 6, maxRows: 12 }}
                        maxLength={16000}
                        showWordLimit
                      />
                    </section>

                    <section className='py-22px'>
                      <div className='flex flex-wrap items-center justify-between gap-8px'>
                        <h2 className='m-0 text-15px font-600 text-t-primary'>
                          {t('settings.expertsSettings.capabilities')}
                        </h2>
                        <div className='flex flex-wrap gap-6px text-11px text-t-tertiary'>
                          <span data-testid='expert-skill-coverage' className='rounded-4px bg-fill-2 px-7px py-3px'>
                            {t('settings.expertsSettings.capabilityCoverage.skills', {
                              selected: draft.skillNames.length,
                              available: availableSkillCount,
                            })}
                          </span>
                          <span data-testid='expert-connector-coverage' className='rounded-4px bg-fill-2 px-7px py-3px'>
                            {t('settings.expertsSettings.capabilityCoverage.connectors', {
                              selected: draft.connectorIds.length,
                              available: availableConnectorCount,
                            })}
                          </span>
                        </div>
                      </div>
                      <p className='mb-12px mt-4px text-12px text-t-tertiary'>
                        {t('settings.expertsSettings.capabilitiesDescription')}
                      </p>
                      <Tabs defaultActiveTab='skills' destroyOnHide={false}>
                        <TabPane
                          key='skills'
                          title={`Skills${draft.skillNames.length ? ` ${draft.skillNames.length}` : ''}`}
                        >
                          <Select
                            aria-label={t('settings.expertsSettings.addSkill')}
                            placeholder={t('settings.expertsSettings.addSkill')}
                            value={undefined}
                            showSearch
                            allowClear
                            className='mb-12px w-260px max-w-full'
                            onChange={(value) =>
                              value && setDraft({ ...draft, skillNames: [...draft.skillNames, value] })
                            }
                          >
                            {availableSkills.map((skill) => (
                              <Select.Option key={skill.name} value={skill.name}>
                                {skill.displayName || skill.name}
                              </Select.Option>
                            ))}
                          </Select>
                          {draft.skillNames.length === 0 ? (
                            <p className='text-12px text-t-tertiary'>{t('settings.expertsSettings.noSkills')}</p>
                          ) : (
                            <div className='divide-y divide-[var(--color-border-2)] border-y border-arco-2'>
                              {draft.skillNames.map((skillName) => {
                                const skill = skills.find((item) => item.name === skillName);
                                return (
                                  <CapabilityRow
                                    key={skillName}
                                    label={skill?.displayName || skillName}
                                    onRemove={() =>
                                      setDraft({
                                        ...draft,
                                        skillNames: draft.skillNames.filter((name) => name !== skillName),
                                      })
                                    }
                                  />
                                );
                              })}
                            </div>
                          )}
                        </TabPane>
                        <TabPane
                          key='connectors'
                          title={`${t('settings.expertsSettings.connectors')}${
                            draft.connectorIds.length ? ` ${draft.connectorIds.length}` : ''
                          }`}
                        >
                          <Select
                            aria-label={t('settings.expertsSettings.addConnector')}
                            placeholder={t('settings.expertsSettings.addConnector')}
                            value={undefined}
                            showSearch
                            allowClear
                            className='mb-12px w-260px max-w-full'
                            onChange={(value) =>
                              value && setDraft({ ...draft, connectorIds: [...draft.connectorIds, value] })
                            }
                          >
                            {availableConnectors.map((connector) => (
                              <Select.Option key={connector.id} value={connector.id}>
                                {connector.displayName || connector.name}
                              </Select.Option>
                            ))}
                          </Select>
                          {draft.connectorIds.length === 0 ? (
                            <p className='text-12px text-t-tertiary'>{t('settings.expertsSettings.noConnectors')}</p>
                          ) : (
                            <div className='divide-y divide-[var(--color-border-2)] border-y border-arco-2'>
                              {draft.connectorIds.map((connectorId) => {
                                const connector = connectors.find((item) => item.id === connectorId);
                                return (
                                  <CapabilityRow
                                    key={connectorId}
                                    label={connector?.displayName || connector?.name || connectorId}
                                    onRemove={() =>
                                      setDraft({
                                        ...draft,
                                        connectorIds: draft.connectorIds.filter((id) => id !== connectorId),
                                      })
                                    }
                                  />
                                );
                              })}
                            </div>
                          )}
                        </TabPane>
                      </Tabs>
                    </section>
                  </>
                )}
              </div>
            </div>
          </Modal>
        );
      })()
    : null;

  return (
    <div data-testid='expert-list-page' className='settings-experts-page flex min-h-0 flex-col gap-24px'>
      {messageContext}
      {withHeader ? (
        <SettingsPageHeader
          data-testid='experts-header'
          title={t('settings.expertsSettings.title')}
          description={t('settings.expertsSettings.description')}
          actions={
            <Button type='primary' onClick={() => setCreateVisible(true)}>
              {t('settings.expertsSettings.addExpert')}
            </Button>
          }
        />
      ) : null}
      <SettingsToolbar className='experts-toolbar'>
        <Select
          aria-label={t('settings.expertsSettings.filter')}
          value={filter}
          onChange={setFilter}
          className='w-170px'
        >
          <Select.Option value='all'>
            {t('settings.expertsSettings.filterAll', { count: profiles.length })}
          </Select.Option>
          <Select.Option value='personal'>
            {t('settings.expertsSettings.filterPersonal', {
              count: profiles.filter((profile) => profile.source === 'user').length,
            })}
          </Select.Option>
          <Select.Option value='builtin'>
            {t('settings.expertsSettings.filterBuiltin', {
              count: profiles.filter((profile) => profile.source !== 'user').length,
            })}
          </Select.Option>
        </Select>
        <Input
          aria-label={t('settings.expertsSettings.search')}
          prefix={<Search size={15} />}
          value={query}
          onChange={setQuery}
          placeholder={t('settings.expertsSettings.searchPlaceholder')}
          className='ml-auto w-260px max-w-full'
          allowClear
        />
      </SettingsToolbar>
      <div className='min-h-0 flex-1 pb-24px'>
        {loading ? (
          <div className='flex min-h-260px items-center justify-center'>
            <Spin />
          </div>
        ) : null}
        {!loading && loadError ? (
          <div className='settings-load-error-panel flex min-h-260px flex-col items-center justify-center gap-12px'>
            <Empty description={loadError} />
            <Button onClick={() => void reloadCatalog()}>{t('common.retry')}</Button>
          </div>
        ) : null}
        {!loading && !loadError && visibleProfiles.length === 0 ? (
          <div className='settings-empty-artwork-panel'>
            <SettingsGeneratedEmptyArtwork id='experts' className='settings-empty-artwork-illustration' />
            <Empty description={t('settings.expertsSettings.noMatches')} />
          </div>
        ) : null}
        {!loading && !loadError ? (
          <div className='w-full'>
            {groupedProfiles.personal.length ? (
              <ExpertGroup
                title={t('settings.expertsSettings.yourExperts')}
                profiles={groupedProfiles.personal}
                onOpen={openProfile}
                onToggle={toggleEnabled}
                pendingProfileName={pendingProfileName}
                availableSkillCount={availableSkillCount}
                availableConnectorCount={availableConnectorCount}
                connectors={connectors}
                usageByName={expertUsage}
              />
            ) : null}
            {groupedProfiles.builtin.length ? (
              <ExpertGroup
                title={t('settings.expertsSettings.builtin')}
                profiles={groupedProfiles.builtin}
                onOpen={openProfile}
                onToggle={toggleEnabled}
                pendingProfileName={pendingProfileName}
                availableSkillCount={availableSkillCount}
                availableConnectorCount={availableConnectorCount}
                connectors={connectors}
                usageByName={expertUsage}
              />
            ) : null}
          </div>
        ) : null}
      </div>
      {detailModal}
      <SynonBiomedExpertProfileModal
        visible={createVisible}
        profile={null}
        onClose={closeCreate}
        onChanged={() => void reloadCatalog()}
      />
    </div>
  );
};

const ExpertGroup: React.FC<{
  title: string;
  profiles: SynonBiomedExpertProfile[];
  onOpen: (profile: SynonBiomedExpertProfile) => void;
  onToggle: (profile: SynonBiomedExpertProfile, enabled: boolean) => void;
  pendingProfileName: string | null;
  availableSkillCount: number;
  availableConnectorCount: number;
  connectors: SynonBiomedMcpServer[];
  usageByName: SynonBiomedExpertUsageByName | null;
}> = ({
  title,
  profiles,
  onOpen,
  onToggle,
  pendingProfileName,
  availableSkillCount,
  availableConnectorCount,
  connectors,
  usageByName,
}) => {
  const { t } = useTranslation();
  return (
    <section className='expert-group pt-18px'>
      <h2 className='mb-6px mt-0 px-8px text-12px font-500 text-t-tertiary'>{title}</h2>
      <div className='expert-grid'>
        {profiles.map((profile) => (
          <div key={profile.name} data-testid={`expert-card-${profile.name}`} className='expert-card group'>
            <SettingsGeneratedArtwork id={resolveExpertArtwork(profile)} className='expert-card__artwork' />
            <button
              type='button'
              className='expert-card__main border-0 bg-transparent text-left focus-visible:outline-2 focus-visible:outline-offset-2'
              onClick={() => onOpen(profile)}
            >
              <span className='expert-card__heading'>
                <span className='settings-list-icon expert-card__icon'>
                  <SettingsGeneratedIcon
                    id={resolveExpertIcon(profile)}
                    className='settings-list-generated-icon expert-card__icon-image'
                  />
                </span>
                <span className='expert-card__title-row'>
                  <span className='expert-card__title'>{profile.displayName}</span>
                  {profile.name === 'OPERON' ? (
                    <span className='expert-card__default-badge'>{t('settings.expertsSettings.default')}</span>
                  ) : null}
                </span>
              </span>
              <span className='expert-card__description' title={profile.description}>
                {compactSettingsDescription(profile.description, {
                  maxLength: 104,
                  stripPrefixes: [profile.displayName, profile.name],
                })}
              </span>
              <span className='expert-card__capabilities'>
                {t('settings.expertsSettings.capabilityCoverage.summary', {
                  skills: profile.skillNames.length,
                  availableSkills: availableSkillCount,
                  connectors: connectorIdsFor(profile, connectors).length,
                  availableConnectors: availableConnectorCount,
                })}
              </span>
            </button>
            <div className='expert-card__footer'>
              <ExpertUsageSummary profileName={profile.name} usageByName={usageByName} />
              <Switch
                className='expert-card__switch shrink-0'
                aria-label={t('settings.expertsSettings.enableNamed', { name: profile.displayName })}
                checked={profile.enabled}
                loading={pendingProfileName === profile.name}
                disabled={profile.source !== 'user' || pendingProfileName !== null}
                onChange={(enabled) => onToggle(profile, enabled)}
              />
            </div>
          </div>
        ))}
      </div>
    </section>
  );
};

const ExpertUsageSummary: React.FC<{
  profileName: string;
  usageByName: SynonBiomedExpertUsageByName | null;
}> = ({ profileName, usageByName }) => {
  const { i18n, t } = useTranslation();
  const usage = usageByName ? findSynonBiomedExpertUsage(usageByName, profileName) : null;
  const formattedLastUsedAt = usage?.lastUsedAt ? formatExpertLastUsedAt(usage.lastUsedAt, i18n.language) : '';

  return (
    <div className='expert-card__usage hidden shrink-0 items-center gap-12px whitespace-nowrap text-11px text-t-tertiary sm:flex'>
      <span data-testid={'expert-usage-count-' + profileName}>
        {usageByName === null
          ? t('settings.expertsSettings.usage.unavailable')
          : t('settings.expertsSettings.usage.count', { count: usage?.invocationCount ?? 0 })}
      </span>
      <span data-testid={'expert-last-used-' + profileName}>
        {usageByName === null
          ? t('settings.expertsSettings.usage.unavailable')
          : formattedLastUsedAt
            ? t('settings.expertsSettings.usage.lastUsed', { time: formattedLastUsedAt })
            : t('settings.expertsSettings.usage.never')}
      </span>
    </div>
  );
};

function formatExpertLastUsedAt(value: string, locale: string | undefined): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return new Intl.DateTimeFormat(locale || undefined, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date);
}

const CapabilityRow: React.FC<{ label: string; onRemove: () => void }> = ({ label, onRemove }) => {
  const { t } = useTranslation();
  return (
    <div className='flex min-h-42px items-center gap-10px py-7px'>
      <span className='min-w-0 flex-1 truncate text-13px text-t-primary'>{label}</span>
      <Button
        aria-label={t('settings.expertsSettings.removeNamed', { name: label })}
        type='text'
        icon={<Close size={14} />}
        onClick={onRemove}
      />
    </div>
  );
};

export default ExpertWorkbench;

/** Header-less, wrapper-less variant used inside the merged library page. */
export const ExpertWorkbenchContent: React.FC = () => <ExpertWorkbench withHeader={false} />;
