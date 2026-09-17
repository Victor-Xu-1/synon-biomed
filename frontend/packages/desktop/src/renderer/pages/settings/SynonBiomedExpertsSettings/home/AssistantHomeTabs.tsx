/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { isSynonBiomedAssistant } from '@/common/types/agent/assistantTypes';
import type { AssistantListItem } from '../types';
import MyAssistantRow from './MyAssistantRow';
import { type AssistantListFilter, filterAssistants } from '../assistantUtils';
import { useLayoutContext } from '@/renderer/hooks/context/LayoutContext';
import type { DragEndEvent } from '@dnd-kit/core';
import { DndContext, PointerSensor, closestCenter, useSensor, useSensors } from '@dnd-kit/core';
import { SortableContext, verticalListSortingStrategy } from '@dnd-kit/sortable';
import { Button, Dropdown, Input, Menu } from '@arco-design/web-react';
import { Down, Search, SortTwo } from '@icon-park/react';
import classNames from 'classnames';
import React, { useCallback, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

type AssistantHomeTabsProps = {
  assistants: AssistantListItem[];
  localeKey: string;
  onOpenSettings: (assistant: AssistantListItem) => void;
  onToggleEnabled: (assistant: AssistantListItem, checked: boolean) => void;
  onReorder: (activeId: string, overId: string) => void | Promise<void>;
  onStartChat: (assistant: AssistantListItem) => void;
  onCreate: () => void;
};

const FILTER_OPTIONS: AssistantListFilter[] = ['all', 'enabled', 'disabled', 'builtin', 'user'];

const bySortOrder = (left: AssistantListItem, right: AssistantListItem) => left.sort_order - right.sort_order;

const AssistantHomeTabs: React.FC<AssistantHomeTabsProps> = ({
  assistants,
  localeKey,
  onOpenSettings,
  onToggleEnabled,
  onReorder,
  onStartChat,
  onCreate,
}) => {
  const { t } = useTranslation();
  const layout = useLayoutContext();
  const isMobile = layout?.isMobile ?? false;
  const [filter, setFilter] = useState<AssistantListFilter>('all');
  const [query, setQuery] = useState('');
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 8 } }));

  const synonBiomedAssistants = useMemo(
    () => filterAssistants(assistants.filter(isSynonBiomedAssistant).toSorted(bySortOrder), query, filter, localeKey),
    [assistants, filter, localeKey, query]
  );
  const reorderEnabled = filter === 'all' && query.trim() === '';

  const handleDragEnd = useCallback(
    (event: DragEndEvent) => {
      const { active, over } = event;
      if (!reorderEnabled || !over || active.id === over.id) return;
      void onReorder(String(active.id), String(over.id));
    },
    [onReorder, reorderEnabled]
  );

  const filterMenu = (
    <Menu onClickMenuItem={(key) => setFilter(key as AssistantListFilter)}>
      {FILTER_OPTIONS.map((option) => (
        <Menu.Item key={option} data-testid={`synon-biomed-assistant-filter-${option}`}>
          {assistantFilterLabel(t, option)}
        </Menu.Item>
      ))}
    </Menu>
  );

  return (
    <div data-testid='assistant-home-shell' className='flex h-full min-h-0 flex-col overflow-hidden bg-transparent'>
      <div
        className={`border-b border-arco-2 bg-base ${isMobile ? 'px-16px py-14px' : 'px-12px py-24px md:px-40px md:py-32px'}`}
      >
        <div className='mx-auto w-full max-w-800px'>
          <div className='flex w-full items-center justify-between gap-12px sm:gap-16px'>
            <h1
              className={classNames(
                'm-0 min-w-0 flex-1 font-bold text-t-primary',
                isMobile ? 'text-22px leading-[1.2]' : 'text-28px leading-[1.15]'
              )}
            >
              {t('settings.synonBiomedAssistantsTitle')}
            </h1>
            <Dropdown droplist={filterMenu} trigger='click' position='br'>
              <Button
                size='small'
                data-testid='synon-biomed-assistant-enabled-filter'
                className='!flex !shrink-0 !items-center !gap-6px !rounded-8px'
              >
                <span>{assistantFilterLabel(t, filter)}</span>
                <Down theme='outline' size={14} fill='currentColor' />
              </Button>
            </Dropdown>
          </div>
          <p
            className={classNames(
              'm-0 mt-8px w-full text-t-secondary',
              isMobile ? 'text-13px leading-20px' : 'text-14px leading-22px'
            )}
          >
            {t('settings.synonBiomedAssistantsDescription')}
          </p>
          <div className='mt-14px flex items-center gap-8px'>
            <Input
              value={query}
              onChange={setQuery}
              allowClear
              prefix={<Search theme='outline' size={14} />}
              placeholder={t('settings.synonBiomedAssistantsSearchPlaceholder')}
              aria-label={t('settings.searchAssistants')}
            />
            <Button type='primary' onClick={onCreate} className='shrink-0' data-testid='btn-create-synon-biomed-expert'>
              {t('settings.synonBiomedAddExpert')}
            </Button>
          </div>
        </div>
      </div>

      <div
        data-testid='assistant-home-body'
        className={`min-h-0 flex-1 overflow-auto ${isMobile ? 'px-16px pb-14px pt-14px' : 'px-12px pb-24px pt-18px md:px-40px'}`}
      >
        <div className='mx-auto w-full max-w-800px'>
          <div className='mb-10px flex items-center justify-between gap-12px'>
            <span className='inline-flex min-w-0 items-center gap-6px text-12px text-t-tertiary'>
              <SortTwo
                theme='outline'
                size={14}
                fill='currentColor'
                className='block shrink-0 leading-none text-t-quaternary'
                style={{ lineHeight: 0 }}
              />
              <span className='truncate'>{t('settings.synonBiomedAssistantsHint')}</span>
            </span>
          </div>

          {synonBiomedAssistants.length > 0 ? (
            <div className='rounded-12px border border-arco-2 bg-2 p-8px md:rounded-16px md:p-10px'>
              <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={handleDragEnd}>
                <SortableContext
                  items={synonBiomedAssistants.map((assistant) => assistant.id)}
                  strategy={verticalListSortingStrategy}
                >
                  <div className='space-y-8px'>
                    {synonBiomedAssistants.map((assistant) => (
                      <MyAssistantRow
                        key={assistant.id}
                        assistant={assistant}
                        localeKey={localeKey}
                        draggable={reorderEnabled}
                        onOpenDetail={onOpenSettings}
                        onToggleEnabled={onToggleEnabled}
                        onStartChat={onStartChat}
                        toggleDisabled={assistant.source !== 'user'}
                      />
                    ))}
                  </div>
                </SortableContext>
              </DndContext>
            </div>
          ) : (
            <div
              data-testid='synon-biomed-assistants-empty'
              className='rounded-12px border border-dashed border-arco-2 bg-fill-1/40 px-20px py-28px text-center text-12px text-t-secondary'
            >
              {t('settings.synonBiomedAssistantsEmpty')}
            </div>
          )}
        </div>
      </div>
    </div>
  );
};

function assistantFilterLabel(t: (key: string) => string, filter: AssistantListFilter): string {
  if (filter === 'builtin') return t('settings.assistantFilterBuiltin');
  if (filter === 'user') return t('settings.assistantFilterCustom');
  return t(`settings.assistantFilter.${filter}`);
}

export default AssistantHomeTabs;
