import { Button } from '@arco-design/web-react';
import {
  Delete,
  InsertTable,
  LinkCloud,
  LinkOne,
  Minus,
  OrderedList,
  Paperclip,
  PictureOne,
  Quote,
  Redo,
  TextBold,
  TextItalic,
  TitleLevel,
  Undo,
  UnorderedList,
} from '@icon-park/react';
import React from 'react';
import { useTranslation } from 'react-i18next';
import { buildDocumentTableHtml } from './documentResourceModel';

export type DocumentFormattingState = {
  bold: boolean;
  italic: boolean;
  unorderedList: boolean;
  orderedList: boolean;
  quote: boolean;
  heading1: boolean;
  heading2: boolean;
};

type DocumentEditorToolbarProps = {
  formattingState: DocumentFormattingState;
  selectedImage: HTMLImageElement | null;
  materializingLocal: boolean;
  onExecute: (command: string, argument?: string) => void;
  onOpenLink: () => void;
  onOpenLocalImage: () => void;
  onOpenLocalFile: () => void;
  onOpenImageUrl: () => void;
  onResizeImage: (width: string) => void;
  onReplaceLocalImage: () => void;
  onReplaceImageUrl: () => void;
  onRemoveImage: () => void;
};

const DocumentEditorToolbar: React.FC<DocumentEditorToolbarProps> = ({
  formattingState,
  selectedImage,
  materializingLocal,
  onExecute,
  onOpenLink,
  onOpenLocalImage,
  onOpenLocalFile,
  onOpenImageUrl,
  onResizeImage,
  onReplaceLocalImage,
  onReplaceImageUrl,
  onRemoveImage,
}) => {
  const { t } = useTranslation();
  return (
    <>
      <div
        className='document-wysiwyg-editor__toolbar'
        role='toolbar'
        aria-label={t('preview.documentEditor.formattingToolbar')}
      >
        <FormatButton label={t('preview.documentEditor.undo')} onAction={() => onExecute('undo')}>
          <Undo size={16} />
        </FormatButton>
        <FormatButton label={t('preview.documentEditor.redo')} onAction={() => onExecute('redo')}>
          <Redo size={16} />
        </FormatButton>
        <span className='document-wysiwyg-editor__separator' />
        <FormatButton
          label={t('preview.documentEditor.bold')}
          active={formattingState.bold}
          onAction={() => onExecute('bold')}
        >
          <TextBold size={16} />
        </FormatButton>
        <FormatButton
          label={t('preview.documentEditor.italic')}
          active={formattingState.italic}
          onAction={() => onExecute('italic')}
        >
          <TextItalic size={16} />
        </FormatButton>
        <FormatButton
          label={t('preview.documentEditor.heading1')}
          active={formattingState.heading1}
          onAction={() => onExecute('formatBlock', 'h1')}
        >
          <TitleLevel size={16} />
          <span>1</span>
        </FormatButton>
        <FormatButton
          label={t('preview.documentEditor.heading2')}
          active={formattingState.heading2}
          onAction={() => onExecute('formatBlock', 'h2')}
        >
          <TitleLevel size={16} />
          <span>2</span>
        </FormatButton>
        <FormatButton
          label={t('preview.documentEditor.bulletList')}
          active={formattingState.unorderedList}
          onAction={() => onExecute('insertUnorderedList')}
        >
          <UnorderedList size={16} />
        </FormatButton>
        <FormatButton
          label={t('preview.documentEditor.numberedList')}
          active={formattingState.orderedList}
          onAction={() => onExecute('insertOrderedList')}
        >
          <OrderedList size={16} />
        </FormatButton>
        <FormatButton
          label={t('preview.documentEditor.quote')}
          active={formattingState.quote}
          onAction={() => onExecute('formatBlock', 'blockquote')}
        >
          <Quote size={16} />
        </FormatButton>
        <span className='document-wysiwyg-editor__separator' />
        <FormatButton label={t('preview.documentEditor.insertLink')} onAction={onOpenLink}>
          <LinkOne size={16} />
        </FormatButton>
        <FormatButton
          label={t('preview.documentEditor.insertLocalImage')}
          disabled={materializingLocal}
          onAction={onOpenLocalImage}
        >
          <PictureOne size={16} />
        </FormatButton>
        <FormatButton
          label={t('preview.documentEditor.insertLocalFile')}
          disabled={materializingLocal}
          onAction={onOpenLocalFile}
        >
          <Paperclip size={16} />
        </FormatButton>
        <FormatButton label={t('preview.documentEditor.insertImageFromUrl')} onAction={onOpenImageUrl}>
          <LinkCloud size={16} />
        </FormatButton>
        <FormatButton
          label={t('preview.documentEditor.insertTable')}
          onAction={() =>
            onExecute(
              'insertHTML',
              buildDocumentTableHtml([1, 2, 3].map((index) => t('preview.documentEditor.tableColumn', { index })))
            )
          }
        >
          <InsertTable size={16} />
        </FormatButton>
        <FormatButton
          label={t('preview.documentEditor.horizontalRule')}
          onAction={() => onExecute('insertHTML', '<hr>')}
        >
          <Minus size={16} />
        </FormatButton>
      </div>

      {selectedImage ? (
        <div
          className='document-wysiwyg-editor__image-toolbar'
          role='toolbar'
          aria-label={t('preview.documentEditor.imageToolbar')}
        >
          <span>{t('preview.documentEditor.imageSelected')}</span>
          {['25%', '50%', '75%', '100%'].map((width) => (
            <Button key={width} size='mini' onClick={() => onResizeImage(width)}>
              {width}
            </Button>
          ))}
          <Button size='mini' disabled={materializingLocal} onClick={onReplaceLocalImage}>
            {t('preview.documentEditor.replaceImage')}
          </Button>
          <Button size='mini' onClick={onReplaceImageUrl}>
            {t('preview.documentEditor.replaceImageFromUrl')}
          </Button>
          <Button size='mini' status='danger' icon={<Delete size={14} />} onClick={onRemoveImage}>
            {t('common.delete')}
          </Button>
        </div>
      ) : null}
    </>
  );
};

const FormatButton: React.FC<{
  label: string;
  onAction: () => void;
  children: React.ReactNode;
  active?: boolean;
  disabled?: boolean;
}> = ({ label, onAction, children, active, disabled }) => (
  <button
    type='button'
    className='document-wysiwyg-editor__format-button'
    aria-label={label}
    aria-pressed={active}
    title={label}
    disabled={disabled}
    onMouseDown={(event) => event.preventDefault()}
    onClick={onAction}
  >
    {children}
  </button>
);

export default DocumentEditorToolbar;
