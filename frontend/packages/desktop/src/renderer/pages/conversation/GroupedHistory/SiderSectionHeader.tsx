import { Right } from '@icon-park/react';
import classNames from 'classnames';
import React from 'react';

type SiderSectionHeaderProps = {
  testId: string;
  label: string;
  expanded: boolean;
  toggleLabel: string;
  onToggle: () => void;
  onTitleClick?: () => void;
  trailing?: React.ReactNode;
};

export const SiderSectionHeader: React.FC<SiderSectionHeaderProps> = ({
  testId,
  label,
  expanded,
  toggleLabel,
  onToggle,
  onTitleClick,
  trailing,
}) => (
  <div
    data-testid={testId}
    className='group/label sider-section-label synon-sidebar-section-header flex items-center px-8px h-28px select-none sticky top-0 z-10 mt-8px'
  >
    <button
      type='button'
      aria-label={toggleLabel}
      aria-expanded={expanded}
      className='synon-sidebar-section-toggle size-24px shrink-0 flex items-center justify-center border-none bg-transparent rd-5px cursor-pointer text-t-tertiary hover:text-t-primary hover:bg-fill-3'
      onClick={onToggle}
    >
      <Right
        theme='outline'
        size={12}
        className={classNames('text-t-tertiary transition-transform duration-150', { 'rotate-90': expanded })}
      />
    </button>
    <button
      type='button'
      className='synon-sidebar-section-title-button min-w-0 flex-1 h-28px px-4px flex items-center border-none bg-transparent cursor-pointer text-left'
      onClick={onTitleClick ?? onToggle}
    >
      <span
        data-testid='sider-section-title'
        className='text-14px text-t-tertiary sider-section-title group-hover/label:text-t-primary transition-colors font-[500] leading-none'
      >
        {label}
      </span>
    </button>
    {trailing && <div className='ml-auto flex items-center gap-2px'>{trailing}</div>}
  </div>
);

type SiderSectionActionProps = {
  label: string;
  onClick?: () => void;
  testId?: string;
};

export const SiderSectionAction: React.FC<SiderSectionActionProps> = ({ label, onClick, testId }) => {
  if (!onClick) return null;
  return (
    <button
      type='button'
      data-testid={testId}
      className='w-52px h-24px shrink-0 px-6px flex items-center justify-center rd-6px border-none bg-transparent text-13px font-[500] leading-22px text-t-tertiary hover:text-t-primary hover:bg-fill-3 transition-colors cursor-pointer'
      onClick={(event) => {
        event.stopPropagation();
        onClick();
      }}
    >
      {label}
    </button>
  );
};
