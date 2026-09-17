import { beforeEach, describe, expect, it, vi } from 'vitest';

const requestJson = vi.hoisted(() => vi.fn());

vi.mock('@/renderer/services/synonBiomedHttp', () => ({
  requestSynonBiomedJson: requestJson,
}));

import { loadLightweightOfficePreview } from '@/renderer/services/lightweightOfficePreview';

describe('lightweight Office preview service', () => {
  beforeEach(() => requestJson.mockReset());

  it('uses the owned project artifact and version instead of an ambient file path', async () => {
    requestJson.mockResolvedValue({
      to: 'markdown',
      result: { success: true, data: '# Report\n\nPreview body' },
    });

    await expect(
      loadLightweightOfficePreview('word', {
        artifactId: ' artifact-1 ',
        versionId: ' version-1 ',
        filePath: '/tmp/untrusted.docx',
        workspace: '/tmp',
      })
    ).resolves.toBe('# Report\n\nPreview body');

    const [path, init, options] = requestJson.mock.calls[0];
    expect(path).toBe('/api/document/convert');
    expect(JSON.parse(String(init.body))).toEqual({
      artifact_id: 'artifact-1',
      version_id: 'version-1',
      to: 'markdown',
    });
    expect(options.timeoutMs).toBe(30_000);
  });

  it('validates and normalizes workbook and presentation responses', async () => {
    requestJson
      .mockResolvedValueOnce({
        to: 'excel-json',
        result: {
          success: true,
          data: {
            sheets: [
              {
                name: '',
                data: [
                  ['Compound', 'IC50'],
                  ['A', 12],
                ],
              },
            ],
          },
        },
      })
      .mockResolvedValueOnce({
        to: 'ppt-json',
        result: {
          success: true,
          data: { slides: [{ slideNumber: 0, content: { text: 'Title\nEvidence' } }] },
        },
      });

    await expect(loadLightweightOfficePreview('excel', { artifactId: 'workbook-1' })).resolves.toEqual({
      sheets: [
        {
          name: 'Sheet 1',
          data: [
            ['Compound', 'IC50'],
            ['A', 12],
          ],
        },
      ],
    });
    await expect(loadLightweightOfficePreview('ppt', { artifactId: 'deck-1' })).resolves.toEqual({
      slides: [{ slideNumber: 1, content: { text: 'Title\nEvidence', paragraphs: ['Title', 'Evidence'] } }],
    });
  });

  it('rejects invalid sources and malformed converter output', async () => {
    await expect(loadLightweightOfficePreview('word', { versionId: 'orphan-version' })).rejects.toThrow(
      'requires a document source'
    );
    expect(requestJson).not.toHaveBeenCalled();

    requestJson.mockResolvedValue({
      to: 'excel-json',
      result: { success: true, data: { sheets: [{ name: 'Bad', data: 'not rows' }] } },
    });
    await expect(loadLightweightOfficePreview('excel', { artifactId: 'bad-workbook' })).rejects.toThrow(
      'Invalid workbook preview response'
    );
  });
});
