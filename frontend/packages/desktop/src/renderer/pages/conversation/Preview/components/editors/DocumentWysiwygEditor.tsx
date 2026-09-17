import { convertLatexDelimiters } from '@/renderer/utils/chat/latexDelimiters';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import ReactMarkdown, { type Components as MarkdownComponents } from 'react-markdown';
import { useTranslation } from 'react-i18next';
import rehypeKatex from 'rehype-katex';
import rehypeRaw from 'rehype-raw';
import remarkBreaks from 'remark-breaks';
import remarkGfm from 'remark-gfm';
import remarkMath from 'remark-math';
import { MarkdownImage } from '../viewers/MarkdownViewer';
import { resolveMarkdownArtifactVersionReferences } from '../viewers/markdownArtifactReferences';
import {
  markdownFromEditableHtml,
  replaceHtmlDocumentBody,
  restoreMarkdownResourceSource,
} from './documentWysiwygModel';
import {
  buildDocumentImageHtml,
  buildDocumentLinkHtml,
  ORIGINAL_RESOURCE_ATTRIBUTE,
  validateDocumentResourceSource,
} from './documentResourceModel';
import { materializeDocumentLocalResource } from './documentLocalResource';
import DocumentEditorToolbar, { type DocumentFormattingState } from './DocumentEditorToolbar';
import DocumentResourceDialog, { type DocumentInsertDialog } from './DocumentResourceDialog';
import {
  directoryOfDocument,
  handleDocumentEditorClick,
  pasteSanitizedDocumentContent,
  prepareHtmlDocumentForEditing,
  prepareInsertedImageSource,
  preventDocumentEditorNavigation,
  selectDocumentImage,
} from './documentEditorDom';
import './documentWysiwygEditor.css';

type DocumentFormat = 'markdown' | 'html';

type DocumentWysiwygEditorProps = {
  format: DocumentFormat;
  value: string;
  fileName: string;
  filePath?: string;
  workspace?: string;
  conversationId?: string;
  onChange: (value: string) => void;
};

const DocumentWysiwygEditor: React.FC<DocumentWysiwygEditorProps> = ({
  format,
  value,
  fileName,
  filePath,
  workspace,
  conversationId,
  onChange,
}) => {
  const { t } = useTranslation();
  const initialValueRef = useRef(value);
  const markdownSurfaceRef = useRef<HTMLDivElement>(null);
  const htmlFrameRef = useRef<HTMLIFrameElement>(null);
  const savedSelectionRef = useRef<Range | null>(null);
  const localFileInputRef = useRef<HTMLInputElement>(null);
  const pendingLocalKindRef = useRef<'image' | 'file'>('image');
  const pendingLocalLabelRef = useRef('');
  const pendingReplaceImageRef = useRef<HTMLImageElement | null>(null);
  const [htmlSource, setHtmlSource] = useState(value);
  const [selectedImage, setSelectedImage] = useState<HTMLImageElement | null>(null);
  const [insertDialog, setInsertDialog] = useState<DocumentInsertDialog>(null);
  const [resourceError, setResourceError] = useState<string | null>(null);
  const [isMaterializingLocal, setIsMaterializingLocal] = useState(false);
  const [formattingState, setFormattingState] = useState<DocumentFormattingState>({
    bold: false,
    italic: false,
    unorderedList: false,
    orderedList: false,
    quote: false,
    heading1: false,
    heading2: false,
  });
  const baseDir = useMemo(() => directoryOfDocument(filePath), [filePath]);
  const markdownSource = useMemo(
    () => resolveMarkdownArtifactVersionReferences(convertLatexDelimiters(initialValueRef.current)),
    []
  );
  const markdownComponents = useMemo<MarkdownComponents>(
    () => ({
      script: () => null,
      style: () => null,
      iframe: () => null,
      object: () => null,
      embed: () => null,
      link: () => null,
      meta: () => null,
      base: () => null,
      form({ children }) {
        return <div>{children}</div>;
      },
      img({ src, alt, ...props }) {
        const source = typeof src === 'string' ? restoreMarkdownResourceSource(src) : '';
        return (
          <MarkdownImage
            src={src}
            alt={alt}
            baseDir={baseDir}
            workspace={workspace}
            {...{ [ORIGINAL_RESOURCE_ATTRIBUTE]: source }}
            {...props}
          />
        );
      },
    }),
    [baseDir, workspace]
  );
  useEffect(() => {
    if (format !== 'html') return;
    let active = true;
    void prepareHtmlDocumentForEditing(initialValueRef.current, filePath, workspace).then((prepared) => {
      if (active) setHtmlSource(prepared);
    });
    return () => {
      active = false;
    };
  }, [filePath, format, workspace]);

  const emitChange = useCallback(() => {
    if (format === 'markdown') {
      const surface = markdownSurfaceRef.current;
      if (surface) onChange(markdownFromEditableHtml(surface.innerHTML));
      return;
    }
    const body = htmlFrameRef.current?.contentDocument?.body;
    if (body) onChange(replaceHtmlDocumentBody(initialValueRef.current, body.outerHTML));
  }, [format, onChange]);

  const bindHtmlEditor = useCallback(() => {
    const frameDocument = htmlFrameRef.current?.contentDocument;
    if (!frameDocument?.body) return undefined;
    frameDocument.designMode = 'on';
    frameDocument.body.contentEditable = 'true';
    frameDocument.body.setAttribute('aria-label', t('preview.documentEditor.namedEditor', { name: fileName }));
    if (!frameDocument.head.querySelector('[data-synon-document-editor-style]')) {
      const style = frameDocument.createElement('style');
      style.setAttribute('data-synon-document-editor-style', 'true');
      style.textContent =
        '[data-document-selected="true"]{outline:2px solid #168cff!important;outline-offset:3px!important}';
      frameDocument.head.appendChild(style);
    }
    const handleInput = () => emitChange();
    const handleClick = (event: MouseEvent) => handleDocumentEditorClick(event, setSelectedImage);
    const handlePaste = (event: ClipboardEvent) => pasteSanitizedDocumentContent(event, frameDocument, emitChange);
    frameDocument.addEventListener('input', handleInput);
    frameDocument.addEventListener('click', handleClick);
    frameDocument.addEventListener('paste', handlePaste);
    return () => {
      frameDocument.removeEventListener('input', handleInput);
      frameDocument.removeEventListener('click', handleClick);
      frameDocument.removeEventListener('paste', handlePaste);
    };
  }, [emitChange, fileName, t]);

  useEffect(() => {
    if (format !== 'html') return;
    const iframe = htmlFrameRef.current;
    if (!iframe) return;
    let cleanup: (() => void) | undefined;
    const handleLoad = () => {
      cleanup?.();
      cleanup = bindHtmlEditor();
    };
    iframe.addEventListener('load', handleLoad);
    if (iframe.contentDocument?.readyState === 'complete') handleLoad();
    return () => {
      iframe.removeEventListener('load', handleLoad);
      cleanup?.();
    };
  }, [bindHtmlEditor, format, htmlSource]);

  const getEditorDocument = useCallback(
    () =>
      format === 'html'
        ? (htmlFrameRef.current?.contentDocument ?? null)
        : (markdownSurfaceRef.current?.ownerDocument ?? null),
    [format]
  );

  const refreshFormattingState = useCallback(() => {
    const editorDocument = getEditorDocument();
    if (!editorDocument?.queryCommandState) return;
    const block = String(editorDocument.queryCommandValue?.('formatBlock') ?? '').toLowerCase();
    setFormattingState({
      bold: editorDocument.queryCommandState('bold'),
      italic: editorDocument.queryCommandState('italic'),
      unorderedList: editorDocument.queryCommandState('insertUnorderedList'),
      orderedList: editorDocument.queryCommandState('insertOrderedList'),
      quote: block === 'blockquote',
      heading1: block === 'h1',
      heading2: block === 'h2',
    });
  }, [getEditorDocument]);

  const focusEditor = () => {
    if (format === 'html') htmlFrameRef.current?.contentDocument?.body.focus();
    else markdownSurfaceRef.current?.focus();
  };

  const captureSelection = () => {
    const editorDocument = getEditorDocument();
    const selection = editorDocument?.getSelection();
    savedSelectionRef.current = selection?.rangeCount ? selection.getRangeAt(0).cloneRange() : null;
    return selection?.toString().trim() ?? '';
  };

  const restoreSelection = () => {
    const editorDocument = getEditorDocument();
    const selection = editorDocument?.getSelection();
    const range = savedSelectionRef.current;
    if (!selection || !range) return;
    selection.removeAllRanges();
    selection.addRange(range);
  };

  const execute = (command: string, argument?: string) => {
    const editorDocument = getEditorDocument();
    if (!editorDocument) return;
    focusEditor();
    restoreSelection();
    editorDocument.execCommand(command, false, argument);
    savedSelectionRef.current = null;
    emitChange();
    refreshFormattingState();
  };

  const applyInsert = async () => {
    if (!insertDialog) return;
    const validation = validateDocumentResourceSource(insertDialog.source, insertDialog.kind);
    if ('reason' in validation) {
      setResourceError(t(`preview.documentEditor.resourceError.${validation.reason}`));
      return;
    }
    setResourceError(null);
    if (insertDialog.kind === 'link') {
      execute('insertHTML', buildDocumentLinkHtml(validation.source, insertDialog.label));
    } else if (selectedImage) {
      const displaySource = await prepareInsertedImageSource(validation.source, filePath, workspace);
      selectedImage.src = displaySource;
      selectedImage.setAttribute(ORIGINAL_RESOURCE_ATTRIBUTE, validation.source);
      selectedImage.alt = insertDialog.label.trim();
      emitChange();
    } else {
      const displaySource = await prepareInsertedImageSource(validation.source, filePath, workspace);
      execute('insertHTML', buildDocumentImageHtml(validation.source, insertDialog.label, displaySource));
    }
    setInsertDialog(null);
  };

  const openLocalResourcePicker = (kind: 'image' | 'file', replaceImage: HTMLImageElement | null = null) => {
    pendingLocalKindRef.current = kind;
    pendingLocalLabelRef.current = captureSelection();
    pendingReplaceImageRef.current = replaceImage;
    const input = localFileInputRef.current;
    if (!input) return;
    input.accept = kind === 'image' ? 'image/*' : '';
    input.value = '';
    input.click();
  };

  const handleLocalFileSelected = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (!file) return;
    setIsMaterializingLocal(true);
    setResourceError(null);
    try {
      const kind = pendingLocalKindRef.current;
      const resource = await materializeDocumentLocalResource({
        file,
        kind,
        filePath,
        workspace,
        conversationId,
      });
      const replaceImage = pendingReplaceImageRef.current;
      if (kind === 'image' && replaceImage) {
        replaceImage.src = resource.displaySource;
        replaceImage.setAttribute(ORIGINAL_RESOURCE_ATTRIBUTE, resource.source);
        replaceImage.alt = resource.label;
        emitChange();
      } else if (kind === 'image') {
        execute('insertHTML', buildDocumentImageHtml(resource.source, resource.label, resource.displaySource));
      } else {
        execute(
          'insertHTML',
          buildDocumentLinkHtml(resource.source, pendingLocalLabelRef.current || resource.label, resource.displaySource)
        );
      }
    } catch {
      setResourceError(t('preview.documentEditor.localResourceFailed'));
    } finally {
      pendingReplaceImageRef.current = null;
      setIsMaterializingLocal(false);
    }
  };

  useEffect(() => {
    const editorDocument = getEditorDocument();
    if (!editorDocument) return;
    const refresh = () => refreshFormattingState();
    editorDocument.addEventListener('selectionchange', refresh);
    editorDocument.addEventListener('keyup', refresh);
    editorDocument.addEventListener('mouseup', refresh);
    return () => {
      editorDocument.removeEventListener('selectionchange', refresh);
      editorDocument.removeEventListener('keyup', refresh);
      editorDocument.removeEventListener('mouseup', refresh);
    };
  }, [getEditorDocument, htmlSource, refreshFormattingState]);

  const resizeSelectedImage = (width: string) => {
    if (!selectedImage) return;
    selectedImage.style.width = width;
    selectedImage.style.height = 'auto';
    emitChange();
  };

  const removeSelectedImage = () => {
    if (!selectedImage) return;
    selectedImage.remove();
    setSelectedImage(null);
    emitChange();
  };

  const handleMarkdownPointerDown = (event: React.PointerEvent<HTMLDivElement>) => {
    selectDocumentImage(event.target, setSelectedImage);
  };

  const handleMarkdownClick = (event: React.MouseEvent<HTMLDivElement>) => {
    preventDocumentEditorNavigation(event);
  };

  return (
    <div className='document-wysiwyg-editor' data-format={format}>
      <DocumentEditorToolbar
        formattingState={formattingState}
        selectedImage={selectedImage}
        materializingLocal={isMaterializingLocal}
        onExecute={execute}
        onOpenLink={() => {
          setResourceError(null);
          setInsertDialog({ kind: 'link', source: '', label: captureSelection() });
        }}
        onOpenLocalImage={() => openLocalResourcePicker('image')}
        onOpenLocalFile={() => openLocalResourcePicker('file')}
        onOpenImageUrl={() => {
          captureSelection();
          setResourceError(null);
          setInsertDialog({ kind: 'image', source: '', label: '' });
        }}
        onResizeImage={resizeSelectedImage}
        onReplaceLocalImage={() => selectedImage && openLocalResourcePicker('image', selectedImage)}
        onReplaceImageUrl={() => {
          if (!selectedImage) return;
          setResourceError(null);
          setInsertDialog({
            kind: 'image',
            source:
              selectedImage.getAttribute(ORIGINAL_RESOURCE_ATTRIBUTE) ||
              restoreMarkdownResourceSource(selectedImage.getAttribute('src') || ''),
            label: selectedImage.alt,
          });
        }}
        onRemoveImage={removeSelectedImage}
      />

      <input ref={localFileInputRef} type='file' hidden onChange={(event) => void handleLocalFileSelected(event)} />

      {resourceError && !insertDialog ? (
        <div className='document-wysiwyg-editor__resource-error' role='alert'>
          {resourceError}
        </div>
      ) : null}

      <div className='document-wysiwyg-editor__canvas'>
        {format === 'markdown' ? (
          <div
            ref={markdownSurfaceRef}
            className='document-wysiwyg-editor__surface synon-ai-markdown preview-markdown__document'
            contentEditable
            suppressContentEditableWarning
            role='textbox'
            aria-multiline='true'
            aria-label={t('preview.documentEditor.namedEditor', { name: fileName })}
            onInput={emitChange}
            onPointerDownCapture={handleMarkdownPointerDown}
            onClickCapture={handleMarkdownClick}
            onPaste={(event) => pasteSanitizedDocumentContent(event.nativeEvent, document, emitChange)}
          >
            <ReactMarkdown
              remarkPlugins={[remarkGfm, remarkBreaks, remarkMath]}
              rehypePlugins={[rehypeRaw, rehypeKatex]}
              components={markdownComponents}
            >
              {markdownSource}
            </ReactMarkdown>
          </div>
        ) : (
          <iframe
            ref={htmlFrameRef}
            className='document-wysiwyg-editor__frame'
            title={t('preview.documentEditor.namedEditor', { name: fileName })}
            sandbox='allow-same-origin'
            srcDoc={htmlSource}
          />
        )}
      </div>

      <DocumentResourceDialog
        value={insertDialog}
        replacingImage={Boolean(selectedImage)}
        onChange={setInsertDialog}
        onConfirm={() => void applyInsert()}
        onCancel={() => setInsertDialog(null)}
      />
    </div>
  );
};

export default DocumentWysiwygEditor;
