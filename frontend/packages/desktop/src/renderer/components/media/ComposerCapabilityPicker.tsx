/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { IConversationMcpStatus, IConversationMcpStatusKind } from '@/common/config/storage';
import { Input, Trigger } from '@arco-design/web-react';
import { Check, Lightning, Right, Search, Shield, Tool, UploadOne } from '@icon-park/react';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import {
  loadSynonBiomedMcpServers,
  loadSynonBiomedSkills,
  type SynonBiomedMcpServer,
  type SynonBiomedSkill,
} from '@/renderer/services/synonBiomedCapabilities';

type ComposerCapabilityPickerProps = {
  skillNames: string[];
  mcpStatuses: IConversationMcpStatus[];
  selectedSkillNames: string[];
  selectedMcpServerIds: string[];
  onSelectSkill: (name: string) => void;
  onSelectMcpServer?: (server: IConversationMcpStatus) => void;
  onOpenMcpSettings: () => void;
  onRequestClose?: () => void;
};

const statusClassName: Record<IConversationMcpStatusKind, string> = {
  loaded: 'text-[var(--color-success-6)]',
  failed: 'text-t-primary',
  unsupported: 'text-[var(--color-warning-6)]',
};

/** Deterministic pastel pair for capability avatars, keyed by the initial. */
const AVATAR_PALETTE: Array<{ bg: string; fg: string }> = [
  { bg: '#fde8f1', fg: '#c6196e' },
  { bg: '#e8ffea', fg: '#009a29' },
  { bg: '#fff3e8', fg: '#d25f00' },
  { bg: '#e8f7f7', fg: '#0aa5a5' },
  { bg: '#ffece8', fg: '#f53f3f' },
  { bg: '#e8f3ff', fg: '#165dff' },
  { bg: '#f5e8ff', fg: '#722ed1' },
];

function searchMatches(query: string, ...fields: Array<string | undefined>): boolean {
  const needle = query.trim().toLocaleLowerCase();
  if (!needle) return true;
  return fields.some((field) => typeof field === 'string' && field.toLocaleLowerCase().includes(needle));
}

const CapabilityRow: React.FC<{
  icon: React.ReactNode;
  label: string;
  checked?: boolean;
  disabled?: boolean;
  suffix?: React.ReactNode;
  title?: string;
  onClick?: () => void;
}> = ({ icon, label, checked, disabled = false, suffix, title, onClick }) => (
  <button
    type='button'
    role={checked === undefined ? 'menuitem' : 'menuitemcheckbox'}
    aria-checked={checked}
    aria-label={label}
    disabled={disabled}
    title={title}
    className={`composer-control-menu__row mx-6px box-border flex items-center gap-10px border-0 bg-transparent px-12px py-7px text-left rounded-8px text-13px transition-colors ${
      disabled ? 'cursor-not-allowed text-t-secondary' : 'cursor-pointer text-t-primary hover:bg-fill-2'
    }`}
    style={{ width: 'calc(100% - 12px)' }}
    onClick={disabled ? undefined : onClick}
  >
    <span className='inline-flex w-18px flex-shrink-0 items-center justify-center color-#86909c'>{icon}</span>
    <span className='min-w-0 flex-1 truncate'>{label}</span>
    {suffix}
  </button>
);

const SubmenuTriggerRow: React.FC<{ icon: React.ReactNode; label: string }> = ({ icon, label }) => (
  <CapabilityRow
    icon={icon}
    label={label}
    suffix={<Right theme='outline' size={12} strokeWidth={3} className='text-t-tertiary' />}
  />
);

const CapabilityMenuActionRow: React.FC<{
  icon: React.ReactNode;
  label: string;
  onClick: () => void;
}> = ({ icon, label, onClick }) => (
  <button
    type='button'
    role='menuitem'
    className='box-border flex h-30px w-full cursor-pointer items-center gap-8px border-0 bg-transparent px-10px text-left rounded-6px text-12px text-t-primary hover:bg-fill-2'
    onClick={onClick}
  >
    <span className='inline-flex w-16px flex-shrink-0 items-center justify-center color-#86909c'>{icon}</span>
    <span className='min-w-0 flex-1 truncate'>{label}</span>
  </button>
);

const CapabilityListRow: React.FC<{
  name: string;
  description: string;
  checked?: boolean;
  disabled?: boolean;
  statusSuffix?: React.ReactNode;
  onClick?: () => void;
}> = ({ name, description, checked, disabled = false, statusSuffix, onClick }) => {
  const avatar = useMemo(() => {
    const initial = name.trim().charAt(0).toUpperCase() || '?';
    const pair = AVATAR_PALETTE[(name.charCodeAt(0) || 0) % AVATAR_PALETTE.length] ?? AVATAR_PALETTE[0];
    return { bg: pair.bg, fg: pair.fg, letter: initial };
  }, [name]);
  return (
    <button
      type='button'
      role={checked === undefined ? 'menuitem' : 'menuitemcheckbox'}
      aria-checked={checked}
      aria-label={name}
      disabled={disabled}
      title={description || name}
      className={`box-border flex w-full items-center gap-8px border-0 bg-transparent px-10px py-5px text-left rounded-6px transition-colors ${
        checked ? 'bg-fill-2' : 'hover:bg-fill-2'
      } ${disabled ? 'cursor-not-allowed opacity-60' : 'cursor-pointer'}`}
      onClick={disabled ? undefined : onClick}
    >
      <span
        aria-hidden='true'
        className='inline-flex h-20px w-20px flex-shrink-0 items-center justify-center rounded-4px text-11px font-600'
        style={{ background: avatar.bg, color: avatar.fg }}
      >
        {avatar.letter}
      </span>
      <span className='min-w-0 flex-1'>
        <span className='block text-13px leading-18px text-t-primary truncate'>{name}</span>
        <span className='block text-11px leading-14px text-t-secondary truncate'>{description}</span>
      </span>
      {checked ? <Check theme='outline' size={14} className='flex-shrink-0 text-primary' /> : statusSuffix}
    </button>
  );
};

const SearchHeader: React.FC<{
  value: string;
  onChange: (next: string) => void;
  placeholder: string;
}> = ({ value, onChange, placeholder }) => (
  <div className='px-8px pb-4px pt-4px'>
    <Input
      allowClear
      value={value}
      onChange={onChange}
      placeholder={placeholder}
      aria-label={placeholder}
      prefix={<Search theme='outline' size={14} strokeWidth={3} className='text-t-tertiary' />}
      className='capability-submenu-search'
    />
  </div>
);

type CatalogEntry = { label: string; description: string };

function catalogMap(
  entries: Array<{ name: string; displayName: string; description: string }>
): Map<string, CatalogEntry> {
  return new Map<string, CatalogEntry>(
    entries.map((entry) => [entry.name, { label: entry.displayName || entry.name, description: entry.description }])
  );
}

const ComposerCapabilityPicker: React.FC<ComposerCapabilityPickerProps> = ({
  skillNames,
  mcpStatuses,
  selectedSkillNames,
  selectedMcpServerIds,
  onSelectSkill,
  onSelectMcpServer,
  onOpenMcpSettings,
  onRequestClose,
}) => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [skillsOpen, setSkillsOpen] = useState(false);
  const [mcpOpen, setMcpOpen] = useState(false);
  const [skillQuery, setSkillQuery] = useState('');
  const [mcpQuery, setMcpQuery] = useState('');
  const [skillCatalog, setSkillCatalog] = useState<Map<string, CatalogEntry> | null>(null);
  const [mcpCatalog, setMcpCatalog] = useState<Map<string, CatalogEntry> | null>(null);

  // Join the loaded names with the catalog descriptions so every row carries
  // its full blurb; failures only hide the descriptions.
  useEffect(() => {
    let cancelled = false;
    loadSynonBiomedSkills()
      .then((skills: SynonBiomedSkill[]) => {
        if (!cancelled && Array.isArray(skills)) setSkillCatalog(catalogMap(skills));
      })
      .catch((): undefined => undefined);
    loadSynonBiomedMcpServers()
      .then((servers: SynonBiomedMcpServer[]) => {
        if (!cancelled && Array.isArray(servers)) setMcpCatalog(catalogMap(servers));
      })
      .catch((): undefined => undefined);
    return () => {
      cancelled = true;
    };
  }, []);

  const filteredSkills = useMemo(
    () =>
      skillNames
        .map((name) => {
          const catalog = skillCatalog?.get(name);
          return { name, label: catalog?.label ?? name, description: catalog?.description ?? '' };
        })
        .filter((skill) => searchMatches(skillQuery, skill.name, skill.label, skill.description)),
    [skillCatalog, skillNames, skillQuery]
  );

  const filteredServers = useMemo(
    () =>
      mcpStatuses
        .map((server) => {
          const catalog = mcpCatalog?.get(server.name);
          return { server, label: catalog?.label ?? server.name, description: catalog?.description ?? '' };
        })
        .filter(({ server, label, description }) => searchMatches(mcpQuery, server.name, label, description)),
    [mcpCatalog, mcpQuery, mcpStatuses]
  );

  if (skillNames.length === 0 && mcpStatuses.length === 0) return null;

  const skillPanel = (
    <div
      className='app-overlay-menu composer-control-submenu flex flex-col'
      style={{ width: 'min(360px, calc(100vw - 96px))', maxHeight: 'min(400px, calc(100vh - 220px))' }}
      role='menu'
      aria-label={t('conversation.skills.loaded')}
    >
      <SearchHeader
        value={skillQuery}
        onChange={setSkillQuery}
        placeholder={t('conversation.attachMenu.searchSkills')}
      />
      <div className='min-h-0 flex-1 overflow-y-auto overscroll-contain py-2px'>
        {filteredSkills.length === 0 ? (
          <div className='px-12px py-16px text-12px text-t-secondary'>{t('conversation.skills.noMatch')}</div>
        ) : (
          filteredSkills.map((skill) => (
            <CapabilityListRow
              key={skill.name}
              name={skill.label}
              description={skill.description}
              checked={selectedSkillNames.includes(skill.name)}
              onClick={() => onSelectSkill(skill.name)}
            />
          ))
        )}
      </div>
      <div className='mx-8px my-4px h-1px bg-[var(--color-border-1)]' />
      <div className='py-4px'>
        <CapabilityMenuActionRow
          icon={<UploadOne theme='outline' size={15} />}
          label={t('conversation.attachMenu.addLocalSkill')}
          onClick={() => {
            onRequestClose?.();
            navigate('/settings/skills?import=1');
          }}
        />
        <CapabilityMenuActionRow
          icon={<Tool theme='outline' size={15} />}
          label={t('conversation.attachMenu.manageSkills')}
          onClick={() => {
            onRequestClose?.();
            navigate('/settings/skills');
          }}
        />
      </div>
    </div>
  );

  const mcpPanel = (
    <div
      className='app-overlay-menu composer-control-submenu flex flex-col'
      style={{ width: 'min(360px, calc(100vw - 96px))', maxHeight: 'min(400px, calc(100vh - 220px))' }}
      role='menu'
      aria-label={t('conversation.mcp.loaded')}
    >
      <SearchHeader
        value={mcpQuery}
        onChange={setMcpQuery}
        placeholder={t('conversation.attachMenu.searchConnectors')}
      />
      <div className='min-h-0 flex-1 overflow-y-auto overscroll-contain py-2px'>
        {filteredServers.length === 0 ? (
          <div className='px-12px py-16px text-12px text-t-secondary'>{t('conversation.mcp.noMatch')}</div>
        ) : (
          filteredServers.map(({ server, label, description }) => {
            const selected = selectedMcpServerIds.includes(server.id);
            const statusText =
              server.status === 'loaded' ? undefined : (
                <span className={`text-12px leading-none ${statusClassName[server.status]}`}>
                  {t(`conversation.mcp.status.${server.status}` as const)}
                </span>
              );
            return (
              <CapabilityListRow
                key={`${server.id}-${server.status}`}
                name={label}
                description={description || t('conversation.mcp.managementHint')}
                checked={server.status === 'loaded' ? selected : undefined}
                disabled={server.status !== 'loaded' || !onSelectMcpServer}
                statusSuffix={server.status === 'loaded' ? undefined : statusText}
                onClick={() => onSelectMcpServer?.(server)}
              />
            );
          })
        )}
      </div>
      <div className='mx-8px my-4px h-1px bg-[var(--color-border-1)]' />
      <div className='py-4px'>
        <CapabilityMenuActionRow
          icon={<Shield theme='outline' size={15} />}
          label={t('conversation.attachMenu.manageConnectors')}
          onClick={() => {
            onRequestClose?.();
            onOpenMcpSettings();
          }}
        />
      </div>
    </div>
  );

  return (
    <div className='px-6px'>
      {skillNames.length > 0 ? (
        <Trigger
          popup={(): React.ReactNode => skillPanel}
          trigger='hover'
          position='rt'
          popupVisible={skillsOpen}
          onVisibleChange={setSkillsOpen}
          mouseEnterDelay={100}
          mouseLeaveDelay={150}
        >
          <div>
            <SubmenuTriggerRow
              icon={<Lightning theme='outline' size={15} />}
              label={`${t('conversation.skills.loaded')} · ${skillNames.length}`}
            />
          </div>
        </Trigger>
      ) : null}
      {mcpStatuses.length > 0 ? (
        <Trigger
          popup={(): React.ReactNode => mcpPanel}
          trigger='hover'
          position='rt'
          popupVisible={mcpOpen}
          onVisibleChange={setMcpOpen}
          mouseEnterDelay={100}
          mouseLeaveDelay={150}
        >
          <div>
            <SubmenuTriggerRow
              icon={<Shield theme='outline' size={15} />}
              label={`${t('conversation.mcp.loaded')} · ${mcpStatuses.length}`}
            />
          </div>
        </Trigger>
      ) : null}
    </div>
  );
};

export default ComposerCapabilityPicker;
