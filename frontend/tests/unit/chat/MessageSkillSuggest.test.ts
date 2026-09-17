import { describe, expect, it } from 'vitest';
import { normalizeSkillSuggestionPayload } from '@/renderer/pages/conversation/Messages/components/MessageSkillSuggest';

describe('normalizeSkillSuggestionPayload', () => {
  it('accepts the current payload and the legacy snake-case content field', () => {
    expect(
      normalizeSkillSuggestionPayload({
        cron_job_id: 'job-1',
        name: 'Literature Review',
        description: 'Review a topic',
        skill_content: '# Instructions',
      })
    ).toEqual({
      name: 'Literature Review',
      description: 'Review a topic',
      content: '# Instructions',
    });
  });

  it('rejects malformed, null, and incomplete artifacts instead of rendering an empty save action', () => {
    expect(normalizeSkillSuggestionPayload(null)).toBeNull();
    expect(normalizeSkillSuggestionPayload('{invalid json')).toBeNull();
    expect(normalizeSkillSuggestionPayload({ name: 'Missing content' })).toBeNull();
    expect(normalizeSkillSuggestionPayload({ cron_job_id: 'job-1', name: 'Empty', skillContent: '   ' })).toBeNull();
  });
});
