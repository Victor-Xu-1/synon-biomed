export type SynonBiomedNoteTargetType = 'message' | 'bench' | 'artifact';

export type SynonBiomedNote = {
  id: string;
  projectId: string;
  targetType: SynonBiomedNoteTargetType;
  targetFrameId: string;
  targetMessageIndex: number | null;
  targetArtifactId: string | null;
  content: string;
  createdAt: string;
  updatedAt: string;
  targetName: string | null;
  messagePreview: string | null;
};

export type SynonBiomedNoteTarget = {
  projectId: string;
  targetType: SynonBiomedNoteTargetType;
  targetFrameId: string;
  targetMessageIndex?: number;
  targetArtifactId?: string;
};

export type SynonBiomedNotesOptions = {
  fetchImpl?: typeof fetch;
};

type RecordValue = Record<string, unknown>;

export async function loadSynonBiomedNotes(
  target: SynonBiomedNoteTarget,
  options: SynonBiomedNotesOptions = {}
): Promise<SynonBiomedNote[]> {
  const search = new URLSearchParams({
    target_type: target.targetType,
    target_frame_id: target.targetFrameId,
  });
  const payload = await requestJson<unknown>(
    `/api/projects/${encodeURIComponent(target.projectId)}/notes?${search.toString()}`,
    undefined,
    options
  );
  if (!Array.isArray(payload)) throw new Error('Synon Biomed notes response is invalid');
  return payload
    .map(toNote)
    .filter((note): note is SynonBiomedNote => note !== null)
    .filter(matchesTarget(target));
}

export async function createSynonBiomedNote(
  target: SynonBiomedNoteTarget,
  content: string,
  options: SynonBiomedNotesOptions = {}
): Promise<SynonBiomedNote> {
  const value = content.trim();
  if (!value) throw new Error('Note content is required');
  const note = toNote(
    await requestJson<unknown>(
      `/api/projects/${encodeURIComponent(target.projectId)}/notes`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          target_type: target.targetType,
          target_frame_id: target.targetFrameId,
          ...(target.targetMessageIndex === undefined ? {} : { target_message_index: target.targetMessageIndex }),
          ...(target.targetArtifactId === undefined ? {} : { target_artifact_id: target.targetArtifactId }),
          content: value,
        }),
      },
      options
    )
  );
  if (!note) throw new Error('Synon Biomed create note response is invalid');
  return note;
}

export async function updateSynonBiomedNote(
  noteId: string,
  content: string,
  options: SynonBiomedNotesOptions = {}
): Promise<SynonBiomedNote> {
  const value = content.trim();
  if (!value) throw new Error('Note content is required');
  const note = toNote(
    await requestJson<unknown>(
      `/api/notes/${encodeURIComponent(noteId)}`,
      { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ content: value }) },
      options
    )
  );
  if (!note) throw new Error('Synon Biomed update note response is invalid');
  return note;
}

export async function deleteSynonBiomedNote(noteId: string, options: SynonBiomedNotesOptions = {}): Promise<void> {
  await requestJson(`/api/notes/${encodeURIComponent(noteId)}`, { method: 'DELETE' }, options);
}

function matchesTarget(target: SynonBiomedNoteTarget): (note: SynonBiomedNote) => boolean {
  return (note) => {
    if (target.targetMessageIndex !== undefined && note.targetMessageIndex !== target.targetMessageIndex) return false;
    if (target.targetArtifactId !== undefined && note.targetArtifactId !== target.targetArtifactId) return false;
    return true;
  };
}

function toNote(value: unknown): SynonBiomedNote | null {
  const record = asRecord(value);
  const id = stringValue(record?.id);
  const projectId = stringValue(record?.project_id);
  const targetType = stringValue(record?.target_type);
  const targetFrameId = stringValue(record?.target_frame_id);
  const content = stringValue(record?.content);
  const createdAt = stringValue(record?.created_at);
  const updatedAt = stringValue(record?.updated_at);
  if (
    !id ||
    !projectId ||
    !isTargetType(targetType) ||
    !targetFrameId ||
    content === null ||
    !createdAt ||
    !updatedAt
  ) {
    return null;
  }
  return {
    id,
    projectId,
    targetType,
    targetFrameId,
    targetMessageIndex: nullableNumber(record?.target_message_index),
    targetArtifactId: stringValue(record?.target_artifact_id),
    content,
    createdAt,
    updatedAt,
    targetName: stringValue(record?.target_name),
    messagePreview: stringValue(record?.message_preview),
  };
}

async function requestJson<T>(
  path: string,
  init: RequestInit | undefined,
  options: SynonBiomedNotesOptions
): Promise<T> {
  const response = await (options.fetchImpl ?? fetch)(path, {
    ...init,
    credentials: 'include',
    headers: { Accept: 'application/json', ...init?.headers },
  });
  if (!response.ok) {
    const detail = await response.text().catch(() => '');
    throw new Error(`Synon Biomed notes request failed: ${response.status}${detail ? ` ${detail.slice(0, 300)}` : ''}`);
  }
  const text = await response.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

function asRecord(value: unknown): RecordValue | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? (value as RecordValue) : null;
}

function stringValue(value: unknown): string | null {
  return typeof value === 'string' ? value : null;
}

function nullableNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isInteger(value) ? value : null;
}

function isTargetType(value: string | null): value is SynonBiomedNoteTargetType {
  return value === 'message' || value === 'bench' || value === 'artifact';
}
