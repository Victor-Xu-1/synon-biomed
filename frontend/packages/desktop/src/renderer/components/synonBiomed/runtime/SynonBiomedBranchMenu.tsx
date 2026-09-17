import {
  SYNON_BIOMED_SESSION_WORKFLOW_CAPABILITIES,
  loadSynonBiomedConversationBranches,
  selectSynonBiomedBranch,
  type SynonBiomedConversationBranchState,
} from '@/renderer/services/synonBiomedConversationBranches';
import { Dropdown, Spin, Tooltip } from '@arco-design/web-react';
import { BranchOne, Check, Refresh } from '@icon-park/react';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

type Props = {
  rootFrameId: string;
  disabled?: boolean;
};

const rowClass =
  'box-border flex h-32px w-full items-center justify-between border-0 bg-transparent px-12px text-left text-13px text-t-primary hover:bg-fill-2 disabled:opacity-50';

type OwnedBranchState = {
  rootFrameId: string;
  value: SynonBiomedConversationBranchState;
};

const SynonBiomedBranchMenu: React.FC<Props> = ({ rootFrameId, disabled = false }) => {
  const branchSelectionSupported = SYNON_BIOMED_SESSION_WORKFLOW_CAPABILITIES.branchSelection.state === 'supported';
  const { t } = useTranslation();
  const [visible, setVisible] = useState(false);
  const [loading, setLoading] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);
  const [retryGeneration, setRetryGeneration] = useState(0);
  const [ownedState, setOwnedState] = useState<OwnedBranchState | null>(null);
  const state = ownedState?.rootFrameId === rootFrameId ? ownedState.value : null;

  useEffect(() => {
    if (!branchSelectionSupported || !visible || !rootFrameId) return;
    let active = true;
    setLoading(true);
    setLoadFailed(false);
    void loadSynonBiomedConversationBranches(rootFrameId)
      .then((next) => {
        if (active) setOwnedState({ rootFrameId, value: next });
      })
      .catch(() => {
        console.error('[branches] load failed');
        if (active) {
          setOwnedState((current) => (current?.rootFrameId === rootFrameId ? null : current));
          setLoadFailed(true);
        }
      })
      .finally(() => active && setLoading(false));
    return () => {
      active = false;
    };
  }, [branchSelectionSupported, retryGeneration, rootFrameId, visible]);

  const rows = useMemo(() => {
    const branches = state?.branches ?? [];
    let branchNumber = 0;
    return branches.map((branch) => {
      const selected = branch.id === state?.selectedBranchId;
      if (branch.parentId) branchNumber += 1;
      const label = branch.parentId
        ? t('conversation.synonRuntime.branches.branchNumber', { number: branchNumber })
        : t('conversation.synonRuntime.branches.main');
      return (
        <button
          key={branch.id}
          type='button'
          role='menuitemradio'
          aria-checked={selected}
          className={rowClass}
          onClick={() => {
            selectSynonBiomedBranch(rootFrameId, branch.id);
            setOwnedState((current) =>
              current?.rootFrameId === rootFrameId
                ? { ...current, value: { ...current.value, selectedBranchId: branch.id } }
                : current
            );
            setVisible(false);
          }}
        >
          <span className='min-w-0 truncate'>
            {label}
            {branch.active ? t('conversation.synonRuntime.branches.currentSuffix') : ''}
          </span>
          {selected ? <Check size={14} /> : null}
        </button>
      );
    });
  }, [rootFrameId, state, t]);

  const droplist = (
    <div
      role='menu'
      aria-label={t('conversation.synonRuntime.branches.title')}
      className='app-overlay-menu min-w-160px py-4px'
    >
      {loading ? (
        <div className='flex h-40px items-center justify-center'>
          <Spin size={14} />
        </div>
      ) : loadFailed ? (
        <div role='alert' className='flex min-h-48px items-center gap-8px px-12px py-8px text-12px text-danger-7'>
          <span className='min-w-0 flex-1'>{t('conversation.synonRuntime.branches.loadFailed')}</span>
          <button
            type='button'
            className='inline-flex h-28px items-center gap-4px border-0 bg-transparent px-4px text-12px text-primary'
            onClick={() => setRetryGeneration((generation) => generation + 1)}
          >
            <Refresh theme='outline' size={13} />
            {t('common.retry')}
          </button>
        </div>
      ) : (
        rows
      )}
      {!loading && !loadFailed && rows.length === 0 ? (
        <div className='px-12px py-8px text-12px text-t-tertiary'>{t('conversation.synonRuntime.branches.empty')}</div>
      ) : null}
    </div>
  );

  if (!branchSelectionSupported) return null;

  return (
    <Dropdown
      trigger='click'
      position='tl'
      popupVisible={visible}
      onVisibleChange={(next) => !disabled && setVisible(next)}
      droplist={droplist}
    >
      <Tooltip content={t('conversation.synonRuntime.branches.title')} position='top'>
        <button
          type='button'
          aria-label={t('conversation.synonRuntime.branches.title')}
          data-testid='synon-biomed-branch-trigger'
          disabled={disabled}
          className='relative box-border flex h-32px w-32px shrink-0 items-center justify-center rd-6px border-0 bg-transparent text-t-secondary hover:bg-fill-2 hover:text-t-primary disabled:opacity-50'
        >
          <BranchOne theme='outline' size={17} />
          {state?.selectedBranchId && state.selectedBranchId !== state.activeBranchId ? (
            <span className='absolute right-4px top-4px h-5px w-5px rounded-full bg-primary' aria-hidden='true' />
          ) : null}
        </button>
      </Tooltip>
    </Dropdown>
  );
};

export default SynonBiomedBranchMenu;
