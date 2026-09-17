import { describe, expect, it } from 'vitest';
import type { NormalizedToolCall } from '@/common/chat/normalizeToolCall';
import { buildToolStepPublicPresentation } from '@/renderer/pages/conversation/Messages/components/toolStepSummaryModel';
import { buildToolPublicDetailPresentation } from '@/renderer/pages/conversation/Messages/toolDetails/detailProjection';

const tool = (input: object, output: object = {}): NormalizedToolCall => ({
  key: 'receipt',
  name: 'web_fetch',
  status: 'completed',
  input: JSON.stringify(input),
  output: JSON.stringify(output),
});

describe('retrieval presentation', () => {
  it('prefers explicit subjects and preserves distinguishable public URL paths', () => {
    expect(
      buildToolStepPublicPresentation(tool({ url: 'https://example.org/articles/123', title: '随访研究' }), 'zh-CN')
        .detail
    ).toBe('随访研究');
    expect(
      buildToolStepPublicPresentation(tool({ url: 'https://example.org/articles/123?token=private' }), 'zh-CN').detail
    ).toBe('example.org · articles/123');
  });
  it('flags contradictory requested sources without claiming a redirect or changing the result', () => {
    const item = tool(
      { url: 'https://example.org/article/123' },
      {
        requested_url: 'https://other.example/paper/456',
        url: 'https://other.example/paper/456',
        bytes_read: 2998,
        complete: true,
        content_type: 'text/html',
        body_sha256: 'abc123',
        body_hash_scope: 'returned_response_bytes',
        etag: 'opaque',
      }
    );
    const compact = buildToolStepPublicPresentation(item, 'zh-CN');
    expect(compact.resultSummary).toBe('来源待核对');
    const detail = buildToolPublicDetailPresentation(item, 'zh-CN', compact.resultSummary);
    expect(detail.notices).toContain('请求来源与结果回执不一致，请核对详情；不能据此视为同一来源。');
    expect(detail.resultRows).toContainEqual({ label: '已读取', value: '2.9 KB' });
    expect(detail.resultRows.some((row) => /Hash|Sha256|Etag|校验|缓存标识/.test(row.label))).toBe(false);
    expect(JSON.stringify(detail.outputTree)).toContain('缓存标识');
    expect(JSON.stringify(detail)).not.toContain('Body Hash Scope');
  });
  it('distinguishes partial responses, unavailable full text and a returned resource', () => {
    expect(buildToolStepPublicPresentation(tool({}, { bytes_read: 100, complete: false }), 'zh-CN').resultSummary).toBe(
      '部分内容 · 100 B'
    );
    expect(
      buildToolStepPublicPresentation(tool({}, { available: false, reason: 'no_open_access_full_text' }), 'zh-CN')
        .resultSummary
    ).toBe('全文不可用');
    expect(buildToolStepPublicPresentation(tool({}, { bytes_read: 0, complete: true }), 'zh-CN').resultSummary).toBe(
      '已读取 0 B'
    );
  });
  it('formats limits and sanitizes source URLs at every disclosure level', () => {
    const item = tool(
      { url: 'https://user:password@example.org/a?token=secret#token=secret', limit: 4194304 },
      { url: 'https://user:password@example.org/a?token=secret#token=secret', bytes_read: 1024 }
    );
    const detail = buildToolPublicDetailPresentation(item, 'zh-CN', null);
    expect(detail.inputRows).toContainEqual({ label: '读取上限', value: '4.0 MB' });
    expect(JSON.stringify(detail)).not.toContain('token=secret');
    expect(JSON.stringify(detail)).not.toContain('user:password');
  });
});
