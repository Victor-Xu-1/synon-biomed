import { Input, Message, Modal, Switch } from '@arco-design/web-react';
import { Delete, Refresh, Right, Up } from '@icon-park/react';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  addSynonBiomedAllowedDomain,
  loadSynonBiomedNetworkSettings,
  removeSynonBiomedAllowedDomain,
  replaceSynonBiomedAllowedDomains,
  updateSynonBiomedAllowlistGroups,
  type SynonBiomedAllowlistGroup,
  type SynonBiomedNetworkSettings,
} from '@/renderer/services/synonBiomedWorkspaceSettings';
import SettingsPageHeader from './components/SettingsPageHeader';
import SettingsPageWrapper from './components/SettingsPageWrapper';
import {
  SettingsGeneratedArtwork,
  SettingsGeneratedIcon,
  type SettingsGeneratedIconId,
} from './components/SettingsGeneratedAsset';
import { EmptyText, RefreshButton } from './components/SettingsPrimitives';

const LOCALIZED_NETWORK_GROUP_IDS = new Set(['pkg', 'nih', 'genomics', 'proteomics', 'literature', 'clinical']);

export const NetworkSettingsContent: React.FC = () => {
  const { t } = useTranslation();
  const [snapshot, setSnapshot] = useState<SynonBiomedNetworkSettings | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);
  const [pendingGroup, setPendingGroup] = useState<string | null>(null);
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(() => new Set());
  const [addVisible, setAddVisible] = useState(false);
  const [domainToRemove, setDomainToRemove] = useState<string | null>(null);
  const [clearVisible, setClearVisible] = useState(false);
  const [domain, setDomain] = useState('');
  const [savingDomain, setSavingDomain] = useState(false);

  const refresh = useCallback(async () => {
    setLoading(true);
    setLoadFailed(false);
    try {
      setSnapshot(await loadSynonBiomedNetworkSettings());
    } catch (error) {
      console.error('Failed to load network settings:', error);
      setLoadFailed(true);
      Message.error(t('settings.networkSettings.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => void refresh(), [refresh]);

  const customDomains = snapshot?.domains ?? [];
  const customDomainSet = useMemo(() => new Set(customDomains), [customDomains]);

  const toggleGroup = async (groupId: string, enabled: boolean) => {
    if (!snapshot || pendingGroup) return;
    const previous = snapshot.disabledGroups;
    const disabledGroups = enabled ? previous.filter((id) => id !== groupId) : [...new Set([...previous, groupId])];
    setSnapshot({ ...snapshot, disabledGroups });
    setPendingGroup(groupId);
    try {
      await updateSynonBiomedAllowlistGroups(disabledGroups);
      Message.success(
        enabled ? t('settings.networkSettings.groupEnabled') : t('settings.networkSettings.groupDisabled')
      );
    } catch (error) {
      setSnapshot({ ...snapshot, disabledGroups: previous });
      console.error('Failed to update network group:', error);
      Message.error(t('settings.networkSettings.groupUpdateFailed'));
    } finally {
      setPendingGroup(null);
    }
  };

  const toggleExpanded = (groupId: string) => {
    setExpandedGroups((current) => {
      const next = new Set(current);
      if (next.has(groupId)) next.delete(groupId);
      else next.add(groupId);
      return next;
    });
  };

  const addDomain = async () => {
    const value = domain.trim();
    if (!value || savingDomain) return;
    if (customDomainSet.has(value)) {
      Message.info(t('settings.networkSettings.domainAlreadyAllowed'));
      return;
    }
    setSavingDomain(true);
    try {
      await addSynonBiomedAllowedDomain(value);
      setDomain('');
      setAddVisible(false);
      await refresh();
      Message.success(t('settings.networkSettings.domainAdded'));
    } catch (error) {
      console.error('Failed to add allowed domain:', error);
      Message.error(t('settings.networkSettings.domainAddFailed'));
    } finally {
      setSavingDomain(false);
    }
  };

  const confirmRemoveDomain = async () => {
    if (!domainToRemove || savingDomain) return;
    setSavingDomain(true);
    try {
      await removeSynonBiomedAllowedDomain(domainToRemove);
      setDomainToRemove(null);
      await refresh();
      Message.success(t('settings.networkSettings.domainRemoved'));
    } catch (error) {
      console.error('Failed to remove allowed domain:', error);
      Message.error(t('settings.networkSettings.domainRemoveFailed'));
    } finally {
      setSavingDomain(false);
    }
  };

  const confirmClearDomains = async () => {
    if (savingDomain) return;
    setSavingDomain(true);
    try {
      await replaceSynonBiomedAllowedDomains([]);
      setClearVisible(false);
      await refresh();
      Message.success(t('settings.networkSettings.domainsCleared'));
    } catch (error) {
      console.error('Failed to clear allowed domains:', error);
      Message.error(t('settings.networkSettings.domainsClearFailed'));
    } finally {
      setSavingDomain(false);
    }
  };

  return (
    <div className='settings-network-page flex flex-col' data-testid='synon-network-settings'>
      <SettingsPageHeader
        title={t('settings.networkSettings.title')}
        description={t('settings.networkSettings.description')}
        actions={<RefreshButton loading={loading} onClick={refresh} />}
      />

      {loadFailed && !snapshot ? (
        <div className='settings-load-error-panel mt-24px' role='alert'>
          <p className='m-0 text-13px font-600 text-t-primary'>{t('settings.networkSettings.loadErrorTitle')}</p>
          <p className='m-0 mt-5px text-12px text-t-tertiary'>{t('settings.networkSettings.loadFailed')}</p>
          <button type='button' className='settings-action-button mt-12px' onClick={() => void refresh()}>
            <Refresh size='14' /> {t('settings.networkSettings.reload')}
          </button>
        </div>
      ) : (
        <>
          <NetworkSection
            title={t('settings.networkSettings.presetGroups')}
            icon='network'
            className='network-section--preset-groups'
          >
            <div className='divide-y divide-[var(--color-border-1)]' data-testid='builtin-network-groups'>
              {(snapshot?.groups ?? []).map((group) => (
                <NetworkGroupRow
                  key={group.id}
                  group={group}
                  enabled={group.locked || !snapshot?.disabledGroups.includes(group.id)}
                  expanded={expandedGroups.has(group.id)}
                  pending={pendingGroup === group.id}
                  disabled={loading || (pendingGroup !== null && pendingGroup !== group.id)}
                  onToggle={(enabled) => void toggleGroup(group.id, enabled)}
                  onExpand={() => toggleExpanded(group.id)}
                />
              ))}
              {loading && !snapshot ? <NetworkRowsSkeleton /> : null}
              {!loading && snapshot?.groups.length === 0 ? (
                <EmptyText>
                  <SettingsGeneratedIcon id='network' className='settings-empty-artwork-icon' />
                  {t('settings.networkSettings.builtinEmpty')}
                </EmptyText>
              ) : null}
            </div>
          </NetworkSection>

          <NetworkSection
            title={t('settings.networkSettings.allowedDomains')}
            description={t('settings.networkSettings.allowedDomainsDescription')}
            icon='network'
            className='network-section--allowed'
            artwork={<SettingsGeneratedArtwork id='network-globe' className='network-section__artwork' />}
            actions={
              <div className='flex items-center gap-4px'>
                <button
                  type='button'
                  className='settings-text-danger-button'
                  disabled={customDomains.length === 0 || loading}
                  onClick={() => setClearVisible(true)}
                >
                  {t('settings.networkSettings.clearAll')}
                </button>
                <button type='button' className='settings-action-button' onClick={() => setAddVisible(true)}>
                  {t('settings.networkSettings.addDomain')}
                </button>
              </div>
            }
          >
            <div className='divide-y divide-[var(--color-border-1)]' data-testid='allowed-domain-list'>
              {customDomains.map((item) => (
                <div
                  key={item}
                  data-testid={`allowed-domain-${item}`}
                  className='group flex min-h-42px items-center gap-10px py-7px'
                >
                  <SettingsGeneratedIcon id='network' className='allowed-domain__icon' />
                  <span className='min-w-0 flex-1 break-all font-mono text-12px text-t-secondary'>{item}</span>
                  <button
                    type='button'
                    className='settings-icon-button shrink-0 opacity-70 hover:opacity-100 focus:opacity-100'
                    title={t('settings.networkSettings.removeNamed', { domain: item })}
                    aria-label={t('settings.networkSettings.removeNamed', { domain: item })}
                    onClick={() => setDomainToRemove(item)}
                  >
                    <Delete size='14' />
                  </button>
                </div>
              ))}
              {!loading && customDomains.length === 0 ? (
                <EmptyText>
                  <SettingsGeneratedIcon id='network' className='settings-empty-artwork-icon' />
                  {t('settings.networkSettings.customEmpty')}
                </EmptyText>
              ) : null}
            </div>
          </NetworkSection>

          {(snapshot?.configDomains.length ?? 0) > 0 ? (
            <NetworkDisclosure
              title={t('settings.networkSettings.configDomains')}
              description={t('settings.networkSettings.configDomainsDescription')}
              domains={snapshot?.configDomains ?? []}
            />
          ) : null}

          {(snapshot?.deniedDomains.length ?? 0) > 0 ? (
            <NetworkDisclosure
              title={t('settings.networkSettings.deniedDomains')}
              description={t('settings.networkSettings.deniedDomainsDescription')}
              domains={snapshot?.deniedDomains ?? []}
              muted
            />
          ) : null}
        </>
      )}

      <Modal
        title={t('settings.networkSettings.addModalTitle')}
        visible={addVisible}
        onCancel={() => {
          if (!savingDomain) {
            setAddVisible(false);
            setDomain('');
          }
        }}
        onOk={() => void addDomain()}
        confirmLoading={savingDomain}
        okButtonProps={{ disabled: !domain.trim() }}
        unmountOnExit
      >
        <label className='flex flex-col gap-7px text-12px text-t-secondary'>
          {t('settings.networkSettings.domainLabel')}
          <Input
            data-testid='allowed-domain-input'
            autoFocus
            value={domain}
            onChange={setDomain}
            placeholder={t('settings.networkSettings.domainPlaceholder')}
            onPressEnter={() => void addDomain()}
          />
          <span className='text-11px text-t-tertiary'>{t('settings.networkSettings.domainHint')}</span>
        </label>
      </Modal>

      <Modal
        title={t('settings.networkSettings.removeModalTitle')}
        visible={domainToRemove !== null}
        onCancel={() => !savingDomain && setDomainToRemove(null)}
        onOk={() => void confirmRemoveDomain()}
        confirmLoading={savingDomain}
        okButtonProps={{ status: 'danger' }}
        unmountOnExit
      >
        <p className='m-0 text-13px text-t-secondary break-all'>
          {t('settings.networkSettings.removeConfirm', { domain: domainToRemove })}
        </p>
      </Modal>

      <Modal
        title={t('settings.networkSettings.clearModalTitle')}
        visible={clearVisible}
        onCancel={() => !savingDomain && setClearVisible(false)}
        onOk={() => void confirmClearDomains()}
        confirmLoading={savingDomain}
        okButtonProps={{ status: 'danger' }}
        unmountOnExit
      >
        <p className='m-0 text-13px text-t-secondary'>
          {t('settings.networkSettings.clearConfirm', { count: customDomains.length })}
        </p>
      </Modal>
    </div>
  );
};

const NetworkSettings: React.FC = () => (
  <SettingsPageWrapper>
    <NetworkSettingsContent />
  </SettingsPageWrapper>
);

const NetworkGroupRow: React.FC<{
  group: SynonBiomedAllowlistGroup;
  enabled: boolean;
  expanded: boolean;
  pending: boolean;
  disabled: boolean;
  onToggle: (enabled: boolean) => void;
  onExpand: () => void;
}> = ({ group, enabled, expanded, pending, disabled, onToggle, onExpand }) => {
  const { t } = useTranslation();
  const hasLocalizedCopy = LOCALIZED_NETWORK_GROUP_IDS.has(group.id);
  const copy = {
    label: hasLocalizedCopy ? t(`settings.networkSettings.groups.${group.id}.label`) : group.label,
    description: hasLocalizedCopy ? t(`settings.networkSettings.groups.${group.id}.description`) : group.description,
  };
  const icon: SettingsGeneratedIconId =
    group.id === 'pkg'
      ? 'connector-database'
      : group.id === 'genomics'
        ? 'connector-dna'
        : group.id === 'proteomics'
          ? 'connector-protein'
          : group.id === 'clinical'
            ? 'connector-clipboard'
            : group.id === 'literature'
              ? 'connector-book'
              : 'connector-regulation';
  return (
    <div data-testid={`network-group-${group.id}`}>
      <div className='network-group-row flex min-h-54px items-center gap-10px py-7px'>
        <button
          type='button'
          className='min-w-0 flex-1 flex items-center gap-9px text-left border-0 bg-transparent'
          aria-expanded={expanded}
          aria-controls={`network-group-domains-${group.id}`}
          onClick={onExpand}
        >
          <SettingsGeneratedIcon id={icon} className='network-group-row__icon' />
          <span className='network-group-row__chevron w-16px shrink-0 text-t-tertiary'>
            {expanded ? <Up size='13' /> : <Right size='13' />}
          </span>
          <span className='min-w-0 flex-1'>
            <span className='flex flex-wrap items-center gap-7px text-14px font-600 text-t-primary'>
              {copy.label}
              <span className='text-11px font-500 text-t-tertiary'>{group.domains.length}</span>
              {group.locked ? (
                <span className='text-10px font-500 text-t-tertiary'>{t('settings.networkSettings.required')}</span>
              ) : null}
            </span>
            <span className='block text-12px text-t-tertiary mt-3px truncate'>{copy.description}</span>
          </span>
        </button>
        <Switch
          size='small'
          checked={enabled}
          loading={pending}
          disabled={group.locked || disabled}
          aria-label={t('settings.networkSettings.enableGroup', { name: copy.label })}
          onChange={onToggle}
        />
      </div>
      {expanded ? (
        <div id={`network-group-domains-${group.id}`} className='pb-13px pl-25px'>
          <DomainGrid domains={group.domains} muted={!enabled} />
        </div>
      ) : null}
    </div>
  );
};

const DomainGrid: React.FC<{ domains: string[]; muted?: boolean }> = ({ domains, muted }) => (
  <div className='grid grid-cols-1 sm:grid-cols-2 gap-x-18px gap-y-2px'>
    {domains.map((item) => (
      <code
        key={item}
        className={`min-w-0 break-all py-3px text-11px leading-18px ${muted ? 'text-t-quaternary' : 'text-t-secondary'}`}
      >
        {item}
      </code>
    ))}
  </div>
);

const NetworkSection: React.FC<{
  title: string;
  description?: string;
  actions?: React.ReactNode;
  className?: string;
  children: React.ReactNode;
  icon?: SettingsGeneratedIconId;
  artwork?: React.ReactNode;
}> = ({ title, description, actions, className = '', children, icon, artwork }) => (
  <section className={className}>
    <div className='network-section__heading flex flex-col gap-8px sm:flex-row sm:items-end sm:justify-between'>
      <div className='settings-section__heading min-w-0'>
        {icon ? <SettingsGeneratedIcon id={icon} className='settings-section__icon' /> : null}
        <div className='min-w-0'>
          <h2 className='m-0 text-14px font-650 text-t-primary'>{title}</h2>
          {description ? <p className='m-0 mt-4px text-12px leading-18px text-t-tertiary'>{description}</p> : null}
        </div>
      </div>
      {actions ? <div className='shrink-0'>{actions}</div> : null}
    </div>
    {children}
    {artwork}
  </section>
);

const NetworkDisclosure: React.FC<{
  title: string;
  description: string;
  domains: string[];
  muted?: boolean;
}> = ({ title, description, domains, muted }) => {
  const { t } = useTranslation();
  return (
    <details className='mt-24px border-t border-arco-2 py-14px'>
      <summary className='cursor-pointer list-none select-none [&::-webkit-details-marker]:hidden'>
        <span className='flex items-center justify-between gap-12px'>
          <span className='min-w-0'>
            <span className='block text-13px font-600 text-t-primary'>{title}</span>
            <span className='mt-3px block text-11px leading-17px text-t-tertiary'>{description}</span>
          </span>
          <span className='shrink-0 text-11px text-t-tertiary'>
            {t('settings.networkSettings.viewDomains', { count: domains.length })}
          </span>
        </span>
      </summary>
      <div className='mt-10px pl-2px'>
        <DomainGrid domains={domains} muted={muted} />
      </div>
    </details>
  );
};

const NetworkRowsSkeleton: React.FC = () => {
  const { t } = useTranslation();
  return (
    <div aria-label={t('settings.networkSettings.loadingGroups')} className='py-3px'>
      {[0, 1, 2, 3].map((item) => (
        <div key={item} className='flex h-54px items-center gap-10px border-b border-arco-1 last:border-b-0'>
          <span className='h-12px w-12px animate-pulse rounded-2px bg-fill-2' />
          <span className='min-w-0 flex-1'>
            <span className='block h-12px w-120px animate-pulse rounded-3px bg-fill-2' />
            <span className='mt-7px block h-9px w-260px max-w-70% animate-pulse rounded-3px bg-fill-1' />
          </span>
          <span className='h-18px w-32px animate-pulse rounded-10px bg-fill-2' />
        </div>
      ))}
    </div>
  );
};

export default NetworkSettings;
