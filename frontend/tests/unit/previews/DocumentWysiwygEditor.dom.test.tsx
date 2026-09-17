import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import DocumentWysiwygEditor from '@/renderer/pages/conversation/Preview/components/editors/DocumentWysiwygEditor';
import {
  markdownFromEditableHtml,
  replaceHtmlDocumentBody,
  sanitizeDocumentPasteHtml,
} from '@/renderer/pages/conversation/Preview/components/editors/documentWysiwygModel';
import {
  buildDocumentLinkHtml,
  buildDocumentTableHtml,
  validateDocumentResourceSource,
} from '@/renderer/pages/conversation/Preview/components/editors/documentResourceModel';
import { selectDocumentImage } from '@/renderer/pages/conversation/Preview/components/editors/documentEditorDom';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@/common', () => ({
  ipcBridge: {
    fs: {
      fetchRemoteImage: { invoke: vi.fn(async ({ url }: { url: string }) => url) },
      getImageBase64: { invoke: vi.fn(async () => 'data:image/png;base64,Y2hhcnQ=') },
      readFile: { invoke: vi.fn(async () => '') },
      copyFilesToWorkspace: {
        invoke: vi.fn(async () => ({ copied_files: ['/project/figure.png'] })),
      },
    },
    theme: {
      setActive: { invoke: vi.fn(async () => undefined) },
      changed: { on: vi.fn(() => () => undefined) },
    },
  },
}));

vi.mock('@/common/config/configService', () => ({
  configService: {
    whenReady: vi.fn(async () => undefined),
    get: vi.fn(() => undefined),
    set: vi.fn(async () => undefined),
    setLocal: vi.fn(),
    setBatch: vi.fn(async () => undefined),
    remove: vi.fn(async () => undefined),
    subscribe: vi.fn(() => () => undefined),
  },
}));

afterEach(() => vi.restoreAllMocks());

describe('DocumentWysiwygEditor', () => {
  it('edits Markdown as a formatted document and emits Markdown instead of editor HTML', async () => {
    const onChange = vi.fn();
    await renderWithI18n(
      <DocumentWysiwygEditor
        format='markdown'
        value={'# Clinical report\n\n**Stable** result.\n\n![Response chart](chart.png)'}
        fileName='report.md'
        filePath='/project/report.md'
        workspace='/project'
        onChange={onChange}
      />,
      'en-US'
    );

    const editor = screen.getByRole('textbox', { name: 'report.md document editor' });
    expect(editor).toHaveAttribute('contenteditable', 'true');
    expect(editor.querySelector('h1')).toHaveTextContent('Clinical report');
    expect(editor.querySelector('strong')).toHaveTextContent('Stable');
    await waitFor(() => expect(editor.querySelector('img')).toHaveAttribute('alt', 'Response chart'));
    expect(screen.queryByText('# Clinical report')).not.toBeInTheDocument();

    editor.innerHTML = '<h1>Edited title</h1><p><strong>Body</strong></p><img src="chart.png" alt="Chart">';
    fireEvent.input(editor);

    expect(onChange).toHaveBeenLastCalledWith(expect.stringContaining('# Edited title'));
    expect(onChange).toHaveBeenLastCalledWith(expect.stringContaining('**Body**'));
    expect(onChange).toHaveBeenLastCalledWith(expect.stringContaining('![Chart](chart.png)'));
  });

  it('exposes document formatting and image controls without a source editor', async () => {
    await renderWithI18n(
      <DocumentWysiwygEditor format='markdown' value='Plain text' fileName='notes.md' onChange={() => undefined} />,
      'en-US'
    );

    expect(screen.getByRole('toolbar', { name: 'Document formatting' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Bold' })).toHaveAttribute('aria-pressed', 'false');
    expect(screen.getByRole('button', { name: 'Heading 1' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Insert local image' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Insert local file' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Insert table' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Insert divider' })).toBeInTheDocument();
    expect(screen.queryByText('Source')).not.toBeInTheDocument();
  });

  it('opens a local file picker first for images and keeps URL insertion as a separate action', async () => {
    const click = vi.spyOn(HTMLInputElement.prototype, 'click').mockImplementation(() => undefined);
    const { container } = await renderWithI18n(
      <DocumentWysiwygEditor
        format='markdown'
        value='Report'
        fileName='report.md'
        filePath='/project/report.md'
        workspace='/project'
        onChange={() => undefined}
      />,
      'en-US'
    );

    fireEvent.click(screen.getByRole('button', { name: 'Insert local image' }));
    expect(click).toHaveBeenCalledTimes(1);
    expect(container.querySelector<HTMLInputElement>('input[type="file"]')?.accept).toBe('image/*');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Insert image from link' }));
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('requires display text for link composition and disables executable protocols', async () => {
    await renderWithI18n(
      <DocumentWysiwygEditor format='markdown' value='Plain text' fileName='notes.md' onChange={() => undefined} />,
      'en-US'
    );

    fireEvent.click(screen.getByRole('button', { name: 'Insert link' }));
    expect(screen.getByRole('textbox', { name: 'Display text' })).toBeInTheDocument();
    fireEvent.change(screen.getByRole('textbox', { name: 'Link or image URL' }), {
      target: { value: 'javascript:alert(1)' },
    });
    expect(screen.getByRole('button', { name: 'Confirm' })).toBeDisabled();
    expect(screen.getByRole('alert')).toHaveTextContent('This address type is not allowed');
  });

  it('keeps document links inert while editing so clicks cannot navigate away from unsaved work', async () => {
    await renderWithI18n(
      <DocumentWysiwygEditor
        format='markdown'
        value='[Evidence](https://example.org/evidence)'
        fileName='report.md'
        onChange={() => undefined}
      />,
      'en-US'
    );

    const link = screen.getByRole('link', { name: 'Evidence' });
    expect(fireEvent.click(link)).toBe(false);
  });

  it('selects images created inside the isolated HTML editor document', () => {
    const frame = document.createElement('iframe');
    document.body.appendChild(frame);
    const frameDocument = frame.contentDocument;
    if (!frameDocument) throw new Error('iframe document unavailable');
    const image = frameDocument.createElement('img');
    frameDocument.body.appendChild(image);
    const select = vi.fn();

    selectDocumentImage(image, select);

    expect(select).toHaveBeenCalledWith(image);
    expect(image).toHaveAttribute('data-document-selected', 'true');
    frame.remove();
  });

  it('keeps a rendered Markdown image selected across toolbar rerenders and removes that live image', async () => {
    const onChange = vi.fn();
    await renderWithI18n(
      <DocumentWysiwygEditor
        format='markdown'
        value='![Binding mode](figure.png)'
        fileName='report.md'
        filePath='/workspace/report.md'
        workspace='/workspace'
        onChange={onChange}
      />,
      'en-US'
    );

    const editor = screen.getByRole('textbox', { name: 'report.md document editor' });
    const image = await screen.findByRole('img', { name: 'Binding mode' });
    fireEvent.pointerDown(image);

    expect(screen.getByRole('toolbar', { name: 'Image editing' })).toBeInTheDocument();
    expect(editor.querySelector('img')).toBe(image);
    expect(image).toHaveAttribute('data-document-selected', 'true');

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));

    expect(editor.querySelector('img')).toBeNull();
    expect(onChange).toHaveBeenLastCalledWith(expect.not.stringContaining('figure.png'));
  });

  it('selects a real image shape when the isolated browser hides the Element constructor', () => {
    const attributes = new Map<string, string>();
    const image = {
      nodeType: 1,
      tagName: 'IMG',
      closest: vi.fn(() => null),
      setAttribute: vi.fn((name: string, value: string) => attributes.set(name, value)),
      removeAttribute: vi.fn((name: string) => attributes.delete(name)),
      ownerDocument: {
        defaultView: {},
        querySelectorAll: vi.fn(() => []),
      },
    } as unknown as EventTarget;
    const select = vi.fn();

    selectDocumentImage(image, select);

    expect(select).toHaveBeenCalledWith(image);
    expect(attributes.get('data-document-selected')).toBe('true');
  });

  it('does not mount active raw HTML elements from an editable Markdown document', async () => {
    const { container } = await renderWithI18n(
      <DocumentWysiwygEditor
        format='markdown'
        value={
          '# Safe report\n\n<style>body{display:none}</style><script>steal()</script><iframe src="https://example.org"></iframe><form action="https://example.org"><strong>Kept content</strong></form>'
        }
        fileName='report.md'
        onChange={() => undefined}
      />,
      'en-US'
    );

    const editor = screen.getByRole('textbox', { name: 'report.md document editor' });
    expect(editor).toHaveTextContent('Safe report');
    expect(editor).toHaveTextContent('Kept content');
    expect(editor.querySelector('style')).toBeNull();
    expect(editor.querySelector('script')).toBeNull();
    expect(editor.querySelector('iframe')).toBeNull();
    expect(editor.querySelector('form')).toBeNull();
    expect(container.querySelector('iframe')).toBeNull();
  });

  it('opens HTML as a live document canvas while keeping its authored styles', async () => {
    await renderWithI18n(
      <DocumentWysiwygEditor
        format='html'
        value='<html><head><style>h1{color:navy}</style></head><body><h1>Styled report</h1><img src="chart.png" alt="Chart"></body></html>'
        fileName='report.html'
        filePath='/project/report.html'
        workspace='/project'
        onChange={() => undefined}
      />,
      'en-US'
    );

    const frame = screen.getByTitle('report.html document editor');
    expect(frame).toHaveAttribute('sandbox', 'allow-same-origin');
    await waitFor(() => expect(frame.getAttribute('srcdoc')).toContain('<style>h1{color:navy}</style>'));
    await waitFor(() => expect(frame.getAttribute('srcdoc')).toContain('data-synon-original-src="chart.png"'));
    expect(frame.getAttribute('srcdoc')).toContain('data:image/png;base64,Y2hhcnQ=');
  });

  it('resolves durable artifact images and links in HTML while preserving their authored references', async () => {
    const versionId = 'de03acd4-6097-4fe9-a3af-f8fa47341850';
    await renderWithI18n(
      <DocumentWysiwygEditor
        format='html'
        value={`<html><body><a href="{{artifact:${versionId}}}">Evidence</a><img src="{{artifact:${versionId}}}" alt="Chart"></body></html>`}
        fileName='report.html'
        workspace='synonbiomed://conversation'
        conversationId='conversation'
        onChange={() => undefined}
      />,
      'en-US'
    );

    const frame = screen.getByTitle('report.html document editor');
    await waitFor(() => expect(frame.getAttribute('srcdoc')).toContain(`/api/artifacts/versions/${versionId}`));
    expect(frame.getAttribute('srcdoc')).toContain(`data-synon-original-href="{{artifact:${versionId}}}"`);
    expect(frame.getAttribute('srcdoc')).toContain(`data-synon-original-src="{{artifact:${versionId}}}"`);
  });
});

describe('documentWysiwygModel', () => {
  it('preserves GFM structure, artifact image references and mathematical source', () => {
    const markdown = markdownFromEditableHtml(`
      <h2>Results</h2>
      <table><thead><tr><th>Drug</th><th>Score</th></tr></thead><tbody><tr><td>A</td><td>8</td></tr></tbody></table>
      <p><img src="/api/artifacts/versions/de03acd4-6097-4fe9-a3af-f8fa47341850" alt="Plot"></p>
      <span class="katex"><math><semantics><annotation encoding="application/x-tex">E=mc^2</annotation></semantics></math></span>
    `);

    expect(markdown).toContain('## Results');
    expect(markdown).toContain('| Drug | Score |');
    expect(markdown).toContain('![Plot]({{artifact:de03acd4-6097-4fe9-a3af-f8fa47341850}})');
    expect(markdown).toContain('$E=mc^2$');
  });

  it('rejects executable resource protocols while allowing durable local, artifact and HTTPS sources', () => {
    expect(validateDocumentResourceSource('javascript:alert(1)', 'link')).toEqual({
      valid: false,
      reason: 'unsafe-protocol',
    });
    expect(validateDocumentResourceSource('data:text/html;base64,PHNjcmlwdD4=', 'image')).toEqual({
      valid: false,
      reason: 'invalid-image-data',
    });
    expect(validateDocumentResourceSource('../figures/chart.png', 'image').valid).toBe(true);
    expect(validateDocumentResourceSource('{{artifact:de03acd4-6097-4fe9-a3af-f8fa47341850}}', 'image').valid).toBe(
      true
    );
    expect(validateDocumentResourceSource('https://example.org/report', 'link').valid).toBe(true);
  });

  it('builds links without requiring selected text and inserts semantic document tables', () => {
    expect(buildDocumentLinkHtml('https://example.org', 'Evidence')).toBe(
      '<a href="https://example.org" data-synon-original-href="https://example.org">Evidence</a>'
    );
    expect(buildDocumentLinkHtml('https://example.org', '')).toContain('>https://example.org</a>');
    expect(buildDocumentTableHtml(['Column 1', 'Column 2', 'Column 3'])).toContain(
      '<thead><tr><th>Column 1</th><th>Column 2</th><th>Column 3</th></tr></thead>'
    );
  });

  it('updates the HTML body while preserving document head, body attributes and image sources', () => {
    const original =
      '<!doctype html><html lang="zh-CN"><head><title>Report</title><style>.lead{color:red}</style></head><body class="paper"><h1>Old</h1></body></html>';
    const editedBody =
      '<body class="paper" contenteditable="true" aria-label="editor"><h1>New</h1><a data-synon-original-href="{{artifact:de03acd4-6097-4fe9-a3af-f8fa47341850}}" href="/api/artifacts/versions/de03acd4-6097-4fe9-a3af-f8fa47341850">Evidence</a><img data-synon-original-src="figures/chart.png" data-document-selected="true" src="data:image/png;base64,abc" alt="Chart"></body>';

    const result = replaceHtmlDocumentBody(original, editedBody);

    expect(result.toLowerCase()).toContain('<!doctype html>');
    expect(result).toContain('<title>Report</title>');
    expect(result).toContain('<style>.lead{color:red}</style>');
    expect(result).toContain('<body class="paper">');
    expect(result).toContain('<h1>New</h1>');
    expect(result).toContain('src="figures/chart.png"');
    expect(result).toContain('href="{{artifact:de03acd4-6097-4fe9-a3af-f8fa47341850}}"');
    expect(result).not.toContain('data-synon-original-src');
    expect(result).not.toContain('data-synon-original-href');
    expect(result).not.toContain('data-document-selected');
    expect(result).not.toContain('contenteditable');
    expect(result).not.toContain('aria-label="editor"');
  });

  it('sanitizes pasted document markup without removing ordinary formatting and images', () => {
    const result = sanitizeDocumentPasteHtml(
      '<h2>Result</h2><p><strong>Safe</strong></p><img src="chart.png" onerror="steal()"><script>steal()</script><iframe srcdoc="bad"></iframe>'
    );

    expect(result).toContain('<h2>Result</h2>');
    expect(result).toContain('<strong>Safe</strong>');
    expect(result).toContain('<img src="chart.png">');
    expect(result).not.toContain('onerror');
    expect(result).not.toContain('<script');
    expect(result).not.toContain('<iframe');
  });
});
