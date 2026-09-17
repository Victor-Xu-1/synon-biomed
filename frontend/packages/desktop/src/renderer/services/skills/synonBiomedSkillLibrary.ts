export type SynonBiomedSkillLibraryOptions = {
  baseUrl?: string;
  fetchImpl?: typeof fetch;
};

export type SynonBiomedSkillDraft = {
  name: string;
  displayName: string;
  description: string;
  files: string[];
  updatedAt: string | null;
};

export type SynonBiomedSaveConversationSkillResult = {
  name: string;
  installedPath: string;
  files: string[];
  source: string;
  scope: 'personal';
};

export type SynonBiomedSkillSource = {
  slug: string;
  repo: string;
  sha: string;
  license: string | null;
  skills: string[];
  importedAt: string | null;
  removable: boolean;
};

export type SynonBiomedSkillRepoEntry = {
  name: string;
  displayName: string;
  description: string;
  path: string | null;
  selected: boolean;
};

export type SynonBiomedSkillRepoPreview = {
  repo: string;
  slug: string;
  sha: string;
  license: string | null;
  skills: SynonBiomedSkillRepoEntry[];
};

export type SynonBiomedSkillImportResult = {
  imported: string[];
  skipped: Array<{ name: string; reason: string }>;
  slug: string | null;
  sha: string | null;
};

export class SynonBiomedSkillLibraryError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'SynonBiomedSkillLibraryError';
    this.status = status;
  }
}

type RecordValue = Record<string, unknown>;

export async function loadSynonBiomedSkillDrafts(
  options: SynonBiomedSkillLibraryOptions = {}
): Promise<SynonBiomedSkillDraft[]> {
  const payload = asRecord(await requestJson('/api/skills/drafts', options));
  const drafts = Array.isArray(payload?.drafts) ? payload.drafts : [];
  return drafts.map(toDraft).filter((draft): draft is SynonBiomedSkillDraft => draft !== null);
}

export async function saveSynonBiomedConversationAsSkill(
  conversationId: string,
  options: SynonBiomedSkillLibraryOptions = {}
): Promise<SynonBiomedSaveConversationSkillResult> {
  const payload = asRecord(
    await requestJson('/api/skills/from-conversation', options, {
      method: 'POST',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify({ conversation_id: conversationId }),
    })
  );
  const name = stringValue(payload?.name).trim();
  if (!payload || payload.ok !== true || !name) {
    throw new SynonBiomedSkillLibraryError(502, 'Personal Skill save response is invalid');
  }
  return {
    name,
    installedPath: stringValue(payload.installed_path ?? payload.installedPath),
    files: Array.isArray(payload.files)
      ? payload.files.filter((value): value is string => typeof value === 'string')
      : [],
    source: stringValue(payload.source),
    scope: 'personal',
  };
}

export async function loadSynonBiomedSkillSources(
  options: SynonBiomedSkillLibraryOptions = {}
): Promise<SynonBiomedSkillSource[]> {
  const payload = asRecord(await requestJson('/api/marketplace/sources', options));
  const sources = Array.isArray(payload?.sources) ? payload.sources : [];
  return sources.map(toSource).filter((source): source is SynonBiomedSkillSource => source !== null);
}

export async function previewSynonBiomedSkillRepository(
  repo: string,
  options: SynonBiomedSkillLibraryOptions = {}
): Promise<SynonBiomedSkillRepoPreview> {
  const payload = asRecord(
    await requestJson('/api/marketplace/preview', options, {
      method: 'POST',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify({ repo }),
    })
  );
  if (!payload) throw new SynonBiomedSkillLibraryError(502, 'Skill repository preview response is invalid');
  const skills = Array.isArray(payload.skills) ? payload.skills : [];
  return {
    repo: stringValue(payload.repo) || repo,
    slug: stringValue(payload.slug),
    sha: stringValue(payload.sha),
    license: nullableStringValue(payload.license),
    skills: skills.map(toRepoEntry).filter((skill): skill is SynonBiomedSkillRepoEntry => skill !== null),
  };
}

export async function importSynonBiomedRepositorySkills(
  preview: SynonBiomedSkillRepoPreview,
  skillNames: string[],
  options: SynonBiomedSkillLibraryOptions = {}
): Promise<SynonBiomedSkillImportResult> {
  return toImportResult(
    await requestJson('/api/marketplace/import', options, {
      method: 'POST',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify({
        repo: preview.repo,
        sha: preview.sha,
        skills: skillNames,
        ...(preview.license ? { license: preview.license } : {}),
      }),
    })
  );
}

export async function importSynonBiomedSkillFile(
  file: File,
  name: string | undefined,
  options: SynonBiomedSkillLibraryOptions = {}
): Promise<RecordValue> {
  const body = new FormData();
  body.append('file', file, file.name);
  if (name?.trim()) body.append('name', name.trim());
  const payload = await requestJson('/api/skills/import', options, {
    method: 'POST',
    headers: { Accept: 'application/json' },
    body,
  });
  return asRecord(payload) ?? {};
}

export async function loadSynonBiomedSkillFiles(
  name: string,
  options: SynonBiomedSkillLibraryOptions = {}
): Promise<string[]> {
  const payload = asRecord(await requestJson(`/api/skills/catalog/${encodeURIComponent(name)}/files`, options));
  return Array.isArray(payload?.files)
    ? payload.files.filter((value): value is string => typeof value === 'string')
    : [];
}

export async function loadSynonBiomedSkillFileContent(
  name: string,
  path: string,
  options: SynonBiomedSkillLibraryOptions = {}
): Promise<string> {
  const payload = asRecord(
    await requestJson(
      `/api/skills/catalog/${encodeURIComponent(name)}/content?path=${encodeURIComponent(path)}`,
      options
    )
  );
  return stringValue(payload?.content);
}

export async function saveSynonBiomedSkillDraftFile(
  name: string,
  path: string,
  oldContent: string,
  newContent: string,
  options: SynonBiomedSkillLibraryOptions = {}
): Promise<RecordValue> {
  const payload = await requestJson(`/api/skills/${encodeURIComponent(name)}/edit`, options, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, old_string: oldContent, new_string: newContent }),
  });
  return asRecord(payload) ?? {};
}

export async function duplicateSynonBiomedSkill(
  sourceName: string,
  name: string,
  options: SynonBiomedSkillLibraryOptions = {}
): Promise<RecordValue> {
  const payload = await requestJson(`/api/skills/${encodeURIComponent(name)}/duplicate`, options, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({ sourceName }),
  });
  return asRecord(payload) ?? {};
}

export async function publishSynonBiomedSkillDraft(
  name: string,
  overwrite: boolean,
  options: SynonBiomedSkillLibraryOptions = {}
): Promise<RecordValue> {
  const payload = await requestJson(`/api/skills/${encodeURIComponent(name)}/publish`, options, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({ overwrite }),
  });
  return asRecord(payload) ?? {};
}

export async function deleteSynonBiomedSkillDraft(
  name: string,
  options: SynonBiomedSkillLibraryOptions = {}
): Promise<void> {
  await requestJson(`/api/skills/${encodeURIComponent(name)}`, options, { method: 'DELETE' });
}

export async function deleteSynonBiomedPersonalSkill(
  name: string,
  options: SynonBiomedSkillLibraryOptions = {}
): Promise<void> {
  await requestJson(`/api/skills/${encodeURIComponent(name)}/full`, options, { method: 'DELETE' });
}

export async function removeSynonBiomedSkillSource(
  slug: string,
  options: SynonBiomedSkillLibraryOptions = {}
): Promise<void> {
  await requestJson(`/api/marketplace/sources/${encodeURIComponent(slug)}`, options, { method: 'DELETE' });
}

function toDraft(value: unknown): SynonBiomedSkillDraft | null {
  if (typeof value === 'string') {
    const name = value.trim();
    if (!name) return null;
    return {
      name,
      displayName: humanizeSkillName(name),
      description: '',
      files: [],
      updatedAt: null,
    };
  }
  const row = asRecord(value);
  const name = stringValue(row?.name);
  if (!row || !name) return null;
  return {
    name,
    displayName: stringValue(row.displayName ?? row.display_name) || name,
    description: stringValue(row.description),
    files: Array.isArray(row.files) ? row.files.filter((item): item is string => typeof item === 'string') : [],
    updatedAt: nullableStringValue(row.updatedAt ?? row.updated_at),
  };
}

function humanizeSkillName(value: string): string {
  return value
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(' ');
}

function toSource(value: unknown): SynonBiomedSkillSource | null {
  const row = asRecord(value);
  const slug = stringValue(row?.slug);
  if (!row || !slug) return null;
  return {
    slug,
    repo: stringValue(row.repo ?? row.url),
    sha: stringValue(row.sha ?? row.pinnedSha ?? row.pinned_sha),
    license: nullableStringValue(row.license),
    skills: toStringArray(row.skills ?? row.skillNames ?? row.skill_names),
    importedAt: nullableStringValue(row.importedAt ?? row.imported_at ?? row.lastImportedAt ?? row.last_imported_at),
    removable: row.removable !== false,
  };
}

function toRepoEntry(value: unknown): SynonBiomedSkillRepoEntry | null {
  const row = asRecord(value);
  const name = stringValue(row?.name);
  if (!row || !name) return null;
  return {
    name,
    displayName: stringValue(row.displayName ?? row.display_name) || name,
    description: stringValue(row.description),
    path: nullableStringValue(row.path),
    selected: row.selected !== false,
  };
}

function toImportResult(value: unknown): SynonBiomedSkillImportResult {
  const row = asRecord(value);
  const skipped = Array.isArray(row?.skipped) ? row.skipped : [];
  return {
    imported: toStringArray(row?.imported),
    skipped: skipped
      .map((entry) => {
        const item = asRecord(entry);
        const name = stringValue(item?.name);
        return item && name ? { name, reason: stringValue(item.reason) } : null;
      })
      .filter((entry): entry is { name: string; reason: string } => entry !== null),
    slug: nullableStringValue(row?.slug),
    sha: nullableStringValue(row?.sha),
  };
}

async function requestJson(
  path: string,
  options: SynonBiomedSkillLibraryOptions,
  init: RequestInit = { headers: { Accept: 'application/json' } }
): Promise<unknown> {
  const response = await (options.fetchImpl ?? fetch)(`${normalizeBaseUrl(options.baseUrl)}${path}`, init);
  const payload = await parsePayload(response);
  if (!response.ok) {
    const row = asRecord(payload);
    throw new SynonBiomedSkillLibraryError(
      response.status,
      stringValue(row?.detail ?? row?.message ?? row?.error) || `Skill library request failed (${response.status})`
    );
  }
  return payload;
}

async function parsePayload(response: Response): Promise<unknown> {
  if (response.status === 204) return null;
  const text = await response.text();
  if (!text) return null;
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return text;
  }
}

function normalizeBaseUrl(value: string | undefined): string {
  return (value ?? '').replace(/\/$/, '');
}

function asRecord(value: unknown): RecordValue | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as RecordValue) : null;
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : value == null ? '' : String(value);
}

function nullableStringValue(value: unknown): string | null {
  const normalized = stringValue(value).trim();
  return normalized || null;
}

function toStringArray(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : [];
}
