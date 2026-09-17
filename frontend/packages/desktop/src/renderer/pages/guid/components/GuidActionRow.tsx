/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { getCleanFileNames } from '@/renderer/services/FileService';
import type { FileMetadata } from '@/renderer/services/FileService';
import type { IConversationMcpStatus } from '@/common/config/storage';
import FileAttachButton from '@/renderer/components/media/FileAttachButton';
import { Button, Tooltip } from '@arco-design/web-react';
import { ArrowUp } from '@icon-park/react';
import React, { useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import styles from '../index.module.css';
import '@/renderer/components/chat/SendBox/sendbox.css';
import type { GuidLocalFile } from '../hooks/useGuidInput';

type GuidActionRowProps = {
  // File handling
  files: string[];
  localFiles?: GuidLocalFile[];
  onFilesUploaded: (paths: string[]) => void;
  onLocalFilesSelected?: (files: File[]) => void | Promise<void>;
  openFileSelector: () => void;
  onSelectProjectFiles: () => void;
  loadedSkills?: string[];
  loadedMcpStatuses?: IConversationMcpStatus[];
  selectedSkillNames?: string[];
  selectedMcpServerIds?: string[];
  onSelectSkill?: (name: string) => void;
  onSelectMcpServer?: (server: IConversationMcpStatus) => void;

  // Model selector node (rendered by parent)
  modelSelectorNode: React.ReactNode;
  sessionOptionsNode?: React.ReactNode;
  permissionNode?: React.ReactNode;

  // Send button
  loading: boolean;
  isButtonDisabled: boolean;
  speechInputNode?: React.ReactNode;
  sendOptionsNode?: React.ReactNode;
  onSend: () => void;
};

const GuidActionRow: React.FC<GuidActionRowProps> = ({
  files,
  localFiles = [],
  onFilesUploaded,
  onLocalFilesSelected,
  openFileSelector,
  onSelectProjectFiles,
  loadedSkills,
  loadedMcpStatuses,
  selectedSkillNames,
  selectedMcpServerIds,
  onSelectSkill,
  onSelectMcpServer,
  modelSelectorNode,
  sessionOptionsNode,
  permissionNode,
  loading,
  isButtonDisabled,
  speechInputNode,
  sendOptionsNode,
  onSend,
}) => {
  const { t } = useTranslation();
  const handleLocalFilesAdded = useCallback(
    (addedFiles: FileMetadata[]) => {
      const paths = addedFiles.map((file) => file.path);
      if (paths.length > 0) onFilesUploaded(paths);
    },
    [onFilesUploaded]
  );

  return (
    <div className={styles.actionRow}>
      <div className={styles.actionTools}>
        <div className={styles.actionEntry}>
          <span className='flex items-center gap-4px lh-[1]'>
            <FileAttachButton
              synonBiomedV11
              openFileSelector={openFileSelector}
              onSelectProjectFiles={onSelectProjectFiles}
              onLocalFilesAdded={handleLocalFilesAdded}
              onLocalFilesSelected={onLocalFilesSelected}
              loadedSkills={loadedSkills}
              loadedMcpStatuses={loadedMcpStatuses}
              selectedSkillNames={selectedSkillNames}
              selectedMcpServerIds={selectedMcpServerIds}
              onSelectSkill={onSelectSkill}
              onSelectMcpServer={onSelectMcpServer}
              triggerTestId='file-upload-btn'
              showUnavailableConversationActions
            />
            {files.length + localFiles.length > 0 && (
              <Tooltip
                className='!max-w-max'
                content={
                  <span className='whitespace-break-spaces'>
                    {[...getCleanFileNames(files), ...localFiles.map(({ file }) => file.name)].join('\n')}
                  </span>
                }
              >
                <span className='text-t-primary'>
                  {t('common.fileAttach.fileCount', { count: files.length + localFiles.length })}
                </span>
              </Tooltip>
            )}
          </span>
        </div>
        {sessionOptionsNode}
        {permissionNode}
      </div>
      <div className={styles.actionSubmit}>
        {modelSelectorNode ? <div className={styles.actionConfigGroup}>{modelSelectorNode}</div> : null}

        {speechInputNode}
        {sendOptionsNode ? (
          <div className='sendbox-joined-actions'>
            <Button
              shape='circle'
              type='primary'
              loading={loading}
              disabled={isButtonDisabled}
              className='send-button-custom'
              icon={<ArrowUp theme='filled' size='14' fill='white' strokeWidth={5} className='sendbox-action-icon' />}
              onClick={onSend}
              data-testid='guid-send-btn'
            />
            <span className='sendbox-joined-actions__divider' aria-hidden='true' />
            {sendOptionsNode}
          </div>
        ) : (
          <Button
            shape='circle'
            type='primary'
            loading={loading}
            disabled={isButtonDisabled}
            className='send-button-custom'
            style={{
              backgroundColor: isButtonDisabled ? undefined : '#000000',
              borderColor: isButtonDisabled ? undefined : '#000000',
            }}
            icon={<ArrowUp theme='filled' size='14' fill='white' strokeWidth={5} className='sendbox-action-icon' />}
            onClick={onSend}
            data-testid='guid-send-btn'
          />
        )}
      </div>
    </div>
  );
};

export default GuidActionRow;
