import { fireEvent, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import SynonBiomedTextArtifactViewer from '@/renderer/pages/artifact/SynonBiomedTextArtifactViewer';
import { resolveMarkdownArtifactVersionReferences } from '@/renderer/pages/conversation/Preview/components/viewers/markdownArtifactReferences';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@/renderer/pages/conversation/Preview/components/editors/CodeEditor', () => ({
  default: ({ value, fileName }: { value: string; fileName: string }) => (
    <pre aria-label={`${fileName} editor`}>{value}</pre>
  ),
}));

describe('SynonBiomedTextArtifactViewer', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('loads a text artifact and exposes an English accessible content name', async () => {
    const fetchMock = vi.fn(async () => new Response('status: ready', { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(
      <SynonBiomedTextArtifactViewer filename='report.txt' contentUrl='/api/artifacts/text-1' kind='code' />,
      'en-US'
    );

    expect(await screen.findByLabelText('report.txt content')).toBeInTheDocument();
    expect(screen.getByLabelText('report.txt editor')).toHaveTextContent('status: ready');
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/artifacts/text-1',
      expect.objectContaining({ credentials: 'same-origin' })
    );
  });

  it('shows a stable localized request error and can retry successfully', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response('unavailable', { status: 503 }))
      .mockResolvedValueOnce(new Response('recovered', { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(
      <SynonBiomedTextArtifactViewer filename='report.txt' contentUrl='/api/artifacts/text-1' kind='code' />,
      'en-US'
    );

    expect(await screen.findByText('Failed to load file preview')).toBeInTheDocument();
    expect(screen.getByText('Failed to load file content (HTTP 503).')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText('recovered')).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('resolves strict immutable artifact version references to same-origin preview URLs', () => {
    const imageVersionId = 'de03acd4-6097-4fe9-a3af-f8fa47341850';
    const validationVersionId = 'f923cba6-0629-4b15-9cea-6a2a28fd210e';
    const markdown = [
      `![TOPSIS]({{artifact:${imageVersionId}}})`,
      `[validation.json]({{artifact:${validationVersionId}}})`,
      '![invalid]({{artifact:../../secrets}})',
      '[legacy]({{artifact:art_not_a_version}})',
    ].join('\n');

    const resolved = resolveMarkdownArtifactVersionReferences(markdown);

    expect(resolved).toContain(`![TOPSIS](/api/artifacts/versions/${imageVersionId})`);
    expect(resolved).toContain(`[validation.json](/api/artifacts/versions/${validationVersionId})`);
    expect(resolved).toContain('![invalid]({{artifact:../../secrets}})');
    expect(resolved).toContain('[legacy]({{artifact:art_not_a_version}})');
  });
});
