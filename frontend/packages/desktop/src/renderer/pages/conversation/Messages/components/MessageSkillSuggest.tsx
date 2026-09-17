/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { ISkillSuggestArtifact } from '@/common/adapter/ipcBridge';
import React from 'react';
import { useTranslation } from 'react-i18next';
import SkillSuggestCard from './SkillSuggestCard';

type NormalizedSkillSuggestion = {
  name: string;
  description: string;
  content: string;
};

export function normalizeSkillSuggestionPayload(value: unknown): NormalizedSkillSuggestion | null {
  if (typeof value === 'string') {
    try {
      return normalizeSkillSuggestionPayload(JSON.parse(value));
    } catch {
      return null;
    }
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  const name = typeof record.name === 'string' ? record.name.trim() : '';
  const description = typeof record.description === 'string' ? record.description.trim() : '';
  const contentValue = record.skillContent ?? record.skill_content;
  const content = typeof contentValue === 'string' ? contentValue.trim() : '';
  return name && content ? { name, description, content } : null;
}

const MessageSkillSuggest: React.FC<{ artifact: ISkillSuggestArtifact }> = ({ artifact }) => {
  const { t } = useTranslation();
  const suggestion = normalizeSkillSuggestionPayload(artifact.payload);

  if (!suggestion) {
    return (
      <div
        data-testid='message-skill-suggest-invalid'
        className='w-full mx-auto rd-6px border border-danger-3 px-12px py-10px text-12px text-danger-6'
        role='alert'
      >
        {t('cron.skill.invalidSuggestion')}
      </div>
    );
  }

  return (
    <div data-testid='message-skill-suggest' className='w-full mx-auto'>
      <SkillSuggestCard
        artifact_id={artifact.id}
        conversation_id={artifact.conversation_id}
        suggestion={{
          name: suggestion.name,
          description: suggestion.description,
          content: suggestion.content,
        }}
      />
    </div>
  );
};

export default MessageSkillSuggest;
