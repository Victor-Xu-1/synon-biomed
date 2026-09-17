import {
  createSynonBiomedArtifactAnnotation,
  type SynonBiomedArtifactAnnotation,
} from '@/renderer/services/synonBiomedAnnotations';
import type { SynonBiomedArtifactCanvasSelection } from './artifactCanvasSelection';
import { Input, Modal } from '@arco-design/web-react';
import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';

export const ArtifactSelectionAnnotationModal: React.FC<{
  artifactId: string;
  versionId: string;
  selection: SynonBiomedArtifactCanvasSelection;
  onCancel: () => void;
  onCreated: (annotation: SynonBiomedArtifactAnnotation) => void | Promise<void>;
}> = ({ artifactId, versionId, selection, onCancel, onCreated }) => {
  const { t } = useTranslation();
  const [note, setNote] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const save = async () => {
    const text = note.trim();
    if (!text) return;
    setSaving(true);
    setError(null);
    try {
      const annotation = await createSynonBiomedArtifactAnnotation(artifactId, versionId, {
        type: selection.type,
        text,
        selectionText: selection.type === 'point' ? null : selection.text,
        selectionPrefix: selection.type === 'text_selection' ? selection.selectionPrefix : null,
        startLine: selection.type === 'text_selection' ? selection.startLine : null,
        startColumn: selection.type === 'text_selection' ? selection.startColumn : null,
        endLine: selection.type === 'text_selection' ? selection.endLine : null,
        endColumn: selection.type === 'text_selection' ? selection.endColumn : null,
        xPercent: selection.type === 'text_selection' ? null : selection.xPercent,
        yPercent: selection.type === 'text_selection' ? null : selection.yPercent,
        pageNumber: selection.type === 'html_element' ? null : selection.pageNumber,
        elementSelector: selection.type === 'html_element' ? selection.elementSelector : null,
        elementDescriptor: selection.type === 'html_element' ? selection.elementDescriptor : null,
      });
      await onCreated(annotation);
    } catch (reason) {
      console.error('[ArtifactSelectionAnnotationModal] Failed to add selection annotation', reason);
      setError(t('preview.selectionAnnotation.addFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      title={
        selection.type === 'point'
          ? t('preview.selectionAnnotation.pointTitle')
          : selection.type === 'html_element'
            ? t('preview.selectionAnnotation.elementTitle')
            : t('preview.selectionAnnotation.selectionTitle')
      }
      visible
      onCancel={onCancel}
      onOk={() => void save()}
      confirmLoading={saving}
      okButtonProps={{ disabled: !note.trim() }}
      okText={t('preview.artifactAnnotations.add')}
      cancelText={t('common.cancel')}
      unmountOnExit
      style={{ width: 560, maxWidth: 'calc(100vw - 32px)' }}
    >
      <div className='flex flex-col gap-12px'>
        <div
          className='max-h-120px overflow-auto border-l-3 border-solid bg-fill-1 px-10px py-8px whitespace-pre-wrap break-words text-12px leading-19px text-t-primary'
          style={{ borderLeftColor: 'rgb(var(--primary-6))' }}
        >
          {selection.text}
        </div>
        <label className='flex flex-col gap-6px text-12px text-t-secondary'>
          {t('preview.artifactAnnotations.fields.content')}
          <Input.TextArea
            aria-label={t('preview.selectionAnnotation.content')}
            value={note}
            onChange={setNote}
            autoFocus
            autoSize={{ minRows: 3, maxRows: 8 }}
            maxLength={4000}
            showWordLimit
          />
        </label>
        {error && <div className='text-11px leading-18px text-danger-6'>{error}</div>}
      </div>
    </Modal>
  );
};
