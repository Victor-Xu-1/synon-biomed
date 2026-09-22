import { Button, Modal } from '@arco-design/web-react';
import { Down, Refresh, Search } from '@icon-park/react';
import React, { useEffect, useId, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  loadScientificRuntimeSettings,
  pauseScientificRuntime,
  saveScientificRuntimeSelection,
  retryScientificRuntime,
  uninstallScientificRuntime,
  type ScientificRuntimeOption,
} from '@/renderer/services/scientificRuntimeSettings';
import { scientificRuntimePresentation } from '@/renderer/utils/scientificRuntimePresentation';
import SettingsPageWrapper from './components/SettingsPageWrapper';
import SettingsPageHeader from './components/SettingsPageHeader';
import SettingsLibraryTabHeader from './components/SettingsLibraryTabHeader';
import SettingsLibraryFilterSelect from './components/SettingsLibraryFilterSelect';
import { RefreshButton } from './components/SettingsPrimitives';
import { useStorageResource } from './storage/useStorageResource';
import { StorageRuntimeSelectionDialog } from './storage/StorageRuntimeSelectionDialog';
import { StorageError, StorageLoading } from './storage/StorageFeedback';
import { EnvironmentCard } from './environments/EnvironmentCard';

import {
  activeEnvironmentStates,
  environmentCategories,
  environmentCategory,
  matchesEnvironmentFilter,
  type EnvironmentCategory,
  type EnvironmentFilter,
} from './environments/environmentCatalog';
import './environments/environments.css';

const load = (signal: AbortSignal) => loadScientificRuntimeSettings({ signal });
interface ScientificEnvironmentSettingsProps {
  /** When false, renders without SettingsPageWrapper for route/tab embedding. */
  withWrapper?: boolean;
  /** When false, omits the page-level header (used inside the merged library page). */
  withHeader?: boolean;
  /** When true (with withHeader=false), renders the merged-page one-line compact header. */
  compactHeader?: boolean;
  /** When true, renders the refresh/manage actions row inside the content instead of the header. */
  showActions?: boolean;
}

export default function ScientificEnvironmentSettings({
  withWrapper = true,
  withHeader = true,
  compactHeader = false,
  showActions = false,
}: ScientificEnvironmentSettingsProps) {
  const { t } = useTranslation();
  const resource = useStorageResource(load);
  const [query, setQuery] = useState('');
  const [category, setCategory] = useState<EnvironmentCategory>('all');
  const [filter, setFilter] = useState<EnvironmentFilter>('all');
  const [selection, setSelection] = useState(false);
  const [filtersExpanded, setFiltersExpanded] = useState(false);
  const [target, setTarget] = useState<ScientificRuntimeOption | null>(null);
  const [uninstallTarget, setUninstallTarget] = useState<ScientificRuntimeOption | null>(null);
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  const pending = useRef(false);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const items = resource.data?.options ?? [];
  const active = items.some((item) => activeEnvironmentStates.has(item.status));
  useEffect(() => {
    if (!active || resource.loading || resource.failed) return;
    const timer = window.setTimeout(() => {
      void resource.refresh(false);
    }, 3000);
    return () => window.clearTimeout(timer);
  }, [active, resource.loading, resource.failed, resource.data, resource.refresh]);
  const filtered = items.filter((item) => {
    const presentation = scientificRuntimePresentation(item.id, t);
    const haystack = [
      item.id,
      presentation.title,
      presentation.description,
      ...(item.packages ?? []).map((pkg) => pkg.spec),
    ]
      .join(' ')
      .toLocaleLowerCase();
    return (
      (category === 'all' || environmentCategory(item.id) === category) &&
      matchesEnvironmentFilter(item, filter) &&
      haystack.includes(query.trim().toLocaleLowerCase())
    );
  });
  const environmentPageItems = filtered;
  const filterPanelId = useId();
  const activeFilterCount = filter === 'all' ? 0 : 1;
  // The merged-page header keeps exactly one control: the research-domain
  // filter, with the inventory count of each domain it can select.
  const environmentCategoryOptions = environmentCategories
    .filter((id) => id === 'all' || items.some((item) => environmentCategory(item.id) === id))
    .map((id) => ({
      id,
      count: id === 'all' ? items.length : items.filter((item) => environmentCategory(item.id) === id).length,
    }));
  const prepare = async () => {
    if (!target || pending.current) return;
    pending.current = true;
    setBusy(true);
    setFailed(false);
    try {
      // Read the current shared selection immediately before adding one item.
      const current = await loadScientificRuntimeSettings();
      if (!alive.current) return;
      const selected = current.options.find((item) => item.id === target.id);
      if (!selected?.available) throw new Error('Runtime unavailable');
      if (!selected.selected) {
        await saveScientificRuntimeSelection(
          Object.fromEntries(current.options.map((item) => [item.id, item.selected || item.id === target.id]))
        );
      } else {
        await retryScientificRuntime(target.id);
      }
      if (alive.current) {
        setTarget(null);
        await resource.refresh(false);
      }
    } catch {
      if (alive.current) setFailed(true);
    } finally {
      pending.current = false;
      if (alive.current) setBusy(false);
    }
  };
  const pause = async (item: ScientificRuntimeOption) => {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    setFailed(false);
    try {
      await pauseScientificRuntime(item.id);
      if (alive.current) await resource.refresh(false);
    } catch {
      if (alive.current) {
        setFailed(true);
        setTarget(item);
      }
    } finally {
      pending.current = false;
      if (alive.current) setBusy(false);
    }
  };
  const uninstall = async () => {
    if (!uninstallTarget || pending.current) return;
    pending.current = true;
    setBusy(true);
    setFailed(false);
    try {
      await uninstallScientificRuntime(uninstallTarget.id);
      if (alive.current) {
        setUninstallTarget(null);
        await resource.refresh(false);
      }
    } catch {
      if (alive.current) setFailed(true);
    } finally {
      pending.current = false;
      if (alive.current) setBusy(false);
    }
  };
  const environmentActions = (
    <>
      <RefreshButton loading={resource.loading} onClick={() => resource.refresh(false)} />
      <button
        type='button'
        className='settings-action-button'
        disabled={!resource.data || resource.failed || busy}
        onClick={() => setSelection(true)}
      >
        {t('settings.environments.manageSelection')}
      </button>
    </>
  );
  // The merged library page keeps exactly one primary action per tab header.
  const compactRefreshAction = (
    <Button
      type='primary'
      loading={resource.loading}
      disabled={resource.failed}
      icon={<Refresh size='14' />}
      onClick={() => resource.refresh(false)}
    >
      {t('common.refresh')}
    </Button>
  );

  const content = (
    <>
      {withHeader ? (
        <SettingsPageHeader
          title={t('settings.environments.title')}
          description={t('settings.environments.description')}
          actions={environmentActions}
        />
      ) : compactHeader ? (
        <SettingsLibraryTabHeader
          title={t('settings.environments.title')}
          count={items.length}
          filters={
            <SettingsLibraryFilterSelect
              aria-label={t('settings.environments.category')}
              data-testid='environment-category-filter'
              value={category}
              onChange={(value) => setCategory(value as EnvironmentCategory)}
            >
              {environmentCategoryOptions.map((option) => (
                <option key={option.id} value={option.id}>
                  {t('settings.environments.categories.' + option.id)} ({option.count})
                </option>
              ))}
            </SettingsLibraryFilterSelect>
          }
          actions={compactRefreshAction}
        />
      ) : null}
      <div className='environment-library' data-testid='scientific-environments'>
        {showActions ? (
          <div className='environment-content-actions mb-12px flex items-center gap-8px'>{environmentActions}</div>
        ) : null}
        {!compactHeader ? (
          <>
            <div className='environment-toolbar' role='search' aria-label={t('settings.environments.title')}>
              <div className='environment-search'>
                <Search size={15} aria-hidden='true' />
                <input
                  type='search'
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  aria-label={t('settings.environments.search')}
                  placeholder={t('settings.environments.search')}
                />
              </div>
              <select
                value={category}
                onChange={(event) => setCategory(event.target.value as EnvironmentCategory)}
                aria-label={t('settings.environments.category')}
              >
                {environmentCategories
                  .filter((id) => id === 'all' || items.some((item) => environmentCategory(item.id) === id))
                  .map((id) => (
                    <option key={id} value={id}>
                      {t('settings.environments.categories.' + id)}
                    </option>
                  ))}
              </select>
              <button
                type='button'
                className='environment-filter-toggle'
                aria-expanded={filtersExpanded}
                aria-controls={filterPanelId}
                onClick={() => setFiltersExpanded((value) => !value)}
              >
                {t('settings.skillsSettings.filters')}
                {activeFilterCount > 0 ? <span className='environment-filter-count'>{activeFilterCount}</span> : null}
                <Down size={14} aria-hidden='true' />
              </button>
            </div>
            {filtersExpanded && (
              <div
                id={filterPanelId}
                className='environment-filter-panel'
                role='group'
                aria-label={t('settings.skillsSettings.filters')}
              >
                <label>
                  <span>{t('settings.environments.status')}</span>
                  <select
                    value={filter}
                    onChange={(event) => setFilter(event.target.value as EnvironmentFilter)}
                    aria-label={t('settings.environments.status')}
                  >
                    {(['all', 'ready', 'available', 'active', 'failed'] as const).map((id) => (
                      <option key={id} value={id}>
                        {t('settings.environments.filters.' + id)}
                      </option>
                    ))}
                  </select>
                </label>
                <button
                  type='button'
                  className='environment-filter-reset'
                  disabled={activeFilterCount === 0}
                  onClick={() => setFilter('all')}
                >
                  {t('settings.skillsSettings.resetFilters')}
                </button>
              </div>
            )}
            <p className='environment-notice'>{t('settings.environments.notice')}</p>
          </>
        ) : null}
        {resource.loading && !resource.data && <StorageLoading />}
        {resource.failed && <StorageError retained={!!resource.data} onRetry={() => void resource.refresh(false)} />}
        {resource.data && (
          <>
            {!compactHeader ? (
              <div className='environment-results' role='status'>
                {t('settings.environments.results', { count: filtered.length, total: items.length })}
              </div>
            ) : null}
            <div className='environment-grid' role='list'>
              {environmentPageItems.map((item) => (
                <EnvironmentCard
                  key={item.id}
                  item={item}
                  disabled={busy || resource.failed}
                  onPrepare={(value) => {
                    setFailed(false);
                    setTarget(value);
                  }}
                  onPause={(value) => void pause(value)}
                  onUninstall={(value) => {
                    setFailed(false);
                    setUninstallTarget(value);
                  }}
                />
              ))}
            </div>
            {!filtered.length && <div className='environment-empty'>{t('settings.environments.empty')}</div>}
          </>
        )}
        {selection && resource.data && (
          <StorageRuntimeSelectionDialog
            items={items}
            onClose={() => setSelection(false)}
            onSaved={() => {
              setSelection(false);
              void resource.refresh(false);
            }}
          />
        )}
        {target && (
          <Modal
            visible
            title={t('settings.environments.confirmTitle')}
            onOk={() => void prepare()}
            onCancel={() => {
              if (!pending.current) setTarget(null);
            }}
            confirmLoading={busy}
            closable={!busy}
            maskClosable={!busy}
            escToExit={!busy}
            cancelButtonProps={{ disabled: busy }}
            okText={t('settings.environments.confirmDownload')}
          >
            <p>{scientificRuntimePresentation(target.id, t).title}</p>
            <p>{t('settings.environments.confirmHint', { value: target.estimatedInstallMB })}</p>
            {failed && <p role='alert'>{t('settings.storageSettings.softwareRetryFailed')}</p>}
          </Modal>
        )}
        {uninstallTarget && (
          <Modal
            visible
            title={t('settings.environments.uninstallTitle')}
            onOk={() => void uninstall()}
            onCancel={() => {
              if (!pending.current) setUninstallTarget(null);
            }}
            confirmLoading={busy}
            closable={!busy}
            maskClosable={!busy}
            escToExit={!busy}
            cancelButtonProps={{ disabled: busy }}
            okButtonProps={{ status: 'danger' }}
            okText={t('settings.environments.confirmUninstall')}
          >
            <p>{scientificRuntimePresentation(uninstallTarget.id, t).title}</p>
            <p>{t('settings.environments.uninstallHint')}</p>
            {failed && <p role='alert'>{t('settings.storageSettings.softwareRetryFailed')}</p>}
          </Modal>
        )}
      </div>
    </>
  );

  if (!withWrapper) return content;
  return <SettingsPageWrapper>{content}</SettingsPageWrapper>;
}

/** Header-less, wrapper-less content used by the merged library page tabs. */
export const ScientificEnvironmentSettingsContent: React.FC<{ showActions?: boolean }> = ({ showActions = false }) => (
  <ScientificEnvironmentSettings withWrapper={false} withHeader={false} compactHeader showActions={showActions} />
);
