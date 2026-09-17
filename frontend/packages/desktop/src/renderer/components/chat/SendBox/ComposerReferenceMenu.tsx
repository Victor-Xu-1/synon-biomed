import React, { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import type { ComposerReferenceItem, ComposerReferenceTrigger } from './composerReferenceModel';

type ComposerReferenceMenuProps = {
  trigger: ComposerReferenceTrigger;
  activeIndex: number;
  items: ComposerReferenceItem[];
  loading: boolean;
  error: boolean;
  onHoverItem: (index: number) => void;
  onSelectItem: (item: ComposerReferenceItem) => void;
};

const ComposerReferenceMenu: React.FC<ComposerReferenceMenuProps> = ({
  trigger,
  activeIndex,
  items,
  loading,
  error,
  onHoverItem,
  onSelectItem,
}) => {
  const { t } = useTranslation();
  const activeRef = useRef<HTMLButtonElement>(null);
  useEffect(() => activeRef.current?.scrollIntoView?.({ block: 'nearest' }), [activeIndex]);

  const subject = t(
    trigger === '@' ? 'conversation.composerReference.artifactsAndFiles' : 'conversation.composerReference.sessions'
  );
  const getKindLabel = (item: ComposerReferenceItem): string => {
    if (item.kind === 'workspace') return t('conversation.composerReference.kind.workspace');
    if (item.kind === 'artifact') {
      return t(
        item.isCurrentProject
          ? 'conversation.composerReference.kind.currentProjectArtifact'
          : 'conversation.composerReference.kind.otherProjectArtifact'
      );
    }
    return t(
      item.isCurrentProject
        ? 'conversation.composerReference.kind.currentProjectSession'
        : 'conversation.composerReference.kind.otherProjectSession'
    );
  };
  return (
    <div
      className='app-overlay-menu composer-reference-menu'
      role='listbox'
      aria-label={t('conversation.composerReference.referenceSubject', { subject })}
    >
      <div className='composer-reference-menu__header'>
        {t('conversation.composerReference.referenceSubject', { subject })}
      </div>
      {loading && (
        <div className='composer-reference-menu__state' role='status'>
          {t('conversation.composerReference.loadingSubject', { subject })}
        </div>
      )}
      {error && (
        <div className='composer-reference-menu__state composer-reference-menu__state--error' role='alert'>
          {t('conversation.composerReference.loadFailed', { subject })}
        </div>
      )}
      {!loading && !error && items.length === 0 && (
        <div className='composer-reference-menu__state'>{t('conversation.composerReference.empty', { subject })}</div>
      )}
      {items.map((item, index) => {
        const active = index === activeIndex;
        return (
          <button
            ref={active ? activeRef : undefined}
            type='button'
            role='option'
            aria-selected={active}
            className='composer-reference-menu__item'
            data-active={active || undefined}
            key={item.key}
            onMouseEnter={() => onHoverItem(index)}
            onMouseDown={(event) => {
              event.preventDefault();
              onSelectItem(item);
            }}
          >
            <span className='composer-reference-menu__main'>
              <span className='composer-reference-menu__label'>{item.label}</span>
              <span className='composer-reference-menu__kind'>{getKindLabel(item)}</span>
            </span>
            <span className='composer-reference-menu__detail'>{item.detail}</span>
          </button>
        );
      })}
    </div>
  );
};

export default ComposerReferenceMenu;
