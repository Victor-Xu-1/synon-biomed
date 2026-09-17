import { Dropdown, Tooltip } from '@arco-design/web-react';
import { BranchOne, ListCheckbox, MessageOne, MoreOne } from '@icon-park/react';
import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';

export type SynonBiomedSendIntent = 'plan_first' | 'side_chat' | 'branch_new_session';

type Props = {
  disabled?: boolean;
  hasDraft?: boolean;
  disabledIntentHints?: Partial<Record<SynonBiomedSendIntent, string>>;
  onSelect: (intent: SynonBiomedSendIntent) => void;
};

const rowClass =
  'box-border flex h-36px w-full items-center gap-10px border-0 bg-transparent px-8px text-left text-13px text-t-primary hover:bg-fill-2 disabled:opacity-45';

const SynonBiomedSendOptionsMenu: React.FC<Props> = ({
  disabled = false,
  hasDraft = true,
  disabledIntentHints,
  onSelect,
}) => {
  const { t } = useTranslation();
  const [visible, setVisible] = useState(false);

  const entries: Array<{
    intent: SynonBiomedSendIntent;
    icon: React.ReactNode;
    label: string;
    description: string;
    available: boolean;
  }> = [
    {
      intent: 'plan_first',
      icon: <ListCheckbox theme='outline' size={15} />,
      label: t('conversation.synonRuntime.sendBox.planFirst'),
      description: t('conversation.synonRuntime.sendBox.planFirstDescription'),
      available: true,
    },
    {
      intent: 'side_chat',
      icon: <MessageOne theme='outline' size={15} />,
      label: t('conversation.synonRuntime.sendBox.sideChat'),
      description: t('conversation.synonRuntime.sendBox.sideChatDescription'),
      available: true,
    },
    {
      intent: 'branch_new_session',
      icon: <BranchOne theme='outline' size={15} />,
      label: t('conversation.synonRuntime.sendBox.branchNewSession'),
      description: t('conversation.synonRuntime.sendBox.branchNewSessionDescription'),
      available: true,
    },
  ];

  const droplist = (
    <div
      role='menu'
      aria-label={t('conversation.synonRuntime.sendBox.moreSendOptions')}
      className='app-overlay-menu min-w-184px py-4px'
    >
      {entries.map((entry) =>
        (() => {
          const intentHint = disabledIntentHints?.[entry.intent];
          const itemDisabled = disabled || !entry.available || !hasDraft || Boolean(intentHint);
          const hint = !hasDraft
            ? t('conversation.synonRuntime.sendBox.sendFirstHint')
            : intentHint
              ? intentHint
              : entry.available
                ? entry.description
                : t('conversation.synonRuntime.sendBox.comingSoon');
          return (
            <Tooltip key={entry.intent} content={hint} position='left'>
              <button
                type='button'
                role='menuitem'
                aria-label={entry.label}
                title={hint}
                disabled={itemDisabled}
                className={rowClass}
                onClick={() => {
                  if (itemDisabled) return;
                  setVisible(false);
                  onSelect(entry.intent);
                }}
              >
                <span
                  aria-hidden='true'
                  className='inline-flex h-24px w-24px shrink-0 items-center justify-center rd-6px bg-fill-2 text-t-secondary'
                >
                  {entry.icon}
                </span>
                <span className='min-w-0 flex-1 truncate leading-20px'>{entry.label}</span>
              </button>
            </Tooltip>
          );
        })()
      )}
    </div>
  );

  return (
    <Dropdown
      trigger='click'
      position='tr'
      popupVisible={visible}
      onVisibleChange={(next) => !disabled && setVisible(next)}
      droplist={droplist}
    >
      <button
        type='button'
        aria-label={t('conversation.synonRuntime.sendBox.moreSendOptions')}
        data-testid='synon-biomed-more-send-options'
        disabled={disabled}
        className='synon-biomed-more-send-options relative box-border flex h-32px w-32px shrink-0 items-center justify-center border border-gray-200 bg-white text-gray-700 outline-none hover:bg-gray-100 focus-visible:ring-2 focus-visible:ring-gray-300 rounded-lg disabled:cursor-not-allowed disabled:opacity-50'
      >
        <MoreOne theme='outline' size={16} strokeWidth={2.5} fill='currentColor' className='sendbox-action-icon' />
      </button>
    </Dropdown>
  );
};

export default SynonBiomedSendOptionsMenu;
