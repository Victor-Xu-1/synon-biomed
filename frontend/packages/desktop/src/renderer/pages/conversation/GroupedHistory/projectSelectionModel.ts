export const ACTIVE_PROJECT_STORAGE_KEY = 'synonbiomed.active-project-id';

export type ProjectSelectionInput = {
  routeProjectId?: string;
  conversationProjectId?: string;
  preferredProjectId?: string;
  availableProjectIds: string[] | null;
};

export type ProjectRecencyInput = {
  projectId: string;
  createdAt?: string | null;
  updatedAt?: string | null;
  lastActiveAt?: string | null;
};

const timestamp = (value: string | null | undefined): number => {
  if (!value) return Number.NEGATIVE_INFINITY;
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : Number.NEGATIVE_INFINITY;
};

export const resolveMostRecentProjectId = (projects: readonly ProjectRecencyInput[]): string | null => {
  let selectedProjectId: string | null = null;
  let selectedTimestamp = Number.NEGATIVE_INFINITY;

  for (const project of projects) {
    const projectTimestamp = Math.max(
      timestamp(project.lastActiveAt),
      timestamp(project.updatedAt),
      timestamp(project.createdAt)
    );
    if (selectedProjectId === null || projectTimestamp > selectedTimestamp) {
      selectedProjectId = project.projectId;
      selectedTimestamp = projectTimestamp;
    }
  }

  return selectedProjectId;
};

export const resolveActiveProjectId = ({
  routeProjectId,
  conversationProjectId,
  preferredProjectId,
  availableProjectIds,
}: ProjectSelectionInput): string | null => {
  const explicitProjectId = routeProjectId || conversationProjectId;
  const isAvailable = (projectId: string): boolean =>
    availableProjectIds === null || availableProjectIds.includes(projectId);

  if (explicitProjectId && isAvailable(explicitProjectId)) return explicitProjectId;
  if (preferredProjectId && isAvailable(preferredProjectId)) return preferredProjectId;
  return availableProjectIds?.[0] ?? null;
};

export const readPreferredProjectId = (): string | undefined => {
  if (typeof window === 'undefined') return undefined;
  try {
    return window.localStorage.getItem(ACTIVE_PROJECT_STORAGE_KEY) || undefined;
  } catch {
    return undefined;
  }
};

export const persistPreferredProjectId = (projectId: string | null): void => {
  if (typeof window === 'undefined') return;
  try {
    if (projectId) window.localStorage.setItem(ACTIVE_PROJECT_STORAGE_KEY, projectId);
    else window.localStorage.removeItem(ACTIVE_PROJECT_STORAGE_KEY);
  } catch {
    // Storage may be unavailable in hardened or private browser contexts.
  }
};
