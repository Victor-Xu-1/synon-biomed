/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Input, Modal } from '@arco-design/web-react';
import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { SynonBiomedSkillMarketPanel } from './SynonBiomedSkillMarketPanel';

interface SkillMarketModalProps {
  visible: boolean;
  onClose: () => void;
  importedSkillKeys: Set<string>;
  onOpenRepository: (repo: string) => void;
  onSkillLoaded: () => Promise<void>;
}

export function SkillMarketModal({
  visible,
  onClose,
  importedSkillKeys,
  onOpenRepository,
  onSkillLoaded,
}: SkillMarketModalProps) {
  const { t } = useTranslation();
  const [query, setQuery] = useState('');
  return (
    <Modal
      visible={visible}
      title={t('settings.skillsSettings.marketplace.tab')}
      onCancel={onClose}
      footer={null}
      unmountOnExit
      style={{ width: 'min(1120px, calc(100vw - 32px))' }}
      className='settings-skill-market-dialog'
    >
      {visible ? (
        <div className='settings-skill-market-dialog__content' data-testid='skill-market-dialog'>
          <Input
            value={query}
            onChange={setQuery}
            allowClear
            placeholder={t('settings.skillsSettings.searchMarket')}
            aria-label={t('settings.skillsSettings.searchMarket')}
          />
          <SynonBiomedSkillMarketPanel
            query={query}
            importedSkillKeys={importedSkillKeys}
            onOpenRepository={(repo) => {
              onClose();
              onOpenRepository(repo);
            }}
            onOpenCustomRepository={() => {
              onClose();
              onOpenRepository('');
            }}
            onSkillLoaded={onSkillLoaded}
          />
        </div>
      ) : null}
    </Modal>
  );
}
