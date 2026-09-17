import { Button, Empty, Input, Message, Modal, Select, Spin, Switch } from '@arco-design/web-react';
import { Copy, Refresh, Search } from '@icon-park/react';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  loadSynonBiomedThirdPartyLicenses,
  type SynonBiomedThirdPartyLicenses,
} from '@/renderer/services/synonBiomedLicenses';
import { copyText } from '@/renderer/utils/ui/clipboard';

export type ThirdPartyLicenseSection = {
  id: string;
  title: string;
  content: string;
};

type ThirdPartyLicensesModalProps = {
  visible: boolean;
  onClose: () => void;
};

const ThirdPartyLicensesModal: React.FC<ThirdPartyLicensesModalProps> = ({ visible, onClose }) => {
  const { t } = useTranslation();
  const [snapshot, setSnapshot] = useState<SynonBiomedThirdPartyLicenses | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);
  const [query, setQuery] = useState('');
  const [selectedId, setSelectedId] = useState('all');
  const [wrapLines, setWrapLines] = useState(true);
  const [reloadToken, setReloadToken] = useState(0);

  useEffect(() => {
    if (!visible) return;
    const controller = new AbortController();
    setLoading(true);
    setLoadFailed(false);
    void loadSynonBiomedThirdPartyLicenses({ signal: controller.signal })
      .then(setSnapshot)
      .catch((loadError: unknown) => {
        if (!controller.signal.aborted) {
          console.error('Failed to load third-party licenses:', loadError);
          setLoadFailed(true);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [reloadToken, visible]);

  const sections = useMemo(() => {
    if (!snapshot) return [];
    return [
      { id: 'all', title: t('settings.thirdPartyLicenses.fullDocument'), content: snapshot.content },
      ...parseThirdPartyLicenseSections(snapshot.content, t('settings.thirdPartyLicenses.overview')),
    ];
  }, [snapshot, t]);
  const normalizedQuery = query.trim().toLocaleLowerCase();
  const visibleSections = useMemo(() => {
    if (!normalizedQuery) return sections;
    return sections
      .filter((section) => section.id !== 'all')
      .filter(
        (section) =>
          section.title.toLocaleLowerCase().includes(normalizedQuery) ||
          section.content.toLocaleLowerCase().includes(normalizedQuery)
      );
  }, [normalizedQuery, sections]);
  const activeSection =
    visibleSections.find((section) => section.id === selectedId) ?? visibleSections[0] ?? sections[0] ?? null;

  const copyActiveSection = async () => {
    if (!activeSection) return;
    try {
      await copyText(activeSection.content);
      Message.success(t('settings.thirdPartyLicenses.copied'));
    } catch (error) {
      console.error('Failed to copy third-party license section:', error);
      Message.error(t('common.copyFailed'));
    }
  };

  return (
    <Modal
      title={t('settings.thirdPartyLicenses.title')}
      visible={visible}
      footer={null}
      onCancel={onClose}
      unmountOnExit
      style={{ width: 'min(980px, 96vw)' }}
    >
      <div className='flex flex-col gap-10px' data-testid='third-party-licenses-modal'>
        <div className='flex flex-col md:flex-row md:items-center justify-between gap-8px'>
          <div className='min-w-0 text-11px text-t-tertiary'>
            {snapshot
              ? t('settings.thirdPartyLicenses.summary', {
                  count: Math.max(sections.length - 1, 0),
                  bytes: formatBytes(snapshot.bytes),
                  hash: snapshot.sha256.slice(0, 12),
                })
              : t('settings.thirdPartyLicenses.description')}
          </div>
          <div className='flex items-center gap-8px shrink-0'>
            <span className='flex items-center gap-5px text-12px text-t-secondary'>
              {t('settings.thirdPartyLicenses.wrapLines')}
              <Switch
                size='small'
                checked={wrapLines}
                onChange={setWrapLines}
                aria-label={t('settings.thirdPartyLicenses.wrapLines')}
              />
            </span>
            <Button
              type='secondary'
              size='small'
              icon={<Copy theme='outline' size='14' />}
              onClick={() => void copyActiveSection()}
              disabled={!activeSection}
              aria-label={t('settings.thirdPartyLicenses.copySection')}
            >
              {t('common.copy')}
            </Button>
          </div>
        </div>

        <Input
          allowClear
          value={query}
          onChange={setQuery}
          prefix={<Search theme='outline' size='14' />}
          placeholder={t('settings.thirdPartyLicenses.searchPlaceholder')}
          aria-label={t('settings.thirdPartyLicenses.search')}
        />

        {loading && !snapshot ? (
          <div
            className='h-360px flex items-center justify-center'
            aria-label={t('settings.thirdPartyLicenses.loading')}
          >
            <Spin />
          </div>
        ) : loadFailed && !snapshot ? (
          <div className='h-360px flex flex-col items-center justify-center gap-12px text-13px text-t-secondary'>
            <span role='alert'>{t('settings.thirdPartyLicenses.loadFailed')}</span>
            <Button
              type='secondary'
              icon={<Refresh theme='outline' size='14' />}
              onClick={() => setReloadToken((token) => token + 1)}
            >
              {t('common.retry')}
            </Button>
          </div>
        ) : visibleSections.length === 0 ? (
          <div className='h-360px flex items-center justify-center'>
            <Empty description={t('settings.thirdPartyLicenses.empty')} />
          </div>
        ) : (
          <div className='grid grid-cols-1 md:grid-cols-[220px_minmax(0,1fr)] border border-arco-2 rd-6px overflow-hidden min-h-0'>
            <div className='md:hidden border-b border-arco-2 p-8px'>
              <Select
                value={activeSection?.id}
                onChange={setSelectedId}
                className='w-full'
                aria-label={t('settings.thirdPartyLicenses.sections')}
              >
                {visibleSections.map((section) => (
                  <Select.Option key={section.id} value={section.id}>
                    {section.title}
                  </Select.Option>
                ))}
              </Select>
            </div>
            <nav className='hidden md:flex flex-col border-r border-arco-2 bg-2 max-h-520px overflow-y-auto p-6px'>
              {visibleSections.map((section) => (
                <button
                  type='button'
                  key={section.id}
                  className={`text-left px-9px py-7px rd-4px text-12px leading-5 transition-colors ${
                    activeSection?.id === section.id
                      ? 'bg-fill-2 text-t-primary font-600'
                      : 'text-t-secondary hover:bg-fill-1'
                  }`}
                  onClick={() => setSelectedId(section.id)}
                >
                  {section.title}
                </button>
              ))}
            </nav>
            <div className='min-w-0 max-h-520px overflow-auto bg-1'>
              <div className='sticky top-0 z-1 border-b border-arco-2 bg-1 px-14px py-9px text-13px font-600 text-t-primary'>
                {activeSection?.title}
              </div>
              <pre
                data-testid='third-party-license-content'
                className={`m-0 p-14px text-11px leading-5 font-mono text-t-secondary ${
                  wrapLines ? 'whitespace-pre-wrap break-words' : 'whitespace-pre min-w-max'
                }`}
              >
                {activeSection?.content}
              </pre>
            </div>
          </div>
        )}

        {snapshot ? <div className='text-10px text-t-tertiary break-all'>{snapshot.source}</div> : null}
      </div>
    </Modal>
  );
};

export function parseThirdPartyLicenseSections(
  content: string,
  overviewTitle = 'Overview'
): ThirdPartyLicenseSection[] {
  const normalized = content.replace(/\r\n/g, '\n');
  const headingPattern = /^(#{2,3})\s+(.+)$/gm;
  const headings = [...normalized.matchAll(headingPattern)];
  const sections: ThirdPartyLicenseSection[] = [];
  const firstHeadingStart = headings[0]?.index ?? normalized.length;
  const overview = normalized
    .slice(0, firstHeadingStart)
    .replace(/^#\s+[^\n]+\n?/, '')
    .trim();
  if (overview) sections.push({ id: 'overview', title: overviewTitle, content: overview });

  headings.forEach((heading, index) => {
    const start = heading.index ?? 0;
    const end = headings[index + 1]?.index ?? normalized.length;
    sections.push({
      id: `section-${index + 1}`,
      title: heading[2].trim(),
      content: normalized.slice(start, end).trim(),
    });
  });
  return sections;
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  return `${(bytes / 1024).toFixed(1)} KB`;
}

export default ThirdPartyLicensesModal;
