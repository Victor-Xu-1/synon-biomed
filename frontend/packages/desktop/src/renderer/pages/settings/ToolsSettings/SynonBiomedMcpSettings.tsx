import { Button, Empty, Message, Modal, Spin, Tabs } from '@arco-design/web-react';
import { Refresh } from '@icon-park/react';
import React, { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
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
} from '@/renderer/services/synonBiomedCapabilities';
import { McpConnectorEditorModal } from './McpConnectorEditorModal';
import { McpMarketplacePanel } from './McpMarketplacePanel';
import { McpOptionalPanel } from './McpOptionalPanel';
import { McpPermissionsModal } from './McpPermissionsModal';
import { resolveSynonBiomedMcpDescription } from '@/renderer/services/mcp/synonBiomedMcpDescriptions';
import { McpConnectorCard } from './McpConnectorCard';
import { McpConnectorConfigurationModal } from './McpConnectorConfigurationModal';
import { McpLibraryToolbar, type ConnectorFilter, type ConnectorBrowseView } from './McpLibraryToolbar';
import SettingsPagination from '../components/SettingsPagination';

// Keep page size independent of viewport and card content.
const MCP_PAGE_SIZE = 12;

const ignoreHandledMutationError = (_error: unknown): undefined => undefined;

export const SynonBiomedMcpSettingsContent: React.FC = () => {
  const { t, i18n } = useTranslation();
  const [servers, setServers] = useState<SynonBiomedMcpServer[]>([]);
  const [customServers, setCustomServers] = useState<SynonBiomedCustomMcpServer[]>([]);
  const [directoryHealth, setDirectoryHealth] = useState<SynonBiomedMcpDirectoryHealth | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const [reconciling, setReconciling] = useState(false);
  const [mutatingServerId, setMutatingServerId] = useState<string | null>(null);
  const [permissionServer, setPermissionServer] = useState<SynonBiomedMcpServer | null>(null);
  const [credentialServer, setCredentialServer] = useState<SynonBiomedMcpServer | null>(null);
  const [editorVisible, setEditorVisible] = useState(false);
  const [editingServer, setEditingServer] = useState<SynonBiomedCustomMcpServer | null>(null);
  const [search, setSearch] = useState('');
  const [filter, setFilter] = useState<ConnectorFilter>('all');
  const [mcpPage, setMcpPage] = useState(1);
  const [browseView, setBrowseView] = useState<ConnectorBrowseView | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
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
        console.error('Failed to update Synon Biomed MCP connector.');
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
        return false;
      }
      popup.opener = null;
      let started = false;
      try {
        await mutate(server.id, async () => {
          const authorization = await authorizeSynonBiomedMcpConnector(server.id);
          if (!authorization.authorizationUrl) {
            popup.close();
            return;
          }
          popup.location.assign(normalizeAuthorizationUrl(authorization.authorizationUrl));
          started = true;
        });
        return started;
      } catch (error) {
        if (!popup.closed) popup.close();
        throw error;
      }
    },
    [mutate, t]
  );

  const currentCredentialServer = credentialServer
    ? (servers.find((server) => server.id === credentialServer.id) ?? credentialServer)
    : null;

  const visibleServers = useMemo(() => {
    const query = search.trim().toLowerCase();
    return servers.filter((server) => {
      const description = resolveSynonBiomedMcpDescription(
        server.name,
        server.description,
        i18n.language,
        server.description_i18n
      );
      if (
        query &&
        !`${server.displayName} ${server.name} ${description} ${server.source}`.toLowerCase().includes(query)
      )
        return false;
      if (filter === 'connected') return server.enabled && server.connectionStatus === 'connected';
      if (filter === 'needs-attention')
        return !server.enabled || !server.health.ok || server.connectionStatus !== 'connected';
      if (filter === 'custom') return customById.has(server.id) || server.source === 'custom';
      return true;
    });
  }, [customById, filter, i18n.language, search, servers]);

  const mcpTotalPages = Math.max(1, Math.ceil(visibleServers.length / MCP_PAGE_SIZE));
  const visiblePageServers = useMemo(
    () => visibleServers.slice((mcpPage - 1) * MCP_PAGE_SIZE, mcpPage * MCP_PAGE_SIZE),
    [mcpPage, visibleServers]
  );
  useEffect(() => {
    setMcpPage(1);
  }, [filter, search]);

  useEffect(() => {
    if (mcpPage > mcpTotalPages) setMcpPage(mcpTotalPages);
  }, [mcpPage, mcpTotalPages]);

  useLayoutEffect(() => {
    if (scrollRef.current) scrollRef.current.scrollTop = 0;
  }, [mcpPage, filter, search]);

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
    <div className='mcp-library' data-testid='synon-biomed-mcp-settings'>
      <McpLibraryToolbar
        count={servers.length}
        customCount={customServers.length}
        search={search}
        onSearch={setSearch}
        filter={filter}
        onFilter={setFilter}
        onCreate={() => {
          setEditingServer(null);
          setEditorVisible(true);
        }}
        onBrowse={setBrowseView}
        onReconcile={() => void reconcile()}
        loading={loading}
        reconciling={reconciling}
        health={
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
        }
      />

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

      <div className='mcp-library-scroll' data-testid='mcp-library-scroll' ref={scrollRef}>
        <Spin loading={loading} className='w-full'>
          <div className='synon-mcp-grid' data-testid='synon-biomed-mcp-grid'>
            {visiblePageServers.map((item) => (
              <McpConnectorCard
                key={item.id}
                server={item}
                custom={customById.get(item.id) ?? null}
                busy={mutatingServerId === item.id}
                onToggle={(server, enabled) =>
                  void mutate(server.id, () => setSynonBiomedMcpConnectorEnabled(server.id, enabled)).catch(
                    ignoreHandledMutationError
                  )
                }
                onPermissions={setPermissionServer}
                onConfigure={setCredentialServer}
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
            ))}
          </div>
          {!loading && !loadError && visibleServers.length === 0 ? (
            <Empty
              description={t(
                servers.length === 0 && !search && filter === 'all'
                  ? 'settings.synonBiomedMcpEmpty'
                  : 'settings.synonBiomedMcpNoMatches'
              )}
            />
          ) : null}
        </Spin>
      </div>
      <footer className='mcp-library-footer' data-testid='mcp-library-footer'>
        {visibleServers.length > MCP_PAGE_SIZE ? (
          <SettingsPagination
            page={mcpPage}
            totalPages={mcpTotalPages}
            onChange={(page) => {
              setMcpPage(page);
              if (scrollRef.current) scrollRef.current.scrollTop = 0;
            }}
            label={t('settings.synonBiomedMcpPaginationLabel')}
          />
        ) : null}
      </footer>

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
      {currentCredentialServer ? (
        <McpConnectorConfigurationModal
          key={currentCredentialServer.id}
          server={currentCredentialServer}
          statusUnavailable={loadError}
          onCancel={() => setCredentialServer(null)}
          onSaveKey={async (apiKey) => {
            await mutate(currentCredentialServer.id, () =>
              configureSynonBiomedMcpConnectorAPIKey(currentCredentialServer.id, apiKey)
            );
          }}
          onAuthorize={() => authorize(currentCredentialServer)}
          onRefresh={loadServers}
          onPermissions={() => {
            setPermissionServer(currentCredentialServer);
            setCredentialServer(null);
          }}
        />
      ) : null}
      <Modal
        visible={browseView !== null}
        title={t('settings.synonBiomedMcpAddConnector')}
        footer={null}
        onCancel={() => setBrowseView(null)}
        unmountOnExit
        className='mcp-library-browser'
        getPopupContainer={() => scrollRef.current?.closest<HTMLElement>('.settings-page-wrapper') ?? document.body}
        style={{ width: 'min(1100px, calc(100vw - 32px))' }}
      >
        <Tabs
          activeTab={browseView ?? 'optional'}
          onChange={(key) => setBrowseView(key as ConnectorBrowseView)}
          destroyOnHide
        >
          <Tabs.TabPane key='optional' title={t('settings.synonBiomedMcpOptionalTab')}>
            <McpOptionalPanel onInstalled={loadServers} />
          </Tabs.TabPane>
          <Tabs.TabPane key='marketplace' title={t('settings.synonBiomedMcpMarketplaceTab')}>
            <McpMarketplacePanel onInstalled={loadServers} />
          </Tabs.TabPane>
        </Tabs>
      </Modal>
    </div>
  );
};

function normalizeAuthorizationUrl(value: string): string {
  const url = new URL(value);
  if (url.protocol !== 'https:' || url.username || url.password)
    throw new Error('Authorization URL must use HTTPS without user information');
  return url.toString();
}
