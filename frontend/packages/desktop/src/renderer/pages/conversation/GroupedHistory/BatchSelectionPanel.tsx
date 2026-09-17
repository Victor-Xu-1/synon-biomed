import { Button } from '@arco-design/web-react';
import React from 'react';
import { useTranslation } from 'react-i18next';

type BatchSelectionPanelProps = {
  scope: 'projects' | 'tasks';
  selectedCount: number;
  allSelected: boolean;
  disabled?: boolean;
  onToggleSelectAll: () => void;
  onDelete: () => void;
};

const BatchSelectionPanel: React.FC<BatchSelectionPanelProps> = ({
  scope,
  selectedCount,
  allSelected,
  disabled = false,
  onToggleSelectAll,
  onDelete,
}) => {
  const { t } = useTranslation();

  return (
    <div data-testid={`${scope}-batch-panel`} className='px-10px pb-8px'>
      <div className='rd-7px bg-fill-1 p-8px flex flex-col gap-7px border border-solid border-[rgba(var(--primary-6),0.08)]'>
        <div className='text-12px leading-18px text-t-secondary'>
          {t('conversation.history.selectedCount', { count: selectedCount })}
        </div>
        <div className='grid grid-cols-2 gap-6px'>
          <Button
            data-testid={`${scope}-batch-select-all`}
            className='!w-full !justify-center !min-w-0 !h-28px !px-6px !text-12px whitespace-nowrap'
            size='mini'
            type='secondary'
            disabled={disabled}
            onClick={onToggleSelectAll}
          >
            {allSelected ? t('common.cancel') : t('conversation.history.selectAll')}
          </Button>
          <Button
            data-testid={`${scope}-batch-delete`}
            className='!w-full !justify-center !min-w-0 !h-28px !px-6px !text-12px whitespace-nowrap'
            size='mini'
            status='warning'
            disabled={disabled || selectedCount === 0}
            onClick={onDelete}
          >
            {t('conversation.history.batchDelete')}
          </Button>
        </div>
      </div>
    </div>
  );
};

export default BatchSelectionPanel;
