import {
  createSynonBiomedArtifactAnnotation,
  deleteSynonBiomedArtifactAnnotation,
  loadSynonBiomedArtifactAnnotations,
  loadSynonBiomedArtifactVerification,
  requestSynonBiomedFrameAudit,
  updateSynonBiomedArtifactAnnotation,
  type CreateSynonBiomedArtifactAnnotationInput,
  type SynonBiomedAppliedArtifactEdit,
  type SynonBiomedAnnotationType,
  type SynonBiomedArtifactAnnotation,
  type SynonBiomedVerificationCheck,
} from '@/renderer/services/synonBiomedAnnotations';
import { Button, Empty, Input, InputNumber, Message, Modal, Select, Spin } from '@arco-design/web-react';
import { CheckOne, Delete, Edit, Magic, Plus, Refresh } from '@icon-park/react';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ArtifactEditRefinementPanel } from './ArtifactEditRefinementPanel';

const ANNOTATION_TYPES: SynonBiomedAnnotationType[] = ['point', 'text_selection', 'html_element', 'screenshot'];

export const ArtifactAnnotationsPanel: React.FC<{
  artifactId: string;
  versionId: string;
  refreshToken?: number;
  onVersionApplied?: (result: SynonBiomedAppliedArtifactEdit) => void | Promise<void>;
  onAnnotationsChange?: (annotations: SynonBiomedArtifactAnnotation[]) => void;
}> = ({ artifactId, versionId, refreshToken = 0, onVersionApplied, onAnnotationsChange }) => {
  const { t } = useTranslation();
  const [annotations, setAnnotations] = useState<SynonBiomedArtifactAnnotation[]>([]);
  const [checksum, setChecksum] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const [editorVisible, setEditorVisible] = useState(false);
  const [editing, setEditing] = useState<SynonBiomedArtifactAnnotation | null>(null);
  const [draft, setDraft] = useState<AnnotationDraft>(emptyDraft());
  const [refining, setRefining] = useState<SynonBiomedArtifactAnnotation | null>(null);
  const [saving, setSaving] = useState(false);
  const [messageApi, messageContextHolder] = Message.useMessage();

  const reload = useCallback(async () => {
    setLoading(true);
    setFailed(false);
    try {
      const result = await loadSynonBiomedArtifactAnnotations(artifactId, versionId);
      setAnnotations(result.annotations);
      onAnnotationsChange?.(result.annotations);
      setChecksum(result.currentChecksum);
    } catch (error) {
      console.error('[ArtifactAnnotationsPanel] Failed to load annotations', error);
      setAnnotations([]);
      onAnnotationsChange?.([]);
      setChecksum(null);
      setFailed(true);
    } finally {
      setLoading(false);
    }
  }, [artifactId, onAnnotationsChange, refreshToken, versionId]);

  useEffect(() => {
    void reload();
  }, [reload]);

  const openCreate = () => {
    setEditing(null);
    setDraft(emptyDraft());
    setEditorVisible(true);
  };

  const openEdit = (annotation: SynonBiomedArtifactAnnotation) => {
    setEditing(annotation);
    setDraft(draftFromAnnotation(annotation));
    setEditorVisible(true);
  };

  const save = async () => {
    const text = draft.text.trim();
    if (!text) return;
    setSaving(true);
    try {
      if (editing) {
        const updated = await updateSynonBiomedArtifactAnnotation(editing.id, { text });
        const nextAnnotations = annotations.map((item) => (item.id === updated.id ? updated : item));
        setAnnotations(nextAnnotations);
        onAnnotationsChange?.(nextAnnotations);
        messageApi.success(t('preview.artifactAnnotations.updated'));
      } else {
        const created = await createSynonBiomedArtifactAnnotation(artifactId, versionId, toCreateInput(draft));
        const nextAnnotations = [...annotations, created];
        setAnnotations(nextAnnotations);
        onAnnotationsChange?.(nextAnnotations);
        setChecksum(created.contentChecksum ?? checksum);
        messageApi.success(t('preview.artifactAnnotations.added'));
      }
      setEditorVisible(false);
    } catch (error) {
      console.error('[ArtifactAnnotationsPanel] Failed to save annotation', error);
      messageApi.error(t('preview.artifactAnnotations.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  const remove = (annotation: SynonBiomedArtifactAnnotation) => {
    Modal.confirm({
      title: t('preview.artifactAnnotations.deleteTitle'),
      content: t('preview.artifactAnnotations.deleteConfirm', {
        label: annotation.label || t('preview.artifactAnnotations.thisAnnotation'),
      }),
      okButtonProps: { status: 'danger' },
      okText: t('common.delete'),
      cancelText: t('common.cancel'),
      onOk: async () => {
        try {
          await deleteSynonBiomedArtifactAnnotation(annotation.id);
          const nextAnnotations = annotations.filter((item) => item.id !== annotation.id);
          setAnnotations(nextAnnotations);
          onAnnotationsChange?.(nextAnnotations);
          messageApi.success(t('preview.artifactAnnotations.deleted'));
        } catch (error) {
          console.error('[ArtifactAnnotationsPanel] Failed to delete annotation', error);
          messageApi.error(t('preview.artifactAnnotations.deleteFailed'));
          throw error;
        }
      },
    });
  };

  return (
    <div className='pb-18px' data-testid='artifact-annotations-panel'>
      {messageContextHolder}
      <div className='px-16px pb-10px flex items-center justify-between gap-8px'>
        <div>
          <div className='text-12px font-[600] text-t-primary'>{t('preview.artifactAnnotations.title')}</div>
          <div className='mt-2px text-11px text-t-tertiary'>
            {t('preview.artifactAnnotations.currentVersionCount', { count: annotations.length })}
          </div>
        </div>
        <div className='flex gap-4px'>
          <Button
            type='text'
            size='small'
            aria-label={t('preview.artifactAnnotations.refresh')}
            icon={<Refresh theme='outline' size={14} />}
            onClick={() => void reload()}
          />
          <Button size='small' type='primary' icon={<Plus theme='outline' size={14} />} onClick={openCreate}>
            {t('preview.artifactAnnotations.add')}
          </Button>
        </div>
      </div>

      {loading ? (
        <div className='h-120px flex-center'>
          <Spin size={20} />
        </div>
      ) : failed ? (
        <Empty description={t('preview.artifactAnnotations.loadFailed')} />
      ) : annotations.length === 0 ? (
        <Empty description={t('preview.artifactAnnotations.empty')} />
      ) : (
        <div className='border-t border-solid border-[var(--color-border-2)]'>
          {annotations.map((annotation) => (
            <AnnotationRow
              key={annotation.id}
              annotation={annotation}
              onEdit={openEdit}
              onDelete={remove}
              onRefine={setRefining}
            />
          ))}
        </div>
      )}

      {checksum && <div className='px-16px pt-10px truncate font-mono text-10px text-t-tertiary'>{checksum}</div>}

      <Modal
        title={editing ? t('preview.artifactAnnotations.editTitle') : t('preview.artifactAnnotations.addTitle')}
        visible={editorVisible}
        onCancel={() => setEditorVisible(false)}
        onOk={() => void save()}
        confirmLoading={saving}
        okButtonProps={{ disabled: !draft.text.trim() }}
        okText={editing ? t('common.save') : t('preview.artifactAnnotations.add')}
        cancelText={t('common.cancel')}
        unmountOnExit
      >
        <AnnotationEditor draft={draft} editing={Boolean(editing)} onChange={setDraft} />
      </Modal>

      {refining?.selectionText && (
        <ArtifactEditRefinementPanel
          artifactId={artifactId}
          versionId={versionId}
          selectedText={refining.selectionText}
          initialInstruction={refining.text}
          onClose={() => setRefining(null)}
          onApplied={async (result) => {
            await onVersionApplied?.(result);
          }}
        />
      )}
    </div>
  );
};

export const ArtifactVerificationPanel: React.FC<{
  versionId: string;
  rootFrameId: string | null;
}> = ({ versionId, rootFrameId }) => {
  const { t } = useTranslation();
  const [checks, setChecks] = useState<SynonBiomedVerificationCheck[]>([]);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const [auditing, setAuditing] = useState(false);
  const [messageApi, messageContextHolder] = Message.useMessage();

  const reload = useCallback(async () => {
    setLoading(true);
    setFailed(false);
    try {
      setChecks(await loadSynonBiomedArtifactVerification(versionId));
    } catch (error) {
      console.error('[ArtifactVerificationPanel] Failed to load verification checks', error);
      setChecks([]);
      setFailed(true);
    } finally {
      setLoading(false);
    }
  }, [versionId]);

  useEffect(() => {
    void reload();
  }, [reload]);

  const summary = useMemo(() => {
    const result = { pass: 0, warn: 0, fail: 0, inconclusive: 0 };
    for (const check of checks) result[check.verdict] += 1;
    return result;
  }, [checks]);

  const requestAudit = async () => {
    if (!rootFrameId) return;
    setAuditing(true);
    try {
      await requestSynonBiomedFrameAudit(rootFrameId);
      messageApi.success(t('preview.artifactVerification.auditStarted'));
      window.setTimeout((): void => {
        void reload();
      }, 1200);
    } catch (error) {
      console.error('[ArtifactVerificationPanel] Failed to start verification audit', error);
      messageApi.error(t('preview.artifactVerification.auditStartFailed'));
    } finally {
      setAuditing(false);
    }
  };

  return (
    <div className='pb-18px' data-testid='artifact-verification-panel'>
      {messageContextHolder}
      <div className='px-16px pb-10px flex items-center justify-between gap-8px'>
        <div>
          <div className='text-12px font-[600] text-t-primary'>{t('preview.artifactVerification.title')}</div>
          <div className='mt-2px text-11px text-t-tertiary'>
            {t('preview.artifactVerification.checkCount', { count: checks.length })}
          </div>
        </div>
        <div className='flex gap-4px'>
          <Button
            type='text'
            size='small'
            aria-label={t('preview.artifactVerification.refresh')}
            icon={<Refresh theme='outline' size={14} />}
            onClick={() => void reload()}
          />
          <Button
            size='small'
            icon={<CheckOne theme='outline' size={14} />}
            loading={auditing}
            disabled={!rootFrameId}
            onClick={() => void requestAudit()}
          >
            {t('preview.artifactVerification.auditAgain')}
          </Button>
        </div>
      </div>

      <div className='mx-16px mb-12px grid grid-cols-4 border border-solid border-[var(--color-border-2)]'>
        <VerificationMetric
          label={t('preview.artifactVerification.verdict.pass')}
          value={summary.pass}
          tone='success'
        />
        <VerificationMetric
          label={t('preview.artifactVerification.verdict.warn')}
          value={summary.warn}
          tone='warning'
        />
        <VerificationMetric label={t('preview.artifactVerification.verdict.fail')} value={summary.fail} tone='danger' />
        <VerificationMetric
          label={t('preview.artifactVerification.verdict.inconclusive')}
          value={summary.inconclusive}
          tone='neutral'
        />
      </div>

      {loading ? (
        <div className='h-120px flex-center'>
          <Spin size={20} />
        </div>
      ) : failed ? (
        <Empty description={t('preview.artifactVerification.loadFailed')} />
      ) : checks.length === 0 ? (
        <Empty description={t('preview.artifactVerification.empty')} />
      ) : (
        <div className='border-t border-solid border-[var(--color-border-2)]'>
          {checks.map((check) => (
            <VerificationRow key={check.id} check={check} />
          ))}
        </div>
      )}
    </div>
  );
};

type AnnotationDraft = {
  type: SynonBiomedAnnotationType;
  text: string;
  xPercent: number | null;
  yPercent: number | null;
  startLine: number | null;
  endLine: number | null;
  pageNumber: number | null;
  selectionText: string;
  elementSelector: string;
  elementDescriptor: string;
};

const emptyDraft = (): AnnotationDraft => ({
  type: 'point',
  text: '',
  xPercent: null,
  yPercent: null,
  startLine: null,
  endLine: null,
  pageNumber: null,
  selectionText: '',
  elementSelector: '',
  elementDescriptor: '',
});

const draftFromAnnotation = (annotation: SynonBiomedArtifactAnnotation): AnnotationDraft => ({
  type: annotation.type,
  text: annotation.text,
  xPercent: annotation.xPercent,
  yPercent: annotation.yPercent,
  startLine: annotation.startLine,
  endLine: annotation.endLine,
  pageNumber: annotation.pageNumber,
  selectionText: annotation.selectionText ?? '',
  elementSelector: annotation.elementSelector ?? '',
  elementDescriptor: annotation.elementDescriptor ?? '',
});

const toCreateInput = (draft: AnnotationDraft): CreateSynonBiomedArtifactAnnotationInput => ({
  type: draft.type,
  text: draft.text.trim(),
  xPercent: draft.xPercent,
  yPercent: draft.yPercent,
  startLine: draft.startLine,
  endLine: draft.endLine,
  pageNumber: draft.pageNumber,
  selectionText: draft.selectionText.trim() || null,
  elementSelector: draft.elementSelector.trim() || null,
  elementDescriptor: draft.elementDescriptor.trim() || null,
});

const AnnotationEditor: React.FC<{
  draft: AnnotationDraft;
  editing: boolean;
  onChange: (draft: AnnotationDraft) => void;
}> = ({ draft, editing, onChange }) => {
  const { t } = useTranslation();
  return (
    <div className='flex flex-col gap-14px'>
      <label className='flex flex-col gap-6px text-12px text-t-secondary'>
        {t('preview.artifactAnnotations.fields.type')}
        <Select
          aria-label={t('preview.artifactAnnotations.fields.type')}
          value={draft.type}
          disabled={editing}
          onChange={(type) => onChange({ ...draft, type })}
        >
          {ANNOTATION_TYPES.map((type) => (
            <Select.Option key={type} value={type}>
              {t(`preview.artifactAnnotations.types.${type}`)}
            </Select.Option>
          ))}
        </Select>
      </label>
      <label className='flex flex-col gap-6px text-12px text-t-secondary'>
        {t('preview.artifactAnnotations.fields.content')}
        <Input.TextArea
          aria-label={t('preview.artifactAnnotations.fields.content')}
          value={draft.text}
          autoSize={{ minRows: 3, maxRows: 7 }}
          maxLength={4000}
          showWordLimit
          onChange={(text) => onChange({ ...draft, text })}
        />
      </label>
      {!editing && draft.type === 'point' && (
        <>
          <div className='grid grid-cols-2 gap-10px'>
            <NumberField
              label={t('preview.artifactAnnotations.fields.horizontalPosition')}
              value={draft.xPercent}
              onChange={(xPercent) => onChange({ ...draft, xPercent })}
            />
            <NumberField
              label={t('preview.artifactAnnotations.fields.verticalPosition')}
              value={draft.yPercent}
              onChange={(yPercent) => onChange({ ...draft, yPercent })}
            />
          </div>
          <NumberField
            label={t('preview.artifactAnnotations.fields.optionalPage')}
            value={draft.pageNumber}
            onChange={(pageNumber) => onChange({ ...draft, pageNumber })}
          />
        </>
      )}
      {!editing && draft.type === 'text_selection' && (
        <>
          <div className='grid grid-cols-3 gap-10px'>
            <NumberField
              label={t('preview.artifactAnnotations.fields.startLine')}
              value={draft.startLine}
              onChange={(startLine) => onChange({ ...draft, startLine })}
            />
            <NumberField
              label={t('preview.artifactAnnotations.fields.endLine')}
              value={draft.endLine}
              onChange={(endLine) => onChange({ ...draft, endLine })}
            />
            <NumberField
              label={t('preview.artifactAnnotations.fields.page')}
              value={draft.pageNumber}
              onChange={(pageNumber) => onChange({ ...draft, pageNumber })}
            />
          </div>
          <label className='flex flex-col gap-6px text-12px text-t-secondary'>
            {t('preview.artifactAnnotations.fields.selectedText')}
            <Input
              aria-label={t('preview.artifactAnnotations.fields.selectedText')}
              value={draft.selectionText}
              onChange={(selectionText) => onChange({ ...draft, selectionText })}
            />
          </label>
        </>
      )}
      {!editing && draft.type === 'html_element' && (
        <>
          <label className='flex flex-col gap-6px text-12px text-t-secondary'>
            {t('preview.artifactAnnotations.fields.elementSelector')}
            <Input
              aria-label={t('preview.artifactAnnotations.fields.htmlElementSelector')}
              value={draft.elementSelector}
              onChange={(elementSelector) => onChange({ ...draft, elementSelector })}
            />
          </label>
          <label className='flex flex-col gap-6px text-12px text-t-secondary'>
            {t('preview.artifactAnnotations.fields.elementDescription')}
            <Input
              aria-label={t('preview.artifactAnnotations.fields.htmlElementDescription')}
              value={draft.elementDescriptor}
              onChange={(elementDescriptor) => onChange({ ...draft, elementDescriptor })}
            />
          </label>
          <label className='flex flex-col gap-6px text-12px text-t-secondary'>
            {t('preview.artifactAnnotations.fields.elementVisibleText')}
            <Input
              aria-label={t('preview.artifactAnnotations.fields.htmlElementVisibleText')}
              value={draft.selectionText}
              onChange={(selectionText) => onChange({ ...draft, selectionText })}
            />
          </label>
          <div className='grid grid-cols-2 gap-10px'>
            <NumberField
              label={t('preview.artifactAnnotations.fields.horizontalPosition')}
              value={draft.xPercent}
              onChange={(xPercent) => onChange({ ...draft, xPercent })}
            />
            <NumberField
              label={t('preview.artifactAnnotations.fields.verticalPosition')}
              value={draft.yPercent}
              onChange={(yPercent) => onChange({ ...draft, yPercent })}
            />
          </div>
        </>
      )}
    </div>
  );
};

const NumberField: React.FC<{
  label: string;
  value: number | null;
  onChange: (value: number | null) => void;
}> = ({ label, value, onChange }) => (
  <label className='flex flex-col gap-6px text-12px text-t-secondary'>
    {label}
    <InputNumber aria-label={label} value={value ?? undefined} min={0} max={10000} onChange={onChange} />
  </label>
);

const AnnotationRow: React.FC<{
  annotation: SynonBiomedArtifactAnnotation;
  onEdit: (annotation: SynonBiomedArtifactAnnotation) => void;
  onDelete: (annotation: SynonBiomedArtifactAnnotation) => void;
  onRefine: (annotation: SynonBiomedArtifactAnnotation) => void;
}> = ({ annotation, onEdit, onDelete, onRefine }) => {
  const { t } = useTranslation();
  return (
    <article className='px-16px py-12px border-b last:border-b-0 border-x-0 border-t-0 border-solid border-[var(--color-border-2)]'>
      <div className='flex items-start gap-8px'>
        <span className='shrink-0 text-13px font-[600] text-t-primary'>{annotation.label || '•'}</span>
        <div className='min-w-0 flex-1'>
          <div className='whitespace-pre-wrap break-words text-12px leading-19px text-t-primary'>{annotation.text}</div>
          <div className='mt-5px flex flex-wrap gap-x-8px gap-y-2px text-10px text-t-tertiary'>
            <span>{t(`preview.artifactAnnotations.types.${annotation.type}`)}</span>
            {annotation.selectionText && <span className='truncate'>“{annotation.selectionText}”</span>}
            {annotation.pageNumber && (
              <span>{t('preview.artifactAnnotations.pageNumber', { page: annotation.pageNumber })}</span>
            )}
            {annotation.startLine != null && (
              <span>{t('preview.artifactAnnotations.lineNumber', { line: annotation.startLine })}</span>
            )}
            {annotation.xPercent != null && annotation.yPercent != null && (
              <span>
                {annotation.xPercent.toFixed(1)}%, {annotation.yPercent.toFixed(1)}%
              </span>
            )}
            {annotation.elementSelector && (
              <span className='max-w-full truncate font-mono'>{annotation.elementSelector}</span>
            )}
            {annotation.elementDescriptor && (
              <span className='max-w-full truncate'>{annotation.elementDescriptor}</span>
            )}
            {annotation.addressedAt && (
              <span className='text-success-6'>{t('preview.artifactAnnotations.addressed')}</span>
            )}
          </div>
        </div>
        <div className='flex shrink-0'>
          {annotation.selectionText && (
            <Button
              type='text'
              size='mini'
              aria-label={t('preview.artifactAnnotations.refineNamed', { label: annotation.label })}
              title={t('preview.artifactAnnotations.generateSuggestion')}
              icon={<Magic theme='outline' size={13} />}
              onClick={() => onRefine(annotation)}
            />
          )}
          <Button
            type='text'
            size='mini'
            aria-label={t('preview.artifactAnnotations.editNamed', { label: annotation.label })}
            icon={<Edit theme='outline' size={13} />}
            onClick={() => onEdit(annotation)}
          />
          <Button
            type='text'
            size='mini'
            status='danger'
            aria-label={t('preview.artifactAnnotations.deleteNamed', { label: annotation.label })}
            icon={<Delete theme='outline' size={13} />}
            onClick={() => onDelete(annotation)}
          />
        </div>
      </div>
    </article>
  );
};

const VerificationMetric: React.FC<{
  label: string;
  value: number;
  tone: 'success' | 'warning' | 'danger' | 'neutral';
}> = ({ label, value, tone }) => (
  <div className='px-6px py-8px text-center border-r last:border-r-0 border-y-0 border-l-0 border-solid border-[var(--color-border-2)]'>
    <div className={`text-15px font-[600] ${toneClass(tone)}`}>{value}</div>
    <div className='mt-1px text-10px text-t-tertiary'>{label}</div>
  </div>
);

const VerificationRow: React.FC<{ check: SynonBiomedVerificationCheck }> = ({ check }) => {
  const { t } = useTranslation();
  return (
    <article className='px-16px py-12px border-b last:border-b-0 border-x-0 border-t-0 border-solid border-[var(--color-border-2)]'>
      <div className='flex items-center gap-6px'>
        <span className={`text-11px font-[600] ${verdictClass(check.verdict)}`}>
          {t(`preview.artifactVerification.verdict.${check.verdict}`)}
        </span>
        <span className='text-10px text-t-tertiary'>{t(`preview.artifactVerification.status.${check.status}`)}</span>
        {check.severity && <span className='ml-auto text-10px text-t-tertiary'>{check.severity}</span>}
      </div>
      <div className='mt-7px whitespace-pre-wrap text-12px leading-19px text-t-primary'>
        {check.claim ?? t('preview.artifactVerification.noClaim')}
      </div>
      {check.evidence && <div className='mt-6px text-11px leading-18px text-t-secondary'>{check.evidence}</div>}
      {(check.reviewerModel || check.reviewerFrameId) && (
        <div className='mt-6px truncate font-mono text-10px text-t-tertiary'>
          {check.reviewerModel ?? check.reviewerFrameId}
        </div>
      )}
    </article>
  );
};

const verdictClass = (verdict: SynonBiomedVerificationCheck['verdict']): string =>
  ({ pass: 'text-success-6', warn: 'text-warning-6', fail: 'text-danger-6', inconclusive: 'text-t-tertiary' })[verdict];

const toneClass = (tone: 'success' | 'warning' | 'danger' | 'neutral'): string =>
  ({ success: 'text-success-6', warning: 'text-warning-6', danger: 'text-danger-6', neutral: 'text-t-primary' })[tone];
