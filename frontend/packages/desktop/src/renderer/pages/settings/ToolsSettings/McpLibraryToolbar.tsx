import { Button, Dropdown, Input, Menu } from '@arco-design/web-react';
import { Down, Plus, Refresh, Search } from '@icon-park/react';
import React from 'react';
import { useTranslation } from 'react-i18next';
import SettingsPageHeader from '../components/SettingsPageHeader';

export type ConnectorFilter = 'all' | 'connected' | 'needs-attention' | 'custom';
export type ConnectorBrowseView = 'optional' | 'marketplace';

interface Props {
  count: number;
  customCount: number;
  search: string;
  onSearch: (value: string) => void;
  filter: ConnectorFilter;
  onFilter: (value: ConnectorFilter) => void;
  onCreate: () => void;
  onBrowse: (view: ConnectorBrowseView) => void;
  onReconcile: () => void;
  loading: boolean;
  reconciling: boolean;
  health: React.ReactNode;
}

/** The installed library stays mounted while users browse additional connectors. */
export function McpLibraryToolbar(props: Props) {
  const { t } = useTranslation();
  return (
    <>
      <SettingsPageHeader
        data-testid='tools-header'
        title={
          <>
            {t('settings.tools')} <span className='mcp-library-total'>{props.count}</span>
          </>
        }
        actions={
          <Dropdown
            trigger='click'
            position='br'
            droplist={
              <Menu
                onClickMenuItem={(key) =>
                  key === 'custom' ? props.onCreate() : props.onBrowse(key as ConnectorBrowseView)
                }
              >
                <Menu.Item key='custom'>{t('settings.synonBiomedMcpCustom')}</Menu.Item>
                <Menu.Item key='optional'>{t('settings.synonBiomedMcpOptionalTab')}</Menu.Item>
                <Menu.Item key='marketplace'>{t('settings.synonBiomedMcpMarketplaceTab')}</Menu.Item>
              </Menu>
            }
          >
            <Button type='primary' icon={<Plus size={15} />} data-testid='synon-biomed-mcp-add'>
              {t('settings.synonBiomedMcpAddConnector')}
              <Down size={14} />
            </Button>
          </Dropdown>
        }
      />
      <div className='mcp-library-toolbar' role='search' aria-label={t('settings.synonBiomedMcpConnectors')}>
        <div className='mcp-library-search'>
          <Input
            value={props.search}
            allowClear
            prefix={<Search size={15} />}
            data-testid='synon-biomed-mcp-search'
            placeholder={t('settings.synonBiomedMcpSearch')}
            aria-label={t('settings.synonBiomedMcpSearch')}
            onChange={props.onSearch}
          />
        </div>
        <select
          className='mcp-library-select'
          value={props.filter}
          data-testid='synon-biomed-mcp-filter'
          aria-label={t('settings.skillsSettings.filters')}
          onChange={(event) => props.onFilter(event.target.value as ConnectorFilter)}
        >
          <option value='all'>
            {t('settings.synonBiomedMcpAll')} ({props.count})
          </option>
          <option value='connected'>{t('settings.synonBiomedMcpConnected')}</option>
          <option value='needs-attention'>{t('settings.synonBiomedMcpNeedsAttention')}</option>
          <option value='custom'>
            {t('settings.synonBiomedMcpCustom')} ({props.customCount})
          </option>
        </select>
        <div className='mcp-library-health'>{props.health}</div>
        <Button
          icon={<Refresh size={14} />}
          loading={props.reconciling}
          disabled={props.loading}
          data-testid='synon-biomed-mcp-reconcile'
          onClick={props.onReconcile}
        >
          {t('settings.synonBiomedMcpReconcile')}
        </Button>
      </div>
    </>
  );
}
