import { describe, expect, it } from 'vitest';
import type { IMessageAcpToolCall, IMessageToolCall, IMessageToolGroup } from '@/common/chat/chatLib';
import { normalizeAcpToolCall, normalizeToolCall, normalizeToolGroup } from '@/common/chat/normalizeToolCall';
import { buildToolStepPublicPresentation } from '@/renderer/pages/conversation/Messages/components/toolStepSummaryModel';

const artifactId = `large-tool-result-${'a'.repeat(32)}`;
const versionId = 'ltr-11111111-1111-4111-8111-111111111111';
const contentUrl = `/api/artifacts/${artifactId}/versions/${versionId}`;
const reference = (preview: unknown, overrides: Record<string, unknown> = {}) => ({
  artifact_id: artifactId,
  version_id: versionId,
  content_url: contentUrl,
  content_type: 'application/json',
  outcome: 'succeeded',
  truncated: true,
  preview: typeof preview === 'string' ? preview : JSON.stringify(preview),
  ...overrides,
});
const searchView = (overrides: Record<string, unknown> = {}) => ({
  view_format: 'search-results-display-lines',
  source_version_id: versionId,
  source_count: 11,
  content: '1\tQuery: public evidence\n2\tDiagnostics: {"returnedResults":0}',
  ...overrides,
});
const normalize = (output: unknown) =>
  normalizeToolCall({
    id: 'search-message',
    conversation_id: 'search-task',
    type: 'tool_call',
    content: { call_id: 'search-call', name: 'web_search', status: 'completed', output: JSON.stringify(output) },
  } as IMessageToolCall)!;

describe('immutable large tool result normalization', () => {
  it('uses the same descriptor metadata for ACP and grouped tool messages', () => {
    const output = JSON.stringify(reference(searchView()));
    const acp = normalizeAcpToolCall({
      id: 'acp-message',
      conversation_id: 'search-task',
      type: 'acp_tool_call',
      content: {
        update: {
          tool_call_id: 'acp-call',
          title: 'web_search',
          kind: 'search',
          status: 'completed',
          content: [{ type: 'content', content: { type: 'text', text: output } }],
        },
      },
    } as IMessageAcpToolCall);
    const grouped = normalizeToolGroup({
      id: 'group-message',
      conversation_id: 'search-task',
      type: 'tool_group',
      content: [{ call_id: 'group-call', name: 'web_search', status: 'Success', result_display: output }],
    } as IMessageToolGroup)[0];
    for (const [tool, messageId] of [
      [acp, 'acp-message'],
      [grouped, 'group-message'],
    ] as const) {
      expect(tool).toMatchObject({ truncated: true, compactResultCount: 11, messageId, conversationId: 'search-task' });
    }
  });
  it('recognizes a canonical search descriptor without a separate history compact marker', () => {
    const tool = normalize(reference(searchView()));
    expect(tool.truncated).toBe(true);
    expect(tool.compactResultCount).toBe(11);
    expect(buildToolStepPublicPresentation(tool, 'en-US')).toMatchObject({
      resultSummary: '11 results',
      shouldLoadFull: true,
      showResearchSources: true,
    });
    expect(buildToolStepPublicPresentation(tool, 'zh-CN').resultSummary).toBe('11 条结果');
  });

  it.each([
    '{"content":"incomplete',
    searchView({ source_version_id: 'ltr-22222222-2222-4222-8222-222222222222' }),
    searchView({ view_format: 'unrecognized-view' }),
    searchView({ source_count: -1 }),
    searchView({ source_count: '11' }),
    searchView({ source_count: Number.MAX_SAFE_INTEGER + 1 }),
    { diagnostics: { returnedResults: 17 } },
  ])('does not turn unavailable source metadata into an empty or invented search result', (preview) => {
    const tool = normalize(reference(preview));
    expect(tool.truncated).toBe(true);
    expect(tool.compactResultCount).toBeUndefined();
    expect(buildToolStepPublicPresentation(tool, 'en-US').resultSummary).toBe('Search details pending');
  });

  it.each([
    { content_url: 'https://other.example/result' },
    { content_url: '//other.example/result' },
    { content_url: `${contentUrl}?redirect=elsewhere` },
    { artifact_id: `large-tool-result-${'b'.repeat(32)}` },
    { version_id: 'ltr-22222222-2222-4222-8222-222222222222' },
    { truncated: false },
  ])('does not hydrate a descriptor with mismatched identity or a noncanonical URL', (overrides) => {
    const tool = normalize(reference(searchView(), overrides));
    expect(tool.truncated).toBe(false);
    expect(tool.compactResultCount).toBeUndefined();
  });

  it('preserves semantic failure and an actual zero-source count', () => {
    expect(
      buildToolStepPublicPresentation(normalize(reference(searchView(), { outcome: 'failed' })), 'en-US').resultSummary
    ).toBe('Failed');
    expect(
      buildToolStepPublicPresentation(normalize(reference(searchView({ source_count: 0 }))), 'en-US').resultSummary
    ).toBe('0 results');
  });
});
