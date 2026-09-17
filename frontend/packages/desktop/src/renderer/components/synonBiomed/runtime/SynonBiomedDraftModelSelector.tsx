/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { AcpModelInfo } from '@/common/types/platform/acpTypes';
import type { AgentRuntimeDerivedOption } from '@/renderer/utils/synonBiomed/runtime/runtimeCatalog';
import { Button, Dropdown } from '@arco-design/web-react';
import { Check, Down, Right } from '@icon-park/react';
import React, { useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

type SynonBiomedDraftModelSelectorProps = {
  modelInfo: AcpModelInfo | null;
  selectedModelId: string | null;
  onSelectModel: (modelId: string) => void;
  thoughtLevelOption?: AgentRuntimeDerivedOption | null;
  selectedThoughtLevel?: string;
  onSelectThoughtLevel?: (value: string) => void;
  onOpenModelSettings?: () => void;
};

const clamp = (value: number, minimum: number, maximum: number) => Math.min(Math.max(value, minimum), maximum);

const SynonBiomedDraftModelSelector: React.FC<SynonBiomedDraftModelSelectorProps> = ({
  modelInfo,
  selectedModelId,
  onSelectModel,
  thoughtLevelOption,
  selectedThoughtLevel,
  onSelectThoughtLevel,
  onOpenModelSettings,
}) => {
  const { t } = useTranslation();
  const [visible, setVisible] = useState(false);
  const [effortVisible, setEffortVisible] = useState(false);
  const menuSurfaceRef = useRef<HTMLDivElement>(null);
  const effortMenuRef = useRef<HTMLDivElement>(null);
  const [effortMenuPosition, setEffortMenuPosition] = useState<React.CSSProperties>();
  const models = modelInfo?.available_models ?? [];
  const resolvedModelId = selectedModelId || modelInfo?.current_model_id || models[0]?.id || '';
  const effortOptions = thoughtLevelOption?.options ?? [];
  const resolvedEffortValue = selectedThoughtLevel || thoughtLevelOption?.currentValue || effortOptions[0]?.value || '';
  const resolvedEffort = effortOptions.find((option) => option.value === resolvedEffortValue);
  const hasPopupContent = models.length > 0 || effortOptions.length > 0 || Boolean(onOpenModelSettings);
  const displayLabel = useMemo(() => {
    const selected = models.find((model) => model.id === resolvedModelId);
    return (
      selected?.label ||
      selected?.id ||
      modelInfo?.current_model_label ||
      modelInfo?.current_model_id ||
      t('common.defaultModel')
    );
  }, [modelInfo?.current_model_id, modelInfo?.current_model_label, models, resolvedModelId, t]);

  useLayoutEffect(() => {
    if (!effortVisible) return;

    const updateEffortMenuPosition = () => {
      const menuRect = menuSurfaceRef.current?.getBoundingClientRect();
      const effortRect = effortMenuRef.current?.getBoundingClientRect();
      if (!menuRect || !effortRect) return;

      const viewportWidth = window.innerWidth;
      const viewportHeight = window.innerHeight;
      const viewportInset = 12;
      const gap = 8;
      const canOpenRight = menuRect.right + gap + effortRect.width <= viewportWidth - viewportInset;
      const canOpenLeft = menuRect.left - gap - effortRect.width >= viewportInset;
      const preferredLeft = canOpenRight || !canOpenLeft ? menuRect.width + gap : -effortRect.width - gap;
      const left = clamp(
        preferredLeft,
        viewportInset - menuRect.left,
        viewportWidth - viewportInset - effortRect.width - menuRect.left
      );
      const top = clamp(
        menuRect.height - effortRect.height,
        viewportInset - menuRect.top,
        viewportHeight - viewportInset - effortRect.height - menuRect.top
      );
      setEffortMenuPosition({ left, top });
    };

    updateEffortMenuPosition();
    window.addEventListener('resize', updateEffortMenuPosition);
    const resizeObserver =
      typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(updateEffortMenuPosition);
    if (menuSurfaceRef.current) resizeObserver?.observe(menuSurfaceRef.current);
    if (effortMenuRef.current) resizeObserver?.observe(effortMenuRef.current);

    return () => {
      window.removeEventListener('resize', updateEffortMenuPosition);
      resizeObserver?.disconnect();
    };
  }, [effortVisible]);

  const trigger = (
    <Button
      type='text'
      size='small'
      className='sendbox-model-btn !h-32px !px-8px !text-13px'
      data-testid='synonbiomed-draft-model-selector'
      aria-label={`${t('common.model')}: ${displayLabel}`}
    >
      <span className='inline-flex min-w-0 items-center gap-4px'>
        <span className='max-w-150px truncate'>{displayLabel}</span>
        {resolvedEffort ? <span className='max-w-90px truncate text-t-secondary'>{resolvedEffort.label}</span> : null}
        {hasPopupContent ? <Down theme='outline' size={12} /> : null}
      </span>
    </Button>
  );

  if (!hasPopupContent) return trigger;

  const selectModel = (modelId: string) => {
    onSelectModel(modelId);
    setVisible(false);
    setEffortVisible(false);
  };

  const selectEffort = (value: string) => {
    onSelectThoughtLevel?.(value);
    setVisible(false);
    setEffortVisible(false);
  };

  const dropdownContent = (
    <div className='relative' data-testid='synonbiomed-model-selector-menu'>
      <div
        ref={menuSurfaceRef}
        role='menu'
        aria-label={t('conversation.synonRuntime.modelSelector.title')}
        className='app-overlay-menu box-border overflow-y-auto rd-14px p-8px'
        style={{
          width: 'min(310px, calc(100vw - 24px))',
          maxWidth: 'calc(100vw - 24px)',
          maxHeight: 'calc(100vh - 24px)',
          opacity: 1,
        }}
      >
        <div className='max-h-360px overflow-y-auto'>
          {models.map((model) => {
            const selected = model.id === resolvedModelId;
            return (
              <button
                key={model.id}
                type='button'
                role='menuitemradio'
                aria-checked={selected}
                data-testid={`synonbiomed-model-option-${model.id}`}
                onClick={() => selectModel(model.id)}
                className='flex w-full items-center justify-between gap-12px rd-10px border-0 bg-transparent px-12px py-10px text-left text-t-primary outline-none hover:bg-fill-2 focus-visible:ring-2 focus-visible:ring-primary/30'
              >
                <span className='min-w-0 flex-1'>
                  <span className='block truncate text-15px font-500'>{model.label || model.id}</span>
                  {model.description ? (
                    <span className='mt-2px block text-13px leading-18px text-t-secondary'>{model.description}</span>
                  ) : null}
                </span>
                {selected ? <Check theme='outline' size={18} className='shrink-0 text-primary' /> : null}
              </button>
            );
          })}
        </div>

        {effortOptions.length > 0 || onOpenModelSettings ? (
          <div role='separator' className='mx-4px my-6px h-1px bg-2' />
        ) : null}

        {effortOptions.length > 0 ? (
          <button
            type='button'
            role='menuitem'
            aria-haspopup='menu'
            aria-expanded={effortVisible}
            data-testid='synonbiomed-effort-selector'
            onClick={(event) => {
              event.stopPropagation();
              setEffortVisible((current) => !current);
            }}
            className={`flex w-full items-center justify-between rd-10px border-0 px-12px py-10px text-left outline-none hover:bg-fill-2 focus-visible:ring-2 focus-visible:ring-primary/30 ${
              effortVisible ? 'bg-fill-2' : 'bg-transparent'
            }`}
          >
            <span className='text-15px text-t-primary'>{t('conversation.synonRuntime.modelSelector.effort')}</span>
            <span className='flex min-w-0 items-center gap-8px text-14px text-t-secondary'>
              <span className='max-w-120px truncate'>{resolvedEffort?.label || resolvedEffortValue}</span>
              <Right theme='outline' size={15} />
            </span>
          </button>
        ) : null}

        {onOpenModelSettings ? (
          <button
            type='button'
            role='menuitem'
            data-testid='synonbiomed-more-models'
            onClick={() => {
              setVisible(false);
              setEffortVisible(false);
              onOpenModelSettings();
            }}
            className='flex w-full items-center justify-between rd-10px border-0 bg-transparent px-12px py-10px text-left text-15px text-t-primary outline-none hover:bg-fill-2 focus-visible:ring-2 focus-visible:ring-primary/30'
          >
            <span>{t('conversation.synonRuntime.modelSelector.moreModels')}</span>
            <Right theme='outline' size={15} className='text-t-secondary' />
          </button>
        ) : null}
      </div>

      {effortVisible ? (
        <div
          ref={effortMenuRef}
          role='menu'
          aria-label={t('conversation.synonRuntime.modelSelector.effort')}
          data-testid='synonbiomed-effort-menu'
          className='app-overlay-menu absolute box-border overflow-y-auto rd-14px p-8px'
          style={{
            width: 'min(340px, calc(100vw - 24px))',
            maxWidth: 'calc(100vw - 24px)',
            maxHeight: 'calc(100vh - 24px)',
            opacity: 1,
            ...effortMenuPosition,
          }}
        >
          <p className='m-0 px-12px pb-8px pt-4px text-13px leading-18px text-t-secondary'>
            {t('conversation.synonRuntime.modelSelector.effortDescription')}
          </p>
          {effortOptions.map((option) => {
            const selected = option.value === resolvedEffortValue;
            return (
              <button
                key={option.value}
                type='button'
                role='menuitemradio'
                aria-checked={selected}
                data-testid={`synonbiomed-effort-option-${option.value}`}
                onClick={(event) => {
                  event.stopPropagation();
                  selectEffort(option.value);
                }}
                className='flex w-full items-center justify-between gap-12px rd-10px border-0 bg-transparent px-12px py-9px text-left outline-none hover:bg-fill-2 focus-visible:ring-2 focus-visible:ring-primary/30'
              >
                <span className='min-w-0 flex-1'>
                  <span className='block text-15px text-t-primary'>{option.label}</span>
                  {option.description ? (
                    <span className='mt-1px block text-12px leading-17px text-t-secondary'>{option.description}</span>
                  ) : null}
                </span>
                {selected ? <Check theme='outline' size={18} className='shrink-0 text-primary' /> : null}
              </button>
            );
          })}
        </div>
      ) : null}
    </div>
  );

  return (
    <Dropdown
      trigger='click'
      position='tl'
      popupVisible={visible}
      onVisibleChange={(next) => {
        setVisible(next);
        if (!next) setEffortVisible(false);
      }}
      droplist={dropdownContent}
    >
      {trigger}
    </Dropdown>
  );
};

export default SynonBiomedDraftModelSelector;
