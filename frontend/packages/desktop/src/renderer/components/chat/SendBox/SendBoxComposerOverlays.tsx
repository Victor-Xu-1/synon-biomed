import BtwOverlay from '@/renderer/components/chat/BtwOverlay';
import SlashCommandMenu, { type SlashCommandMenuItem } from '@/renderer/components/chat/SlashCommandMenu';
import type { useBtwCommand } from '@/renderer/components/chat/BtwOverlay/useBtwCommand';
import type { useConversationExport } from '@renderer/hooks/file/useConversationExport';
import type { useSlashCommandController } from '@/renderer/hooks/chat/useSlashCommandController';
import type { TFunction } from 'i18next';
import React from 'react';
import ComposerReferenceMenu from './ComposerReferenceMenu';
import ConversationExportFileNamePanel from './ConversationExportFileNamePanel';
import type { ActiveComposerReferenceQuery, ComposerReferenceItem } from './composerReferenceModel';

type Props = {
  activeReferenceQuery: ActiveComposerReferenceQuery | null;
  btwCommand: ReturnType<typeof useBtwCommand>;
  conversationExport: ReturnType<typeof useConversationExport>;
  insertSelectedReference: (item: ComposerReferenceItem) => void;
  isCommandMenuOpen: boolean;
  isLoading: boolean;
  isReferenceMenuOpen: boolean;
  loading?: boolean;
  referenceError: boolean;
  referenceLoading: boolean;
  referenceMenuActiveIndex: number;
  setReferenceMenuActiveIndex: (index: number) => void;
  slashController: ReturnType<typeof useSlashCommandController>;
  slashMenuItems: SlashCommandMenuItem[];
  t: TFunction;
  visibleReferenceItems: ComposerReferenceItem[];
  anchorEl: HTMLDivElement | null;
};

const SendBoxComposerOverlays = ({
  activeReferenceQuery,
  anchorEl,
  btwCommand,
  conversationExport,
  insertSelectedReference,
  isCommandMenuOpen,
  isLoading,
  isReferenceMenuOpen,
  loading,
  referenceError,
  referenceLoading,
  referenceMenuActiveIndex,
  setReferenceMenuActiveIndex,
  slashController,
  slashMenuItems,
  t,
  visibleReferenceItems,
}: Props) => (
  <>
    <BtwOverlay
      answer={btwCommand.answer}
      anchorEl={anchorEl}
      isLoading={btwCommand.isLoading}
      isOpen={btwCommand.isOpen}
      onDismiss={btwCommand.dismiss}
      parentTaskRunning={Boolean(loading || isLoading)}
      question={btwCommand.question}
    />
    {isReferenceMenuOpen && activeReferenceQuery && (
      <div className='absolute left-12px right-12px bottom-[calc(100%+8px)] z-70'>
        <ComposerReferenceMenu
          trigger={activeReferenceQuery.trigger}
          activeIndex={referenceMenuActiveIndex}
          items={visibleReferenceItems}
          loading={referenceLoading}
          error={referenceError}
          onHoverItem={setReferenceMenuActiveIndex}
          onSelectItem={insertSelectedReference}
        />
      </div>
    )}
    {isCommandMenuOpen && (
      <div className='absolute left-12px right-12px bottom-[calc(100%+8px)] z-70'>
        {conversationExport.step === 'menu' ? (
          <SlashCommandMenu
            title={t('messages.export.menuTitle')}
            hint={t('messages.export.menuHint')}
            items={conversationExport.menuItems}
            activeIndex={conversationExport.activeIndex}
            loading={conversationExport.loading}
            onHoverItem={conversationExport.setActiveIndex}
            onSelectItem={(item) => conversationExport.onSelectMenuItem(item.key)}
            emptyText={t('messages.slash.empty')}
          />
        ) : conversationExport.step === 'filename' ? (
          <ConversationExportFileNamePanel
            filename={conversationExport.filename}
            pathPreview={conversationExport.pathPreview}
            loading={conversationExport.loading}
            fileNameLabel={t('messages.export.fileNameLabel')}
            fileNamePlaceholder={t('messages.export.fileNamePlaceholder')}
            pathLabel={t('messages.export.pathLabel')}
            cancelLabel={t('common.cancel')}
            backLabel={t('common.back')}
            saveLabel={t('common.save')}
            onFilenameChange={conversationExport.setFilename}
            onKeyDown={conversationExport.handleKeyDown}
            onCancel={conversationExport.closeExportFlow}
            onBack={conversationExport.showMenu}
            onSave={() => void conversationExport.submitFilename()}
          />
        ) : (
          <SlashCommandMenu
            title={t('messages.slash.title')}
            hint={t('messages.slash.hint')}
            items={slashMenuItems}
            activeIndex={slashController.activeIndex}
            loading={false}
            onHoverItem={slashController.setActiveIndex}
            onSelectItem={(item) => {
              const targetIndex = slashController.filteredCommands.findIndex((command) => command.name === item.key);
              if (targetIndex >= 0) slashController.onSelectByIndex(targetIndex);
            }}
            emptyText={t('messages.slash.empty')}
          />
        )}
      </div>
    )}
  </>
);

export default SendBoxComposerOverlays;
