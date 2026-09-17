import { Button, Dropdown, Empty, Input, Menu, Message, Modal, Select, Spin, Tabs } from '@arco-design/web-react';
import { Delete, Edit, MoreOne, Refresh } from '@icon-park/react';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  attachSynonBiomedMcpServerToAllAgents,
  authorizeSynonBiomedMcpConnector,
  configureSynonBiomedMcpConnectorAPIKey,
  createSynonBiomedCustomMcpServer,
  deleteSynonBiomedCustomMcpServer,
  detachSynonBiomedMcpServerFromAllAgents,
  disconnectSynonBiomedMcpConnector,
  loadSynonBiomedCustomMcpServers,
  loadSynonBiomedMcpDirectoryHealth,
  loadSynonBiomedMcpServers,
  reconcileSynonBiomedMcpServers,
  setSynonBiomedMcpConnectorEnabled,
  updateSynonBiomedCustomMcpServer,
  type SynonBiomedCustomMcpServer,
  type SynonBiomedCustomMcpServerInput,
  type SynonBiomedMcpDirectoryHealth,
  type SynonBiomedMcpServer,
  type SynonBiomedMcpUsage,
} from '@/renderer/services/synonBiomedCapabilities';
import { McpConnectorEditorModal } from './McpConnectorEditorModal';
import { McpMarketplacePanel } from './McpMarketplacePanel';
import { McpOptionalPanel } from './McpOptionalPanel';
import { McpPermissionsModal } from './McpPermissionsModal';
import { SettingsGeneratedIcon } from '../components/SettingsGeneratedAsset';
import { compactSettingsDescription } from '../components/settingsPresentation';
import { resolveSynonBiomedMcpDescription } from '@/renderer/services/mcp/synonBiomedMcpDescriptions';
import { McpConnectorVisualMark, resolveMcpConnectorVisual } from './mcpConnectorVisuals';
import SettingsPagination from '../components/SettingsPagination';

type ConnectorFilter = 'all' | 'connected' | 'needs-attention' | 'custom';

// Keep one review page dense enough for the approved desktop sheet (12 cards).
const MCP_PAGE_SIZE = 12;

const removeDescriptionTerminalPunctuation = (value: string | undefined): string | undefined =>
  value
    ?.trim()
    .replace(/[。！？!?；;，,、:：.]+$/u, '')
    .trim();

const ignoreHandledMutationError = (_error: unknown): undefined => undefined;

export const SynonBiomedMcpSettingsContent: React.FC = () => {
  const { t } = useTranslation();
  const [servers, setServers] = useState<SynonBiomedMcpServer[]>([]);
  const [customServers, setCustomServers] = useState<SynonBiomedCustomMcpServer[]>([]);
  const [directoryHealth, setDirectoryHealth] = useState<SynonBiomedMcpDirectoryHealth | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const [reconciling, setReconciling] = useState(false);
  const [mutatingServerId, setMutatingServerId] = useState<string | null>(null);
  const [permissionServer, setPermissionServer] = useState<SynonBiomedMcpServer | null>(null);
  const [credentialServer, setCredentialServer] = useState<SynonBiomedMcpServer | null>(null);
  const [credentialValue, setCredentialValue] = useState('');
  const [credentialSaving, setCredentialSaving] = useState(false);
  const [editorVisible, setEditorVisible] = useState(false);
  const [editingServer, setEditingServer] = useState<SynonBiomedCustomMcpServer | null>(null);
  const [search, setSearch] = useState('');
  const [filter, setFilter] = useState<ConnectorFilter>('all');
  const [mcpPage, setMcpPage] = useState(1);
  const [activeView, setActiveView] = useState<'installed' | 'optional' | 'marketplace'>('installed');
  const loadGenerationRef = useRef(0);
  const initializationPollsRef = useRef(0);

  const customById = useMemo(() => new Map(customServers.map((server) => [server.id, server])), [customServers]);

  const loadServers = useCallback(async () => {
    const generation = ++loadGenerationRef.current;
    setLoading(true);
    setLoadError(false);
    const [serversResult, customResult, healthResult] = await Promise.allSettled([
      loadSynonBiomedMcpServers(),
      loadSynonBiomedCustomMcpServers(),
      loadSynonBiomedMcpDirectoryHealth(),
    ]);
    if (generation !== loadGenerationRef.current) return;
    let failed = false;
    if (serversResult.status === 'fulfilled') setServers(serversResult.value);
    else {
      failed = true;
      console.error('Failed to load Synon Biomed MCP connectors:', serversResult.reason);
    }

    if (customResult.status === 'fulfilled') setCustomServers(customResult.value);
    else {
      failed = true;
      console.error('Failed to load Synon Biomed MCP custom connectors:', customResult.reason);
    }

    if (healthResult.status === 'fulfilled') setDirectoryHealth(healthResult.value);
    else {
      failed = true;
      setDirectoryHealth(null);
      console.error('Failed to load Synon Biomed MCP directory health:', healthResult.reason);
    }
    if (failed) {
      setLoadError(true);
      Message.error(t('settings.synonBiomedMcpFetchError'));
    }
    setLoading(false);
  }, [t]);

  useEffect(() => {
    void loadServers();
    return () => {
      loadGenerationRef.current += 1;
    };
  }, [loadServers]);

  const hasInitializingBundledConnector = servers.some(
    (server) => server.source === 'bundled' && server.enabled && server.connectionStatus === 'connecting'
  );

  useEffect(() => {
    if (!hasInitializingBundledConnector) {
      initializationPollsRef.current = 0;
      return;
    }
    if (loading || loadError || initializationPollsRef.current >= 60) return;
    const timer = window.setTimeout(() => {
      initializationPollsRef.current += 1;
      void loadServers();
    }, 1000);
    return () => window.clearTimeout(timer);
  }, [hasInitializingBundledConnector, loadError, loadServers, loading]);

  const reconcile = useCallback(async () => {
    setReconciling(true);
    try {
      await reconcileSynonBiomedMcpServers();
      Message.success(t('settings.synonBiomedMcpReconcileSuccess'));
      await loadServers();
    } catch (error) {
      console.error('Failed to reconcile Synon Biomed MCP connectors:', error);
      Message.error(t('settings.synonBiomedMcpReconcileError'));
    } finally {
      setReconciling(false);
    }
  }, [loadServers, t]);

  const mutate = useCallback(
    async (serverId: string, action: () => Promise<unknown>, success?: string) => {
      setMutatingServerId(serverId);
      try {
        await action();
        if (success) Message.success(success);
        await loadServers();
      } catch (error) {
        console.error('Failed to update Synon Biomed MCP connector:', error);
        Message.error(t('settings.synonBiomedMcpMutationError'));
        throw error;
      } finally {
        setMutatingServerId(null);
      }
    },
    [loadServers, t]
  );

  const saveCustomServer = useCallback(
    async (input: SynonBiomedCustomMcpServerInput) => {
      const current = editingServer;
      await mutate(
        current?.id ?? 'new',
        () => (current ? updateSynonBiomedCustomMcpServer(current.id, input) : createSynonBiomedCustomMcpServer(input)),
        current ? t('settings.synonBiomedMcpUpdated') : t('settings.synonBiomedMcpCreated')
      );
      setEditorVisible(false);
      setEditingServer(null);
    },
    [editingServer, mutate, t]
  );

  const removeCustomServer = useCallback(
    (server: SynonBiomedCustomMcpServer) => {
      Modal.confirm({
        title: t('settings.synonBiomedMcpDeleteTitle', { name: server.name }),
        content: t('settings.synonBiomedMcpDeleteDetail'),
        okButtonProps: { status: 'danger' },
        onOk: () =>
          mutate(
            server.id,
            () => deleteSynonBiomedCustomMcpServer(server.id),
            t('settings.synonBiomedMcpDeleted')
          ).catch(ignoreHandledMutationError),
      });
    },
    [mutate, t]
  );

  const authorize = useCallback(
    async (server: SynonBiomedMcpServer) => {
      const popup = window.open('about:blank', '_blank');
      if (!popup) {
        Message.error(t('settings.synonBiomedMcpPopupBlocked'));
        return;
      }
      popup.opener = null;
      try {
        await mutate(server.id, async () => {
          const authorization = await authorizeSynonBiomedMcpConnector(server.id);
          if (!authorization.authorizationUrl) {
            popup.close();
            return;
          }
          popup.location.assign(normalizeAuthorizationUrl(authorization.authorizationUrl));
        });
      } catch (error) {
        if (!popup.closed) popup.close();
        throw error;
      }
    },
    [mutate, t]
  );

  const saveAPIKey = useCallback(async () => {
    const server = credentialServer;
    const apiKey = credentialValue.trim();
    if (!server || !apiKey) return;
    setCredentialSaving(true);
    try {
      await mutate(
        server.id,
        () => configureSynonBiomedMcpConnectorAPIKey(server.id, apiKey),
        t('settings.synonBiomedMcpApiKeySaved')
      );
      setCredentialServer(null);
      setCredentialValue('');
    } finally {
      setCredentialSaving(false);
    }
  }, [credentialServer, credentialValue, mutate, t]);

  const closeCredentialModal = useCallback(() => {
    if (credentialSaving) return;
    setCredentialServer(null);
    setCredentialValue('');
  }, [credentialSaving]);

  const visibleServers = useMemo(() => {
    const query = search.trim().toLowerCase();
    return servers.filter((server) => {
      if (
        query &&
        !`${server.displayName} ${server.name} ${server.description} ${server.source}`.toLowerCase().includes(query)
      )
        return false;
      if (filter === 'connected') return server.enabled && server.connectionStatus === 'connected';
      if (filter === 'needs-attention')
        return !server.enabled || !server.health.ok || server.connectionStatus !== 'connected';
      if (filter === 'custom') return customById.has(server.id) || server.source === 'custom';
      return true;
    });
  }, [customById, filter, search, servers]);

  const grouped = useMemo(() => {
    const custom: SynonBiomedMcpServer[] = [];
    const catalog: SynonBiomedMcpServer[] = [];
    for (const server of visibleServers) {
      if (customById.has(server.id) || server.source === 'custom') custom.push(server);
      else catalog.push(server);
    }
    return { catalog, custom };
  }, [customById, visibleServers]);

  const mcpTotalPages = Math.max(1, Math.ceil(visibleServers.length / MCP_PAGE_SIZE));
  const visiblePageServers = useMemo(
    () => visibleServers.slice((mcpPage - 1) * MCP_PAGE_SIZE, mcpPage * MCP_PAGE_SIZE),
    [mcpPage, visibleServers]
  );
  const groupedPage = useMemo(() => {
    const custom: SynonBiomedMcpServer[] = [];
    const catalog: SynonBiomedMcpServer[] = [];
    for (const server of visiblePageServers) {
      if (customById.has(server.id) || server.source === 'custom') custom.push(server);
      else catalog.push(server);
    }
    return { catalog, custom };
  }, [customById, visiblePageServers]);

  useEffect(() => {
    setMcpPage(1);
  }, [activeView, filter, search]);

  useEffect(() => {
    if (mcpPage > mcpTotalPages) setMcpPage(mcpTotalPages);
  }, [mcpPage, mcpTotalPages]);

  const enabledConnectorCount = servers.filter((server) => server.enabled).length;
  const connectedConnectorCount = servers.filter(
    (server) => server.enabled && server.connectionStatus === 'connected'
  ).length;
  const connectorHealthOK =
    enabledConnectorCount > 0 &&
    connectedConnectorCount === enabledConnectorCount &&
    directoryHealth !== null &&
    directoryHealth.ok;

  return (
    <div className='flex flex-col gap-14px' data-testid='synon-biomed-mcp-settings'>
      <Tabs
        activeTab={activeView}
        destroyOnHide
        onChange={(key) => setActiveView(key as 'installed' | 'optional' | 'marketplace')}
      >
        <Tabs.TabPane key='installed' title={t('settings.synonBiomedMcpInstalledTab')}>
          <div className='flex flex-col gap-10px lg:flex-row lg:items-start lg:justify-between'>
            <div className='min-w-0'>
              <h2 className='m-0 text-16px font-650 text-t-primary'>{t('settings.synonBiomedMcpConnectors')}</h2>
              <p className='m-0 mt-3px max-w-720px text-12px leading-5 text-t-tertiary'>
                {t('settings.synonBiomedMcpDescriptionV11')}
              </p>
            </div>
            <div className='flex flex-wrap gap-8px shrink-0'>
              <Button
                icon={<Refresh size='14' />}
                loading={reconciling}
                disabled={loading}
                data-testid='synon-biomed-mcp-reconcile'
                onClick={() => void reconcile()}
              >
                {t('settings.synonBiomedMcpReconcile')}
              </Button>
              <Button
                type='primary'
                data-testid='synon-biomed-mcp-add'
                onClick={() => {
                  setEditingServer(null);
                  setEditorVisible(true);
                }}
              >
                {t('settings.synonBiomedMcpAddConnector')}
              </Button>
            </div>
          </div>

          <div className='synon-mcp-toolbar flex flex-wrap items-center gap-10px border-y border-arco-2 py-12px'>
            <Input.Search
              value={search}
              allowClear
              data-testid='synon-biomed-mcp-search'
              placeholder={t('settings.synonBiomedMcpSearch')}
              className='min-w-200px flex-1 sm:max-w-360px'
              onChange={setSearch}
            />
            <Select
              value={filter}
              className='w-full sm:w-170px'
              data-testid='synon-biomed-mcp-filter'
              onChange={setFilter}
            >
              <Select.Option value='all'>
                {t('settings.synonBiomedMcpAll')} ({servers.length})
              </Select.Option>
              <Select.Option value='connected'>{t('settings.synonBiomedMcpConnected')}</Select.Option>
              <Select.Option value='needs-attention'>{t('settings.synonBiomedMcpNeedsAttention')}</Select.Option>
              <Select.Option value='custom'>
                {t('settings.synonBiomedMcpCustom')} ({customServers.length})
              </Select.Option>
            </Select>
            <div
              className='flex shrink-0 items-center gap-8px text-12px text-t-secondary sm:ml-auto'
              data-testid='synon-biomed-mcp-directory-health'
            >
              <span
                className={`size-7px rounded-full ${
                  directoryHealth === null || enabledConnectorCount === 0
                    ? 'bg-fill-4'
                    : connectorHealthOK
                      ? 'bg-success-6'
                      : 'bg-danger-6'
                }`}
              />
              <span>
                {t('settings.synonBiomedMcpConnected')} {connectedConnectorCount}/{enabledConnectorCount}
              </span>
              {directoryHealth !== null && !directoryHealth.ok ? (
                <span>· {t('settings.synonBiomedMcpDirectoryUnhealthy')}</span>
              ) : null}
              <Button
                type='text'
                size='mini'
                icon={<Refresh size='13' />}
                loading={loading}
                data-testid='synon-biomed-mcp-refresh'
                aria-label={t('common.refresh')}
                onClick={() => void loadServers()}
              />
            </div>
          </div>

          {loadError ? (
            <div
              data-testid='synon-biomed-mcp-load-error'
              role='status'
              className='settings-load-error-panel flex items-center gap-10px text-12px text-danger-6'
            >
              <span className='min-w-0 flex-1'>{t('settings.synonBiomedMcpPartialLoadError')}</span>
              <Button size='mini' loading={loading} onClick={() => void loadServers()}>
                {t('common.retry')}
              </Button>
            </div>
          ) : null}

          <Spin loading={loading} className='w-full'>
            <div className='flex flex-col gap-18px'>
              <ConnectorSection
                title={t('settings.synonBiomedMcpRecommended')}
                description={t('settings.synonBiomedMcpRecommendedDescription')}
                servers={groupedPage.catalog}
                totalCount={grouped.catalog.length}
                customById={customById}
                mutatingServerId={mutatingServerId}
                onToggle={(server, enabled) =>
                  void mutate(server.id, () => setSynonBiomedMcpConnectorEnabled(server.id, enabled)).catch(
                    ignoreHandledMutationError
                  )
                }
                onPermissions={setPermissionServer}
                onAuthorize={(server) => void authorize(server).catch(ignoreHandledMutationError)}
                onConfigureKey={(server) => {
                  setCredentialValue('');
                  setCredentialServer(server);
                }}
                onDisconnect={(server) =>
                  void mutate(server.id, () => disconnectSynonBiomedMcpConnector(server.id)).catch(
                    ignoreHandledMutationError
                  )
                }
                onAttachAll={(server) =>
                  void mutate(server.id, () => attachSynonBiomedMcpServerToAllAgents(server.id)).catch(
                    ignoreHandledMutationError
                  )
                }
                onDetachAll={(server) =>
                  void mutate(server.id, () => detachSynonBiomedMcpServerFromAllAgents(server.id)).catch(
                    ignoreHandledMutationError
                  )
                }
                onEdit={(custom) => {
                  setEditingServer(custom);
                  setEditorVisible(true);
                }}
                onDelete={removeCustomServer}
              />
              <ConnectorSection
                title={t('settings.synonBiomedMcpYourConnectors')}
                description={t('settings.synonBiomedMcpYourConnectorsDescription')}
                servers={groupedPage.custom}
                totalCount={grouped.custom.length}
                customById={customById}
                mutatingServerId={mutatingServerId}
                onToggle={(server, enabled) =>
                  void mutate(server.id, () => setSynonBiomedMcpConnectorEnabled(server.id, enabled)).catch(
                    ignoreHandledMutationError
                  )
                }
                onPermissions={setPermissionServer}
                onAuthorize={(server) => void authorize(server).catch(ignoreHandledMutationError)}
                onConfigureKey={(server) => {
                  setCredentialValue('');
                  setCredentialServer(server);
                }}
                onDisconnect={(server) =>
                  void mutate(server.id, () => disconnectSynonBiomedMcpConnector(server.id)).catch(
                    ignoreHandledMutationError
                  )
                }
                onAttachAll={(server) =>
                  void mutate(server.id, () => attachSynonBiomedMcpServerToAllAgents(server.id)).catch(
                    ignoreHandledMutationError
                  )
                }
                onDetachAll={(server) =>
                  void mutate(server.id, () => detachSynonBiomedMcpServerFromAllAgents(server.id)).catch(
                    ignoreHandledMutationError
                  )
                }
                onEdit={(custom) => {
                  setEditingServer(custom);
                  setEditorVisible(true);
                }}
                onDelete={removeCustomServer}
              />
              {!loading && visibleServers.length === 0 && search ? (
                <Empty description={t('settings.synonBiomedMcpNoMatches')} />
              ) : null}
            </div>
          </Spin>
          {activeView === 'installed' && visibleServers.length > MCP_PAGE_SIZE ? (
            <SettingsPagination
              page={mcpPage}
              totalPages={mcpTotalPages}
              onChange={setMcpPage}
              label={t('settings.synonBiomedMcpPaginationLabel')}
            />
          ) : null}

          <McpConnectorEditorModal
            visible={editorVisible}
            server={editingServer}
            onCancel={() => {
              setEditorVisible(false);
              setEditingServer(null);
            }}
            onSubmit={saveCustomServer}
          />
          <McpPermissionsModal server={permissionServer} onCancel={() => setPermissionServer(null)} />
          <Modal
            visible={credentialServer !== null}
            title={t('settings.synonBiomedMcpApiKeyTitle', {
              name: credentialServer?.displayName ?? '',
            })}
            okText={t('common.save')}
            cancelText={t('common.cancel')}
            confirmLoading={credentialSaving}
            okButtonProps={{ disabled: credentialValue.trim().length === 0 }}
            onOk={() => void saveAPIKey().catch(ignoreHandledMutationError)}
            onCancel={closeCredentialModal}
            unmountOnExit
          >
            <div className='flex flex-col gap-8px'>
              <label className='text-12px font-600 text-t-primary' htmlFor='synon-biomed-mcp-api-key'>
                {credentialServer?.apiKeyLabel || t('settings.synonBiomedMcpApiKeyLabel')}
              </label>
              <Input.Password
                id='synon-biomed-mcp-api-key'
                data-testid='synon-biomed-mcp-api-key-input'
                value={credentialValue}
                disabled={credentialSaving}
                autoComplete='new-password'
                placeholder={t('settings.synonBiomedMcpApiKeyPlaceholder')}
                onChange={setCredentialValue}
                onPressEnter={() => {
                  if (!credentialSaving && credentialValue.trim()) {
                    void saveAPIKey().catch(ignoreHandledMutationError);
                  }
                }}
              />
              <p className='m-0 text-12px leading-5 text-t-tertiary'>
                {t('settings.synonBiomedMcpApiKeySecurityHint')}
              </p>
              {credentialServer?.authHint ? (
                <p className='m-0 text-12px leading-5 text-t-secondary'>{credentialServer.authHint}</p>
              ) : null}
            </div>
          </Modal>
        </Tabs.TabPane>
        <Tabs.TabPane key='optional' title={t('settings.synonBiomedMcpOptionalTab')}>
          <McpOptionalPanel onInstalled={loadServers} />
        </Tabs.TabPane>
        <Tabs.TabPane key='marketplace' title={t('settings.synonBiomedMcpMarketplaceTab')}>
          <McpMarketplacePanel onInstalled={loadServers} />
        </Tabs.TabPane>
      </Tabs>
    </div>
  );
};

type ConnectorSectionProps = {
  title: string;
  description: string;
  servers: SynonBiomedMcpServer[];
  totalCount: number;
  customById: Map<string, SynonBiomedCustomMcpServer>;
  mutatingServerId: string | null;
  onToggle: (server: SynonBiomedMcpServer, enabled: boolean) => void;
  onPermissions: (server: SynonBiomedMcpServer) => void;
  onAuthorize: (server: SynonBiomedMcpServer) => void;
  onConfigureKey: (server: SynonBiomedMcpServer) => void;
  onDisconnect: (server: SynonBiomedMcpServer) => void;
  onAttachAll: (server: SynonBiomedMcpServer) => void;
  onDetachAll: (server: SynonBiomedMcpServer) => void;
  onEdit: (server: SynonBiomedCustomMcpServer) => void;
  onDelete: (server: SynonBiomedCustomMcpServer) => void;
};

const ConnectorSection: React.FC<ConnectorSectionProps> = ({
  title,
  description,
  servers,
  totalCount,
  customById,
  mutatingServerId,
  ...actions
}) => {
  if (servers.length === 0) return null;
  return (
    <section>
      <div className='synon-mcp-section-heading mb-8px flex items-end justify-between gap-12px'>
        <div className='settings-section__heading min-w-0'>
          <SettingsGeneratedIcon id='tools' className='settings-section__icon' />
          <div className='min-w-0'>
            <h3 className='m-0 text-14px font-650 text-t-primary'>{title}</h3>
            <p className='m-0 mt-2px text-12px text-t-tertiary'>{description}</p>
          </div>
        </div>
        <span className='text-12px text-t-tertiary'>{totalCount}</span>
      </div>
      <div
        className='synon-mcp-grid grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4'
        data-testid={`synon-biomed-mcp-grid-${title}`}
      >
        {servers.map((server) => (
          <ConnectorRow
            key={server.id}
            server={server}
            custom={customById.get(server.id) ?? null}
            busy={mutatingServerId === server.id}
            {...actions}
          />
        ))}
      </div>
    </section>
  );
};

const ConnectorRow: React.FC<{
  server: SynonBiomedMcpServer;
  custom: SynonBiomedCustomMcpServer | null;
  busy: boolean;
  onToggle: (server: SynonBiomedMcpServer, enabled: boolean) => void;
  onPermissions: (server: SynonBiomedMcpServer) => void;
  onAuthorize: (server: SynonBiomedMcpServer) => void;
  onConfigureKey: (server: SynonBiomedMcpServer) => void;
  onDisconnect: (server: SynonBiomedMcpServer) => void;
  onAttachAll: (server: SynonBiomedMcpServer) => void;
  onDetachAll: (server: SynonBiomedMcpServer) => void;
  onEdit: (server: SynonBiomedCustomMcpServer) => void;
  onDelete: (server: SynonBiomedCustomMcpServer) => void;
}> = ({
  server,
  custom,
  busy,
  onToggle,
  onPermissions,
  onAuthorize,
  onConfigureKey,
  onDisconnect,
  onAttachAll,
  onDetachAll,
  onEdit,
  onDelete,
}) => {
  const { i18n, t } = useTranslation();
  const connected = server.enabled && server.connectionStatus === 'connected';
  const authorized = isAuthorized(server.authState);
  const keyDisconnectAvailable = server.apiKeyConfigurable && server.apiKeyConfigured && !server.oauthSupported;
  const upstreams = server.upstreams.map(readUpstreamName).filter((name): name is string => Boolean(name));
  const localizedDescription = server.description
    ? resolveSynonBiomedMcpDescription(server.name, server.description, i18n.language, server.description_i18n)
    : custom?.url;
  const connectorDescription = localizedDescription
    ? compactSettingsDescription(localizedDescription, {
        maxLength: 150,
        stripPrefixes: [server.displayName, server.name, ...upstreams],
      })
    : custom?.url;
  const displayDescription = removeDescriptionTerminalPunctuation(connectorDescription);
  const connectorVisual = resolveMcpConnectorVisual(server.name, server.displayName);
  const connectionState = !server.enabled ? 'disabled' : connected ? 'connected' : 'attention';
  const connectionLabel = !server.enabled
    ? t('settings.synonBiomedMcpDisabled')
    : connected
      ? t('settings.synonBiomedMcpConnected')
      : t('settings.synonBiomedMcpNeedsAttention');
  const usageDetails = formatMcpUsageDetails(server.usage, i18n.language, t);
  const usageSummary = `${usageDetails.lastUsed} · ${usageDetails.count}`;
  const primaryActionKind =
    !authorized && server.oauthSupported && server.authState !== 'not-required'
      ? 'authorize'
      : server.apiKeyConfigurable && !server.oauthSupported
        ? 'configure-key'
        : 'permissions';
  const primaryAction = () => {
    if (primaryActionKind === 'authorize') onAuthorize(server);
    else if (primaryActionKind === 'configure-key') onConfigureKey(server);
    else onPermissions(server);
  };
  const canConfigureKeyInMenu = Boolean(server.apiKeyConfigurable && server.oauthSupported);
  const hasSecondaryActions =
    primaryActionKind !== 'permissions' ||
    (server.oauthSupported && authorized) ||
    canConfigureKeyInMenu ||
    keyDisconnectAvailable ||
    Boolean(custom);
  const actionMenu = (
    <Menu
      onClickMenuItem={(key) => {
        if (key === 'permissions') onPermissions(server);
        else if (key === 'disconnect') onDisconnect(server);
        else if (key === 'configure-key') onConfigureKey(server);
        else if (key === 'attach') {
          if (server.attachedAgents.length) onDetachAll(server);
          else onAttachAll(server);
        } else if (key === 'edit' && custom) onEdit(custom);
        else if (key === 'delete' && custom) onDelete(custom);
      }}
    >
      {primaryActionKind !== 'permissions' ? (
        <Menu.Item key='permissions' data-testid={`synon-biomed-mcp-permissions-${normalizeTestId(server.name)}`}>
          {t('settings.synonBiomedMcpPermissions')}
        </Menu.Item>
      ) : null}
      {server.oauthSupported && authorized ? (
        <Menu.Item key='disconnect' data-testid={`synon-biomed-mcp-disconnect-${normalizeTestId(server.name)}`}>
          {t('settings.synonBiomedMcpDisconnect')}
        </Menu.Item>
      ) : null}
      {canConfigureKeyInMenu ? (
        <Menu.Item key='configure-key' data-testid={`synon-biomed-mcp-configure-key-${normalizeTestId(server.name)}`}>
          {server.apiKeyConfigured
            ? t('settings.synonBiomedMcpReplaceApiKey')
            : t('settings.synonBiomedMcpConfigureApiKey')}
        </Menu.Item>
      ) : null}
      {keyDisconnectAvailable ? (
        <Menu.Item key='disconnect' data-testid={`synon-biomed-mcp-disconnect-${normalizeTestId(server.name)}`}>
          {t('settings.synonBiomedMcpDisconnect')}
        </Menu.Item>
      ) : null}
      {custom ? (
        <>
          <Menu.Item key='attach'>
            {server.attachedAgents.length
              ? t('settings.synonBiomedMcpDetachAll')
              : t('settings.synonBiomedMcpAttachAll')}
          </Menu.Item>
          <Menu.Item key='edit' data-testid={`synon-biomed-mcp-edit-${normalizeTestId(server.name)}`}>
            <span className='flex items-center gap-7px'>
              <Edit size='13' />
              {t('common.edit')}
            </span>
          </Menu.Item>
          <Menu.Item key='delete' data-testid={`synon-biomed-mcp-delete-${normalizeTestId(server.name)}`}>
            <span className='flex items-center gap-7px text-danger-6'>
              <Delete size='13' />
              {t('common.delete')}
            </span>
          </Menu.Item>
        </>
      ) : null}
    </Menu>
  );
  return (
    <article
      data-testid={`synon-biomed-mcp-${normalizeTestId(server.name)}`}
      aria-label={`${server.displayName}. ${connectionLabel}. ${authStateLabel(server.authState, t)}. ${upstreams.join(', ')}. ${usageSummary}`}
      data-mcp-tone={connectorVisual.tone}
      data-mcp-visual={connectorVisual.glyph}
      data-mcp-connection-state={connectionState}
      data-mcp-usage-summary={usageSummary}
      className='synon-mcp-card flex min-w-0 flex-col border border-arco-2 bg-fill-1 transition-colors'
    >
      <McpConnectorVisualMark visual={connectorVisual} variant='artwork' />
      <div className='synon-mcp-card__top flex min-w-0 items-center'>
        <div className='synon-mcp-card__icon flex shrink-0 items-center justify-center'>
          <McpConnectorVisualMark visual={connectorVisual} />
        </div>
        <div className='synon-mcp-card__identity min-w-0 flex-1'>
          <div className='synon-mcp-card__title-row'>
            <span className='min-w-0 truncate font-650 text-t-primary' title={server.displayName}>
              {server.displayName}
            </span>
          </div>
        </div>
      </div>
      <p className='synon-mcp-card__description m-0 line-clamp-3 text-t-secondary' title={displayDescription}>
        {displayDescription}
      </p>
      <div className='synon-mcp-card__usage' aria-label={usageSummary}>
        <span>{usageDetails.lastUsed}</span>
        <span>{usageDetails.count}</span>
      </div>
      <div className='synon-mcp-card__actions flex items-center justify-between'>
        <div className='synon-mcp-card__action-cluster flex items-center'>
          <Button
            size='small'
            type='secondary'
            className='synon-mcp-card__configure'
            data-testid={`synon-biomed-mcp-${primaryActionKind}-${normalizeTestId(server.name)}`}
            onClick={primaryAction}
          >
            {t('settings.synonBiomedMcpConfigure')}
          </Button>
          {hasSecondaryActions ? (
            <Dropdown droplist={actionMenu} trigger='click' position='br' getPopupContainer={() => document.body}>
              <Button
                size='small'
                type='text'
                shape='circle'
                className='synon-mcp-card__more'
                data-testid={`synon-biomed-mcp-more-${normalizeTestId(server.name)}`}
                aria-label={t('common.more')}
                icon={<MoreOne size='15' />}
              />
            </Dropdown>
          ) : null}
        </div>
        <button
          type='button'
          role='switch'
          aria-checked={server.enabled}
          aria-busy={busy}
          aria-label={t('settings.synonBiomedMcpToggleEnabled', { name: server.displayName })}
          title={usageSummary}
          disabled={busy}
          className='synon-mcp-card__status-control'
          data-state={connectionState}
          data-testid={`synon-biomed-mcp-enabled-${normalizeTestId(server.name)}`}
          onClick={() => onToggle(server, !server.enabled)}
        >
          <span className='synon-mcp-card__status-dot' aria-hidden='true' />
          <span>{connectionLabel}</span>
        </button>
      </div>
    </article>
  );
};

function formatMcpUsageDetails(
  usage: SynonBiomedMcpUsage | undefined,
  language: string | undefined,
  t: ReturnType<typeof useTranslation>['t']
): { lastUsed: string; count: string } {
  const lastUsed = usage
    ? usage.lastUsedAt
      ? t('settings.skillsSettings.usageLastUsed', {
          date: formatMcpUsageDate(usage.lastUsedAt, language),
        })
      : t('settings.skillsSettings.usageNever')
    : t('settings.skillsSettings.usageLastUsed', { date: '—' });
  const callCount = usage
    ? t('settings.skillsSettings.usageCount', { count: usage.invocationCount })
    : t('settings.skillsSettings.usageCountUnknown');
  return { lastUsed, count: callCount };
}

function formatMcpUsageDate(value: string, language: string | undefined): string {
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return value;
  const locale = language?.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en-US';
  return new Intl.DateTimeFormat(locale, { dateStyle: 'short', timeStyle: 'short' }).format(new Date(timestamp));
}

function isAuthorized(authState: string): boolean {
  return authState === 'connected' || authState === 'authorized' || authState === 'authenticated';
}

function normalizeAuthorizationUrl(value: string): string {
  const url = new URL(value);
  if (url.protocol !== 'https:') throw new Error('Authorization URL must use HTTPS');
  return url.toString();
}

function authStateLabel(authState: string, t: ReturnType<typeof useTranslation>['t']): string {
  if (authState === 'not-required') return t('settings.synonBiomedMcpNoAuth');
  if (isAuthorized(authState)) return t('settings.synonBiomedMcpConnected');
  if (authState === 'required' || authState === 'unauthorized') return t('settings.synonBiomedMcpAuthRequired');
  return t('settings.synonBiomedMcpAuthUnknown');
}

function readUpstreamName(value: unknown): string | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const name = (value as Record<string, unknown>).name;
  return typeof name === 'string' && name ? name : null;
}

function normalizeTestId(value: string): string {
  return value.replace(/[:/\s<>"'|?*]/g, '-');
}
