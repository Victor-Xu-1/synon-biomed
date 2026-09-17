import type { IConversationMcpStatus, IConversationMcpStatusKind } from '@/common/config/storage';
import { Button, Trigger } from '@arco-design/web-react';
import { Check, Lightning, Right, Shield } from '@icon-park/react';
import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';

type ComposerCapabilityPickerProps = {
  skillNames: string[];
  mcpStatuses: IConversationMcpStatus[];
  selectedSkillNames: string[];
  selectedMcpServerIds: string[];
  onSelectSkill: (name: string) => void;
  onSelectMcpServer?: (server: IConversationMcpStatus) => void;
  onOpenMcpSettings: () => void;
};

const statusClassName: Record<IConversationMcpStatusKind, string> = {
  loaded: 'text-[var(--color-success-6)]',
  failed: 'text-t-primary',
  unsupported: 'text-[var(--color-warning-6)]',
};

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
    className={`composer-control-menu__row mx-6px box-border flex items-center gap-10px border-0 bg-transparent px-12px py-9px text-left rounded-8px text-14px transition-colors ${
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

const ComposerCapabilityPicker: React.FC<ComposerCapabilityPickerProps> = ({
  skillNames,
  mcpStatuses,
  selectedSkillNames,
  selectedMcpServerIds,
  onSelectSkill,
  onSelectMcpServer,
  onOpenMcpSettings,
}) => {
  const { t } = useTranslation();
  const [skillsOpen, setSkillsOpen] = useState(false);
  const [mcpOpen, setMcpOpen] = useState(false);
  if (skillNames.length === 0 && mcpStatuses.length === 0) return null;

  const skillPanel = (
    <div
      className='app-overlay-menu composer-control-submenu min-w-180px py-6px'
      role='menu'
      aria-label={t('conversation.skills.loaded')}
    >
      {skillNames.map((name) => {
        const selected = selectedSkillNames.includes(name);
        return (
          <CapabilityRow
            key={name}
            icon={<Lightning theme='outline' size={15} />}
            label={name}
            checked={selected}
            suffix={selected ? <Check theme='outline' size={14} className='text-primary' /> : undefined}
            onClick={() => onSelectSkill(name)}
          />
        );
      })}
    </div>
  );

  const mcpPanel = (
    <div
      className='app-overlay-menu composer-control-submenu w-[min(320px,calc(100vw-96px))] min-w-220px max-w-320px py-6px'
      role='menu'
      aria-label={t('conversation.mcp.loaded')}
    >
      {mcpStatuses.map((server) => {
        const selected = selectedMcpServerIds.includes(server.id);
        return (
          <CapabilityRow
            key={`${server.id}-${server.status}`}
            icon={<Shield theme='outline' size={15} />}
            label={server.name}
            checked={server.status === 'loaded' ? selected : undefined}
            disabled={server.status !== 'loaded' || !onSelectMcpServer}
            title={server.reason}
            suffix={
              selected ? (
                <Check theme='outline' size={14} className='text-primary' />
              ) : server.status === 'loaded' ? undefined : (
                <span className={`text-12px leading-none ${statusClassName[server.status]}`}>
                  {t(`conversation.mcp.status.${server.status}` as const)}
                </span>
              )
            }
            onClick={() => onSelectMcpServer?.(server)}
          />
        );
      })}
      <div className='mx-12px my-4px h-1px bg-[var(--color-border-1)]' />
      <div className='px-12px py-8px'>
        <div className='text-12px leading-16px text-t-secondary whitespace-normal break-words'>
          {t('conversation.mcp.managementHint')}
        </div>
        <Button type='text' size='mini' className='mt-6px h-auto! px-0! text-12px!' onClick={onOpenMcpSettings}>
          {t('conversation.mcp.openSettings')}
        </Button>
      </div>
    </div>
  );

  return (
    <div className='px-6px'>
      {skillNames.length > 0 ? (
        <Trigger
          popup={() => skillPanel}
          trigger='hover'
          position='right'
          popupVisible={skillsOpen}
          onVisibleChange={setSkillsOpen}
          mouseEnterDelay={100}
          mouseLeaveDelay={150}
        >
          <div>
            <CapabilityRow
              icon={<Lightning theme='outline' size={15} />}
              label={`${t('conversation.skills.loaded')} · ${skillNames.length}`}
              suffix={<Right theme='outline' size={12} strokeWidth={3} className='text-t-tertiary' />}
            />
          </div>
        </Trigger>
      ) : null}
      {mcpStatuses.length > 0 ? (
        <Trigger
          popup={() => mcpPanel}
          trigger='hover'
          position='right'
          popupVisible={mcpOpen}
          onVisibleChange={setMcpOpen}
          mouseEnterDelay={100}
          mouseLeaveDelay={150}
        >
          <div>
            <CapabilityRow
              icon={<Shield theme='outline' size={15} />}
              label={`${t('conversation.mcp.loaded')} · ${mcpStatuses.length}`}
              suffix={<Right theme='outline' size={12} strokeWidth={3} className='text-t-tertiary' />}
            />
          </div>
        </Trigger>
      ) : null}
    </div>
  );
};

export default ComposerCapabilityPicker;
