/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import { iconColors } from '@/renderer/styles/colors';
import { useUpdateConversationArtifactStatus } from '@renderer/pages/conversation/Messages/artifacts';
import { Button, Message } from '@arco-design/web-react';
import { Down, Lightning, Up } from '@icon-park/react';
import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';
import MarkdownView from '@renderer/components/Markdown';
import type { SkillSuggestion } from '@renderer/utils/chat/skillSuggestParser';
import { saveSynonBiomedConversationAsSkill } from '@/renderer/services/skills/synonBiomedSkillLibrary';

interface SkillSuggestCardProps {
  artifact_id: string;
  conversation_id: string;
  suggestion: SkillSuggestion;
}

const CODE_STYLE = { marginTop: 4, marginBlock: 4 };

const SkillSuggestCard: React.FC<SkillSuggestCardProps> = ({ artifact_id, conversation_id, suggestion }) => {
  const { t } = useTranslation();
  const updateArtifactStatus = useUpdateConversationArtifactStatus();
  const [saving, setSaving] = useState(false);
  const [dismissing, setDismissing] = useState(false);
  const [saved, setSaved] = useState(false);
  const [dismissed, setDismissed] = useState(false);
  const [expanded, setExpanded] = useState(false);

  if (dismissed || saved) return null;

  const handleSave = async () => {
    setSaving(true);
    try {
      await saveSynonBiomedConversationAsSkill(conversation_id);
      updateArtifactStatus(artifact_id, 'saved');
      setSaved(true);
      Message.success(t('cron.skill.saveSuccess'));
    } catch (err) {
      Message.error(t('cron.skill.saveFailed'));
      console.error('[SkillSuggestCard] Failed to save skill:', err);
    } finally {
      setSaving(false);
    }
  };

  const handleDismiss = async () => {
    setDismissing(true);
    try {
      await ipcBridge.conversation.updateArtifact.invoke({
        conversation_id,
        artifact_id,
        status: 'dismissed',
      });
      updateArtifactStatus(artifact_id, 'dismissed');
      setDismissed(true);
    } catch (error) {
      Message.error(t('cron.skill.dismissFailed'));
      console.error('[SkillSuggestCard] Failed to dismiss artifact:', error);
    } finally {
      setDismissing(false);
    }
  };

  return (
    <div
      data-testid='skill-suggest-card'
      className='mt-8px p-12px rd-8px bg-fill-0 b-1 b-solid'
      style={{ borderColor: 'color-mix(in srgb, var(--color-border-2) 70%, transparent)' }}
    >
      <div className='flex items-center gap-6px mb-8px'>
        <Lightning theme='filled' size={16} fill={iconColors.warning} />
        <span className='font-500 text-14px'>{t('cron.skill.turnIntoSkill')}</span>
      </div>
      <div className='text-t-primary text-13px mb-4px'>{suggestion.name}</div>
      <div className='text-t-secondary text-12px mb-8px'>{suggestion.description}</div>

      {/* Expandable preview */}
      <button
        type='button'
        className='flex items-center gap-4px border-0 bg-transparent p-0 text-12px text-t-secondary cursor-pointer hover:text-t-primary mb-8px select-none'
        aria-expanded={expanded}
        onClick={() => setExpanded(!expanded)}
      >
        {expanded ? <Up size={12} /> : <Down size={12} />}
        <span>{t('cron.skill.preview')}</span>
      </button>
      {expanded && (
        <div className='mb-12px p-8px rd-4px bg-3 max-h-240px overflow-y-auto text-12px'>
          <MarkdownView codeStyle={CODE_STYLE}>{`\`\`\`markdown\n${suggestion.content}\n\`\`\``}</MarkdownView>
        </div>
      )}

      <div className='flex gap-8px'>
        <Button type='primary' size='small' loading={saving} disabled={dismissing} onClick={() => void handleSave()}>
          {t('cron.skill.save')}
        </Button>
        <Button size='small' loading={dismissing} disabled={saving} onClick={() => void handleDismiss()}>
          {t('cron.skill.dismiss')}
        </Button>
      </div>
    </div>
  );
};

export default SkillSuggestCard;
