import { Button, Empty, Input, Message, Spin, Tag } from '@arco-design/web-react';
import { Link, Refresh, Search } from '@icon-park/react';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  createSynonBiomedCustomMcpServer,
  type SynonBiomedCustomMcpServerInput,
} from '@/renderer/services/synonBiomedCapabilities';
import { resolveSynonBiomedMcpDescription } from '@/renderer/services/mcp/synonBiomedMcpDescriptions';
import { searchMcpMarketplace, type McpMarketplaceEntry } from '@/renderer/services/mcp/mcpMarketplace';

type Props = {
  onInstalled: () => Promise<void>;
};

const QUICK_SEARCHES = [
  { labelKey: 'settings.synonBiomedMcpMarketplaceQuickBiomedical', query: 'bio' },
  { labelKey: 'settings.synonBiomedMcpMarketplaceQuickPubMed', query: 'pubmed' },
  { labelKey: 'settings.synonBiomedMcpMarketplaceQuickChemistry', query: 'chem' },
  { labelKey: 'settings.synonBiomedMcpMarketplaceQuickClinical', query: 'clinical' },
  { labelKey: 'settings.synonBiomedMcpMarketplaceQuickProtein', query: 'protein' },
] as const;

export const McpMarketplacePanel: React.FC<Props> = ({ onInstalled }) => {
  const { t } = useTranslation();
  const [query, setQuery] = useState('bio');
  const [entries, setEntries] = useState<McpMarketplaceEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(false);
  const [pendingId, setPendingId] = useState<string | null>(null);
  const generationRef = useRef(0);

  const runSearch = useCallback(
    async (nextQuery = query) => {
      const generation = ++generationRef.current;
      setLoading(true);
      setError(false);
      try {
        const result = await searchMcpMarketplace(nextQuery);
        if (generation !== generationRef.current) return;
        setEntries(result.entries);
      } catch (reason) {
        if (generation !== generationRef.current) return;
        console.error('Failed to load MCP marketplace:', reason);
        setEntries([]);
        setError(true);
      } finally {
        if (generation === generationRef.current) setLoading(false);
      }
    },
    [query]
  );

  useEffect(() => {
    void runSearch('bio');
    return () => {
      generationRef.current += 1;
    };
  }, [runSearch]);

  const install = useCallback(
    async (entry: McpMarketplaceEntry) => {
      if (!entry.remoteUrl || !entry.transport || entry.authRequired || pendingId) return;
      setPendingId(entry.id);
      try {
        const input: SynonBiomedCustomMcpServerInput = {
          name: connectorName(entry),
          description: `${entry.title} · ${entry.description}`,
          url: entry.remoteUrl,
          transport: entry.transport,
        };
        await createSynonBiomedCustomMcpServer(input);
        Message.success(t('settings.synonBiomedMcpMarketplaceInstalled'));
        await onInstalled();
      } catch (reason) {
        console.error('Failed to install MCP marketplace connector:', reason);
        Message.error(t('settings.synonBiomedMcpMarketplaceInstallFailed'));
      } finally {
        setPendingId(null);
      }
    },
    [onInstalled, pendingId, t]
  );

  return (
    <div className='flex flex-col gap-14px' data-testid='synon-biomed-mcp-marketplace'>
      <div className='border-y border-arco-2 py-12px'>
        <div className='flex items-start gap-10px'>
          <span className='mt-1px flex size-28px shrink-0 items-center justify-center rounded-6px bg-fill-2 text-t-secondary'>
            <Link size={15} />
          </span>
          <div className='min-w-0'>
            <div className='text-14px font-650 text-t-primary'>{t('settings.synonBiomedMcpMarketplaceTitle')}</div>
            <div className='mt-3px max-w-760px text-12px leading-5 text-t-secondary'>
              {t('settings.synonBiomedMcpMarketplaceDescription')}
            </div>
          </div>
        </div>
        <div className='mt-10px flex flex-wrap gap-x-14px gap-y-6px text-12px'>
          <a
            className='text-t-secondary underline underline-offset-3px'
            href='https://registry.modelcontextprotocol.io/docs'
            target='_blank'
            rel='noreferrer'
          >
            {t('settings.synonBiomedMcpMarketplaceOfficialRegistry')}
          </a>
          <a
            className='text-t-secondary underline underline-offset-3px'
            href='https://smithery.ai/'
            target='_blank'
            rel='noreferrer'
          >
            {t('settings.synonBiomedMcpMarketplaceSmithery')}
          </a>
        </div>
        <div className='mt-10px rounded-6px border border-warning-3 bg-warning-1 px-12px py-9px text-12px leading-5 text-t-secondary'>
          {t('settings.synonBiomedMcpMarketplaceSecurity')}
        </div>
      </div>

      <div className='flex flex-wrap items-center gap-8px border-b border-arco-2 pb-12px'>
        <Input
          value={query}
          allowClear
          className='min-w-220px flex-1 sm:max-w-420px'
          prefix={<Search size={14} />}
          data-testid='synon-biomed-mcp-marketplace-search'
          placeholder={t('settings.synonBiomedMcpMarketplaceSearch')}
          onChange={setQuery}
          onPressEnter={() => void runSearch()}
        />
        <Button
          type='primary'
          loading={loading}
          data-testid='synon-biomed-mcp-marketplace-search-submit'
          onClick={() => void runSearch()}
        >
          {t('settings.synonBiomedMcpMarketplaceSearchButton')}
        </Button>
        <Button
          type='text'
          icon={<Refresh size={14} />}
          loading={loading}
          aria-label={t('common.refresh')}
          onClick={() => void runSearch()}
        />
        <div className='flex w-full flex-wrap gap-6px pt-2px'>
          {QUICK_SEARCHES.map((item) => (
            <Button
              key={item.query}
              size='mini'
              type={query === item.query ? 'primary' : 'secondary'}
              onClick={() => {
                setQuery(item.query);
                void runSearch(item.query);
              }}
            >
              {t(item.labelKey)}
            </Button>
          ))}
        </div>
      </div>

      {error ? (
        <div className='flex items-center justify-between gap-10px border-b border-danger-3 pb-10px text-12px text-danger-6'>
          <span>{t('settings.synonBiomedMcpMarketplaceLoadFailed')}</span>
          <Button size='mini' onClick={() => void runSearch()}>
            {t('common.retry')}
          </Button>
        </div>
      ) : null}

      <Spin loading={loading} className='w-full'>
        {entries.length > 0 ? (
          <div
            className='synon-mcp-grid grid grid-cols-1 items-stretch gap-10px sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4'
            role='list'
            data-testid='synon-biomed-mcp-marketplace-grid'
          >
            {entries.map((entry) => (
              <MarketplaceEntryCard
                key={entry.id}
                entry={entry}
                pending={pendingId === entry.id}
                onInstall={() => void install(entry)}
              />
            ))}
          </div>
        ) : !loading && !error ? (
          <Empty description={t('settings.synonBiomedMcpMarketplaceEmpty')} />
        ) : null}
      </Spin>
    </div>
  );
};

const MarketplaceEntryCard: React.FC<{
  entry: McpMarketplaceEntry;
  pending: boolean;
  onInstall: () => void;
}> = ({ entry, pending, onInstall }) => {
  const { i18n, t } = useTranslation();
  const canInstall = Boolean(entry.remoteUrl && entry.transport && !entry.authRequired);
  const connectorId = connectorName(entry);
  const description = resolveSynonBiomedMcpDescription(connectorId, entry.description, i18n.language);
  return (
    <div
      role='listitem'
      data-testid={`synon-biomed-mcp-marketplace-entry-${normalizeTestId(entry.id)}`}
      className='synon-mcp-card flex min-w-0 flex-col border border-arco-2 bg-fill-1 rd-8px transition-colors hover:border-arco-3 hover:bg-fill-2'
    >
      <div className='synon-mcp-card__top flex min-w-0 items-start'>
        <div className='synon-mcp-card__icon settings-list-icon flex size-32px shrink-0 items-center justify-center rounded-6px'>
          <Link size='16' />
        </div>
        <div className='synon-mcp-card__identity min-w-0 flex-1'>
          <div className='synon-mcp-card__title-row'>
            <span className='min-w-0 truncate text-14px font-650 text-t-primary'>{entry.title}</span>
          </div>
          <div className='synon-mcp-card__badges'>
            <Tag size='small' color='arcoblue'>
              {t('settings.synonBiomedMcpMarketplaceOfficialRegistry')}
            </Tag>
            {entry.version ? <Tag size='small'>v{entry.version}</Tag> : null}
          </div>
        </div>
        <Tag size='small'>{t('settings.synonBiomedMcpMarketplaceNotInstalled')}</Tag>
      </div>
      <p className='synon-mcp-card__description m-0 line-clamp-2 text-t-secondary' title={entry.description}>
        {description}
      </p>
      <div className='synon-mcp-card__metadata flex flex-wrap items-center'>
        <span className='text-11px text-t-tertiary'>{entry.provider}</span>
        {entry.transport ? <Tag size='small'>{transportLabel(entry.transport)}</Tag> : null}
        <Tag size='small' color={entry.authRequired ? 'orangered' : 'green'}>
          {entry.authRequired
            ? t('settings.synonBiomedMcpMarketplaceAuthRequired')
            : t('settings.synonBiomedMcpMarketplaceNoAuth')}
        </Tag>
      </div>
      <div
        className='synon-mcp-usage grid grid-cols-2 text-11px text-t-tertiary'
        title={`${t('settings.skillsSettings.usageNever')} · ${t('settings.skillsSettings.usageCount', { count: 0 })}`}
      >
        <span className='min-w-0 truncate'>{t('settings.skillsSettings.usageNever')}</span>
        <span className='truncate text-right tabular-nums'>
          {t('settings.skillsSettings.usageCount', { count: 0 })}
        </span>
      </div>
      <div className='synon-mcp-card__actions flex flex-wrap items-center'>
        <Button
          size='mini'
          type='text'
          loading={pending}
          disabled={!canInstall || pending}
          title={canInstall ? undefined : t('settings.synonBiomedMcpMarketplaceManualSetup')}
          onClick={onInstall}
        >
          {canInstall
            ? t('settings.synonBiomedMcpMarketplaceInstall')
            : t('settings.synonBiomedMcpMarketplaceViewSource')}
        </Button>
        <a
          className='text-11px text-t-tertiary underline underline-offset-3px'
          href={entry.registryUrl}
          target='_blank'
          rel='noreferrer'
        >
          {t('settings.synonBiomedMcpMarketplaceDetails')}
        </a>
      </div>
    </div>
  );
};

function connectorName(entry: McpMarketplaceEntry): string {
  const suffix = entry.name.split('/').pop() || entry.name;
  const normalized = suffix
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return (normalized || 'mcp-server').slice(0, 48);
}

function normalizeTestId(value: string): string {
  return value.replace(/[^a-zA-Z0-9_-]/g, '-');
}

function transportLabel(value: 'sse' | 'streamable_http'): string {
  return value === 'sse' ? 'SSE' : 'Streamable HTTP';
}
