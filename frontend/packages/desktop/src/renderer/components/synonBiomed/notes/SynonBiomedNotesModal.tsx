import {
  createSynonBiomedNote,
  deleteSynonBiomedNote,
  loadSynonBiomedNotes,
  updateSynonBiomedNote,
  type SynonBiomedNote,
  type SynonBiomedNoteTarget,
} from '@/renderer/services/synonBiomedNotes';
import { Button, Empty, Input, Message, Modal, Popconfirm, Spin } from '@arco-design/web-react';
import { Delete, Edit, Plus, Refresh } from '@icon-park/react';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import MarkdownView from '@/renderer/components/Markdown';
import { redactErrorText } from '@/renderer/pages/conversation/platforms/acp/errorDiagnostics';

type Props = {
  visible: boolean;
  target: SynonBiomedNoteTarget;
  onClose: () => void;
};

const SynonBiomedNotesModal: React.FC<Props> = ({ visible, target, onClose }) => {
  const { t, i18n } = useTranslation();
  const [notes, setNotes] = useState<SynonBiomedNote[]>([]);
  const [draft, setDraft] = useState('');
  const [editingId, setEditingId] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const generation = useRef(0);

  const load = useCallback(async () => {
    const current = ++generation.current;
    setLoading(true);
    setError(null);
    try {
      const next = await loadSynonBiomedNotes(target);
      if (generation.current === current) setNotes(next);
    } catch (reason) {
      console.warn('[SynonBiomedNotesModal] Failed to load notes:', diagnostic(reason));
      if (generation.current === current) {
        setNotes([]);
        setError(t('conversation.notes.loadFailed'));
      }
    } finally {
      if (generation.current === current) setLoading(false);
    }
  }, [t, target]);

  useEffect(() => {
    if (!visible) return;
    setDraft('');
    setEditingId(null);
    void load();
    return () => {
      generation.current += 1;
    };
  }, [load, visible]);

  const save = async () => {
    if (!draft.trim() || saving) return;
    setSaving(true);
    try {
      const saved = editingId
        ? await updateSynonBiomedNote(editingId, draft)
        : await createSynonBiomedNote(target, draft);
      setNotes((current) =>
        editingId ? current.map((note) => (note.id === saved.id ? saved : note)) : [saved, ...current]
      );
      setDraft('');
      setEditingId(null);
    } catch (reason) {
      console.warn('[SynonBiomedNotesModal] Failed to save note:', diagnostic(reason));
      Message.error(t('conversation.notes.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (noteId: string) => {
    try {
      await deleteSynonBiomedNote(noteId);
      setNotes((current) => current.filter((note) => note.id !== noteId));
      if (editingId === noteId) {
        setEditingId(null);
        setDraft('');
      }
    } catch (reason) {
      console.warn('[SynonBiomedNotesModal] Failed to delete note:', diagnostic(reason));
      Message.error(t('conversation.notes.deleteFailed'));
    }
  };

  return (
    <Modal
      title={t('conversation.notes.title')}
      visible={visible}
      onCancel={onClose}
      footer={null}
      unmountOnExit
      style={{ width: 620, maxWidth: 'calc(100vw - 24px)' }}
    >
      <div className='flex flex-col gap-12px' data-testid='synon-biomed-notes-modal'>
        <Input.TextArea
          value={draft}
          onChange={setDraft}
          autoSize={{ minRows: 3, maxRows: 8 }}
          maxLength={20_000}
          showWordLimit
          placeholder={editingId ? t('conversation.notes.editPlaceholder') : t('conversation.notes.addPlaceholder')}
          aria-label={t('conversation.notes.content')}
        />
        <div className='flex justify-end gap-8px'>
          {editingId && (
            <Button
              onClick={() => {
                setEditingId(null);
                setDraft('');
              }}
            >
              {t('conversation.notes.cancelEditing')}
            </Button>
          )}
          <Button type='primary' icon={<Plus />} loading={saving} disabled={!draft.trim()} onClick={() => void save()}>
            {editingId ? t('conversation.notes.saveChanges') : t('conversation.notes.add')}
          </Button>
        </div>

        {loading ? (
          <div className='min-h-120px flex items-center justify-center' aria-label={t('conversation.notes.loading')}>
            <Spin />
          </div>
        ) : error ? (
          <div className='min-h-120px flex flex-col items-center justify-center gap-12px text-t-secondary'>
            <span role='alert'>{error}</span>
            <Button icon={<Refresh />} onClick={() => void load()}>
              {t('common.retry')}
            </Button>
          </div>
        ) : notes.length === 0 ? (
          <Empty description={t('conversation.notes.empty')} />
        ) : (
          <div className='max-h-360px overflow-y-auto flex flex-col gap-8px'>
            {notes.map((note) => (
              <div key={note.id} className='border border-solid border-b-1 rounded-6px p-12px'>
                <div className='break-words text-14px'>
                  <MarkdownView>{note.content}</MarkdownView>
                </div>
                <div className='mt-8px flex items-center justify-between gap-8px text-12px text-t-secondary'>
                  <span>{new Date(note.updatedAt).toLocaleString(i18n.language)}</span>
                  <div className='flex gap-4px'>
                    <Button
                      type='text'
                      size='small'
                      icon={<Edit />}
                      aria-label={t('conversation.notes.edit')}
                      onClick={() => {
                        setEditingId(note.id);
                        setDraft(note.content);
                      }}
                    />
                    <Popconfirm title={t('conversation.notes.deleteConfirm')} onOk={() => remove(note.id)}>
                      <Button
                        type='text'
                        status='danger'
                        size='small'
                        icon={<Delete />}
                        aria-label={t('conversation.notes.delete')}
                      />
                    </Popconfirm>
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </Modal>
  );
};

export default SynonBiomedNotesModal;

const diagnostic = (error: unknown): string =>
  redactErrorText(error instanceof Error ? error.message : String(error || 'unknown error'));
