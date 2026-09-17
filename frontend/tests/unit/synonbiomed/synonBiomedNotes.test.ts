import { describe, expect, it, vi } from 'vitest';
import {
  createSynonBiomedNote,
  deleteSynonBiomedNote,
  loadSynonBiomedNotes,
  updateSynonBiomedNote,
} from '@/renderer/services/synonBiomedNotes';

const target = {
  projectId: 'project/a',
  targetType: 'message' as const,
  targetFrameId: 'frame-1',
  targetMessageIndex: 2,
};

const note = {
  id: 'note-1',
  project_id: 'project/a',
  user_id: 'local',
  target_type: 'message',
  target_frame_id: 'frame-1',
  target_message_index: 2,
  target_artifact_id: null,
  content: 'Evidence note',
  created_at: '2026-07-14T01:00:00Z',
  updated_at: '2026-07-14T01:00:00Z',
  target_name: 'Assay review',
  message_preview: 'Observed response',
};

describe('Synon Biomed notes service', () => {
  it('loads and strictly filters notes for one message target', async () => {
    const fetchImpl = vi.fn(
      async () =>
        new Response(JSON.stringify([note, { ...note, id: 'note-other', target_message_index: 3 }]), { status: 200 })
    );

    await expect(loadSynonBiomedNotes(target, { fetchImpl })).resolves.toEqual([
      expect.objectContaining({ id: 'note-1', targetMessageIndex: 2, content: 'Evidence note' }),
    ]);
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/projects/project%2Fa/notes?target_type=message&target_frame_id=frame-1',
      expect.objectContaining({ credentials: 'include' })
    );
  });

  it('creates, updates and deletes through the real compatibility contract', async () => {
    const fetchImpl = vi.fn(async (_path: string, init?: RequestInit) => {
      if (init?.method === 'DELETE') return new Response(JSON.stringify({ status: 'deleted' }), { status: 200 });
      return new Response(JSON.stringify({ ...note, content: init?.method === 'PATCH' ? 'Updated' : 'Created' }), {
        status: 200,
      });
    });

    await createSynonBiomedNote(target, ' Created ', { fetchImpl });
    expect(fetchImpl).toHaveBeenNthCalledWith(
      1,
      '/api/projects/project%2Fa/notes',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({
          target_type: 'message',
          target_frame_id: 'frame-1',
          target_message_index: 2,
          content: 'Created',
        }),
      })
    );

    await updateSynonBiomedNote('note/1', ' Updated ', { fetchImpl });
    expect(fetchImpl).toHaveBeenNthCalledWith(
      2,
      '/api/notes/note%2F1',
      expect.objectContaining({ method: 'PATCH', body: '{"content":"Updated"}' })
    );

    await deleteSynonBiomedNote('note/1', { fetchImpl });
    expect(fetchImpl).toHaveBeenNthCalledWith(3, '/api/notes/note%2F1', expect.objectContaining({ method: 'DELETE' }));
  });

  it('rejects invalid payloads and preserves backend failure status in diagnostics', async () => {
    await expect(
      loadSynonBiomedNotes(target, { fetchImpl: vi.fn(async () => new Response('{}', { status: 200 })) })
    ).rejects.toThrow('notes response is invalid');

    await expect(
      deleteSynonBiomedNote('missing', {
        fetchImpl: vi.fn(
          async () => new Response(JSON.stringify({ detail: 'Note missing not found' }), { status: 404 })
        ),
      })
    ).rejects.toThrow('404');
  });
});
