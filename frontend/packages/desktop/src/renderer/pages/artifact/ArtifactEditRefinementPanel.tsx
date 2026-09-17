import {
  applySynonBiomedArtifactEdit,
  suggestSynonBiomedArtifactEdit,
  type SynonBiomedAppliedArtifactEdit,
  type SynonBiomedArtifactEditMode,
} from '@/renderer/services/synonBiomedAnnotations';
import { Alert, Button, Input, Modal, Spin } from '@arco-design/web-react';
import { CheckOne, Edit, Refresh } from '@icon-park/react';
import { diffWordsWithSpace } from 'diff';
import React, { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

type SuggestionView = 'diff' | 'full';
type RefinementStatus = 'idle' | 'suggesting' | 'ready' | 'applying' | 'applied';
type DiffChange = { value: string; added?: boolean; removed?: boolean };

export const ArtifactEditRefinementPanel: React.FC<{
  artifactId: string;
  versionId: string;
  selectedText: string;
  initialInstruction: string;
  onClose: () => void;
  onApplied: (result: SynonBiomedAppliedArtifactEdit) => void | Promise<void>;
}> = ({ artifactId, versionId, selectedText, initialInstruction, onClose, onApplied }) => {
  const { t } = useTranslation();
  const [instruction, setInstruction] = useState(initialInstruction);
  const [mode, setMode] = useState<SynonBiomedArtifactEditMode>('edit');
  const [suggestion, setSuggestion] = useState<string | null>(null);
  const [editedSuggestion, setEditedSuggestion] = useState<string | null>(null);
  const [view, setView] = useState<SuggestionView>('diff');
  const [manualEditing, setManualEditing] = useState(false);
  const [status, setStatus] = useState<RefinementStatus>('idle');
  const [error, setError] = useState<string | null>(null);
  const replacement = editedSuggestion ?? suggestion ?? '';

  const changes = useMemo<DiffChange[]>(
    () => (suggestion && mode === 'edit' ? (diffWordsWithSpace(selectedText, replacement) as DiffChange[]) : []),
    [mode, replacement, selectedText, suggestion]
  );

  const generate = async (nextMode: SynonBiomedArtifactEditMode) => {
    const annotationText = instruction.trim();
    if (!annotationText) return;
    setMode(nextMode);
    setStatus('suggesting');
    setError(null);
    try {
      const next = await suggestSynonBiomedArtifactEdit(artifactId, versionId, {
        selectedText,
        annotationText,
        ...(nextMode === 'edit' && mode === 'edit' && replacement && replacement !== selectedText
          ? { currentIteration: replacement }
          : {}),
        mode: nextMode,
      });
      setSuggestion(next);
      setEditedSuggestion(null);
      setManualEditing(false);
      setView(nextMode === 'edit' ? 'diff' : 'full');
      setStatus('ready');
    } catch (reason) {
      console.error('[ArtifactEditRefinementPanel] Failed to generate suggestion', reason);
      setError(t('preview.artifactRefinement.generateFailed'));
      setStatus(suggestion ? 'ready' : 'idle');
    }
  };

  const apply = async () => {
    if (mode !== 'edit' || !replacement || replacement === selectedText) return;
    setStatus('applying');
    setError(null);
    try {
      const result = await applySynonBiomedArtifactEdit(artifactId, versionId, {
        selectedText,
        replacementText: replacement,
      });
      setStatus('applied');
      await onApplied(result);
    } catch (reason) {
      console.error('[ArtifactEditRefinementPanel] Failed to apply edit', reason);
      setError(t('preview.artifactRefinement.applyFailed'));
      setStatus('ready');
    }
  };

  const working = status === 'suggesting' || status === 'applying';
  const canApply = status === 'ready' && mode === 'edit' && Boolean(replacement) && replacement !== selectedText;

  return (
    <Modal
      title={t('preview.artifactRefinement.title')}
      visible
      onCancel={working ? undefined : onClose}
      footer={null}
      unmountOnExit
      style={{ width: 720, maxWidth: 'calc(100vw - 32px)' }}
    >
      <div className='flex flex-col gap-14px' data-testid='artifact-edit-refinement'>
        <section>
          <div className='mb-6px text-11px font-[600] text-t-secondary'>{t('preview.artifactRefinement.editing')}</div>
          <div
            className='max-h-120px overflow-auto border-l-3 border-solid bg-fill-1 px-10px py-8px whitespace-pre-wrap break-words text-12px leading-19px text-t-primary'
            style={{ borderLeftColor: 'rgb(var(--primary-6))' }}
          >
            {selectedText}
          </div>
        </section>

        <label className='flex flex-col gap-6px text-12px text-t-secondary'>
          {t('preview.artifactRefinement.instruction')}
          <Input.TextArea
            aria-label={t('preview.artifactRefinement.instruction')}
            value={instruction}
            onChange={setInstruction}
            autoSize={{ minRows: 2, maxRows: 6 }}
            maxLength={4000}
            disabled={working || status === 'applied'}
            placeholder={t('preview.artifactRefinement.instructionPlaceholder')}
          />
        </label>

        <div className='flex flex-wrap items-center justify-end gap-6px'>
          <Button disabled={!instruction.trim() || working} onClick={() => void generate('ask')}>
            {t('preview.artifactRefinement.ask')}
          </Button>
          <Button
            type='primary'
            icon={<Refresh theme='outline' size={14} />}
            disabled={!instruction.trim() || working}
            onClick={() => void generate('edit')}
          >
            {suggestion ? t('preview.artifactRefinement.regenerate') : t('preview.artifactRefinement.generateEdit')}
          </Button>
        </div>

        {error && <Alert type='error' showIcon content={error} />}

        {status === 'suggesting' && (
          <WorkingState
            label={
              mode === 'ask'
                ? t('preview.artifactRefinement.generatingAnswer')
                : t('preview.artifactRefinement.generatingSuggestion')
            }
          />
        )}
        {status === 'applying' && <WorkingState label={t('preview.artifactRefinement.creatingVersion')} />}
        {status === 'applied' && (
          <div className='min-h-120px flex-center gap-8px text-success-6'>
            <CheckOne theme='outline' size={20} />
            <span className='text-13px font-[600]'>{t('preview.artifactRefinement.applied')}</span>
          </div>
        )}

        {status === 'ready' && suggestion && (
          <section className='border border-solid border-[var(--color-border-2)] bg-1'>
            <header className='min-h-38px px-10px flex flex-wrap items-center justify-between gap-8px border-b border-x-0 border-t-0 border-solid border-[var(--color-border-2)] bg-fill-1'>
              <span className='text-11px font-[600] text-t-secondary'>
                {mode === 'ask' ? t('preview.artifactRefinement.answer') : t('preview.artifactRefinement.suggestion')}
              </span>
              {mode === 'edit' && (
                <div className='flex items-center gap-4px'>
                  <ViewButton active={view === 'diff'} onClick={() => setView('diff')}>
                    {t('preview.artifactRefinement.diff')}
                  </ViewButton>
                  <ViewButton active={view === 'full'} onClick={() => setView('full')}>
                    {t('preview.artifactRefinement.fullText')}
                  </ViewButton>
                  <Button
                    type='text'
                    size='mini'
                    aria-label={t('preview.artifactRefinement.editSuggestion')}
                    icon={<Edit theme='outline' size={13} />}
                    onClick={() => setManualEditing((current) => !current)}
                  />
                </div>
              )}
            </header>
            <div className='max-h-300px overflow-auto px-12px py-10px text-12px leading-20px text-t-primary'>
              {manualEditing && mode === 'edit' ? (
                <Input.TextArea
                  aria-label={t('preview.artifactRefinement.editedSuggestion')}
                  value={replacement}
                  onChange={setEditedSuggestion}
                  autoSize={{ minRows: 5, maxRows: 14 }}
                />
              ) : mode === 'edit' && view === 'diff' ? (
                <div className='whitespace-pre-wrap break-words'>
                  {changes.map((change, index) => (
                    <span
                      key={`${index}-${change.value.slice(0, 12)}`}
                      className={
                        change.added
                          ? 'bg-success-1 text-success-7 underline'
                          : change.removed
                            ? 'bg-danger-1 text-danger-7 line-through'
                            : undefined
                      }
                    >
                      {change.value}
                    </span>
                  ))}
                </div>
              ) : (
                <div className='whitespace-pre-wrap break-words'>{mode === 'ask' ? suggestion : replacement}</div>
              )}
            </div>
          </section>
        )}

        <footer className='flex items-center justify-end gap-8px pt-2px'>
          <Button disabled={working} onClick={onClose}>
            {status === 'applied' ? t('common.close') : t('common.cancel')}
          </Button>
          {mode === 'edit' && status !== 'applied' && (
            <Button type='primary' disabled={!canApply} loading={status === 'applying'} onClick={() => void apply()}>
              {t('preview.artifactRefinement.applyAsVersion')}
            </Button>
          )}
        </footer>
      </div>
    </Modal>
  );
};

const WorkingState: React.FC<{ label: string }> = ({ label }) => (
  <div className='min-h-120px flex-center gap-8px text-t-secondary'>
    <Spin size={18} />
    <span className='text-12px'>{label}</span>
  </div>
);

const ViewButton: React.FC<{ active: boolean; onClick: () => void; children: React.ReactNode }> = ({
  active,
  onClick,
  children,
}) => (
  <button
    type='button'
    className={`h-24px border-0 px-7px text-10px cursor-pointer ${active ? 'text-white' : 'bg-transparent text-t-secondary hover:bg-fill-2'}`}
    style={active ? { backgroundColor: 'rgb(var(--primary-6))' } : undefined}
    onClick={onClick}
  >
    {children}
  </button>
);
