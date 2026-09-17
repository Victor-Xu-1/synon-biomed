import { Button, Empty, Message, Modal, Spin, Switch, Tag } from '@arco-design/web-react';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  loadSynonBiomedMcpToolPermissions,
  updateSynonBiomedMcpToolPermission,
  type SynonBiomedMcpServer,
  type SynonBiomedMcpToolPermission,
  type SynonBiomedMcpToolPermissionState,
} from '@/renderer/services/synonBiomedCapabilities';

type Props = {
  server: SynonBiomedMcpServer | null;
  onCancel: () => void;
};

const DECISIONS: SynonBiomedMcpToolPermissionState[] = ['allow', 'ask', 'deny'];

export const McpPermissionsModal: React.FC<Props> = ({ server, onCancel }) => {
  const { t } = useTranslation();
  const [tools, setTools] = useState<SynonBiomedMcpToolPermission[]>([]);
  const [loading, setLoading] = useState(false);
  const [mutatingTool, setMutatingTool] = useState<string | null>(null);
  const [mutatingAll, setMutatingAll] = useState(false);
  const [skipApprovalsActive, setSkipApprovalsActive] = useState(false);
  const loadGeneration = useRef(0);

  const load = useCallback(async () => {
    const generation = ++loadGeneration.current;
    setTools([]);
    setSkipApprovalsActive(false);
    if (!server) {
      setLoading(false);
      return;
    }
    setLoading(true);
    try {
      const permissions = await loadSynonBiomedMcpToolPermissions(server.id);
      if (generation === loadGeneration.current) {
        setTools(permissions.tools);
        setSkipApprovalsActive(
          permissions.skipApprovalsActive ||
            (permissions.tools.length > 0 && permissions.tools.every((tool) => tool.state === 'allow'))
        );
      }
    } catch (error) {
      console.warn('[McpPermissionsModal] Failed to load MCP tool permissions', {
        errorName: error instanceof Error ? error.name : typeof error,
      });
      if (generation === loadGeneration.current) {
        setTools([]);
        setSkipApprovalsActive(false);
        Message.error(t('settings.synonBiomedMcpFetchError'));
      }
    } finally {
      if (generation === loadGeneration.current) {
        setLoading(false);
      }
    }
  }, [server, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const setDecision = useCallback(
    async (tool: SynonBiomedMcpToolPermission, state: SynonBiomedMcpToolPermissionState) => {
      if (!server) return;
      setMutatingTool(tool.toolName);
      try {
        await updateSynonBiomedMcpToolPermission(server.id, tool.toolName, state);
        setTools((current) => current.map((item) => (item.toolName === tool.toolName ? { ...item, state } : item)));
        setSkipApprovalsActive(false);
      } catch (error) {
        console.warn('[McpPermissionsModal] Failed to update MCP tool permission', {
          errorName: error instanceof Error ? error.name : typeof error,
        });
        Message.error(t('settings.synonBiomedMcpMutationError'));
      } finally {
        setMutatingTool(null);
      }
    },
    [server, t]
  );

  const setAllDecisions = useCallback(
    async (allow: boolean) => {
      if (!server || tools.length === 0) return;
      setMutatingAll(true);
      const state: SynonBiomedMcpToolPermissionState = allow ? 'allow' : 'ask';
      try {
        await Promise.all(tools.map((tool) => updateSynonBiomedMcpToolPermission(server.id, tool.toolName, state)));
        setTools((current) => current.map((tool) => ({ ...tool, state })));
        setSkipApprovalsActive(allow);
      } catch (error) {
        console.warn('[McpPermissionsModal] Failed to update all MCP tool permissions', {
          errorName: error instanceof Error ? error.name : typeof error,
        });
        Message.error(t('settings.synonBiomedMcpMutationError'));
        await load();
      } finally {
        setMutatingAll(false);
      }
    },
    [load, server, t, tools]
  );

  return (
    <Modal
      visible={Boolean(server)}
      title={t('settings.synonBiomedMcpPermissionsTitle', { name: server?.displayName ?? '' })}
      footer={null}
      onCancel={onCancel}
      style={{ width: 720, maxWidth: 'calc(100vw - 32px)' }}
      unmountOnExit
    >
      <Spin loading={loading} className='w-full'>
        <div className='max-h-60vh overflow-y-auto flex flex-col gap-8px'>
          {tools.length > 0 ? (
            <div className='mb-4px flex items-start justify-between gap-16px rounded-8px border border-arco-2 bg-fill-1 px-13px py-12px'>
              <div>
                <div className='text-13px font-650 text-t-primary'>{t('settings.synonBiomedMcpSkipApprovals')}</div>
                <div className='mt-3px text-12px leading-5 text-t-secondary'>
                  {t('settings.synonBiomedMcpSkipApprovalsDetail')}
                </div>
              </div>
              <Switch
                checked={skipApprovalsActive}
                loading={mutatingAll}
                disabled={mutatingAll || Boolean(mutatingTool)}
                aria-label={t('settings.synonBiomedMcpSkipApprovals')}
                data-testid='synon-biomed-mcp-skip-approvals'
                onChange={(checked) => void setAllDecisions(checked)}
              />
            </div>
          ) : null}
          {tools.map((tool) => (
            <div
              key={tool.toolName}
              className='border-t border-arco-2 px-2px py-12px first:border-t-0 flex flex-col sm:flex-row sm:items-start sm:justify-between gap-10px'
            >
              <div className='min-w-0'>
                <div className='flex flex-wrap items-center gap-6px'>
                  <span className='text-13px font-600 text-t-primary'>{tool.title || tool.toolName}</span>
                  {tool.readOnlyHint ? <Tag size='small'>{t('settings.synonBiomedMcpReadOnlyTool')}</Tag> : null}
                </div>
                <div className='mt-3px text-11px text-t-tertiary break-all'>{tool.toolName}</div>
                {tool.description ? (
                  <div className='mt-4px text-12px leading-5 text-t-secondary'>{tool.description}</div>
                ) : null}
              </div>
              <div className='flex flex-wrap gap-6px shrink-0'>
                {DECISIONS.map((decision) => (
                  <Button
                    key={decision}
                    size='mini'
                    type={tool.state === decision ? 'primary' : 'secondary'}
                    loading={mutatingTool === tool.toolName && tool.state !== decision}
                    disabled={mutatingTool === tool.toolName || mutatingAll}
                    data-testid={`synon-biomed-mcp-permission-${tool.toolName}-${decision}`}
                    onClick={() => void setDecision(tool, decision)}
                  >
                    {permissionLabel(decision, t)}
                  </Button>
                ))}
              </div>
            </div>
          ))}
          {!loading && tools.length === 0 ? <Empty description={t('settings.synonBiomedMcpPermissionsEmpty')} /> : null}
        </div>
      </Spin>
    </Modal>
  );
};

function permissionLabel(state: SynonBiomedMcpToolPermissionState, t: ReturnType<typeof useTranslation>['t']): string {
  if (state === 'allow') return t('settings.synonBiomedMcpPermissionAllow');
  if (state === 'deny') return t('settings.synonBiomedMcpPermissionDeny');
  return t('settings.synonBiomedMcpPermissionAsk');
}
