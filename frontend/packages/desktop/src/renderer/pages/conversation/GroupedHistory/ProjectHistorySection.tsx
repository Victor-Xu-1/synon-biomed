import {
  createSynonBiomedProject,
  deleteSynonBiomedProject,
  loadSynonBiomedProjectArtifacts,
  loadSynonBiomedProjects,
  updateSynonBiomedProject,
  type SynonBiomedProject,
  type SynonBiomedProjectInput,
} from '@/renderer/services/synonBiomedGateway';
import { downloadFileFromUrl } from '@/renderer/utils/file/download';
import { copyText } from '@/renderer/utils/ui/clipboard';
import { loadProjectSidebarOrder, saveProjectSidebarOrder } from '@/renderer/services/projectSidebarOrder';
import { emitter, useAddEventListener } from '@/renderer/utils/emitter';
import { Button, Checkbox, Empty, Message, Modal, Spin, Tooltip } from '@arco-design/web-react';
import { Copy, DeleteOne, Download, Drag, EditOne, MoreOne, Plus } from '@icon-park/react';
import classNames from 'classnames';
import React, { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { useLocation, useNavigate } from 'react-router';
import { SiderSectionHeader } from './SiderSectionHeader';
import { moveProjectByOffset, reconcileProjectOrder, reorderProjectIds, sameProjectOrder } from './projectOrderModel';
import { redactErrorText } from '@/renderer/pages/conversation/platforms/acp/errorDiagnostics';
import {
  resolveProjectNavigationTarget,
  type ProjectNavigationTarget,
} from '@/renderer/pages/project/projectNavigationModel';

const BatchSelectionPanel = React.lazy(() => import('./BatchSelectionPanel'));
const ProjectEditorModal = React.lazy(() => import('./ProjectEditorModal'));

type ProjectHistorySectionProps = {
  collapsed: boolean;
  activeProjectId: string | null;
  onProjectsChange?: (projects: SynonBiomedProject[]) => void;
  onProjectSelect?: (projectId: string) => void;
  resolveImmediateProjectTarget?: (projectId: string) => ProjectNavigationTarget | null;
  onPrepareNavigation?: (target: ProjectNavigationTarget) => Promise<void>;
  batchMode?: boolean;
  onBatchModeChange?: (value: boolean) => void;
};

type EditorState = { mode: 'create'; project: null } | { mode: 'edit'; project: SynonBiomedProject } | null;
type ProjectOrderError = 'saveRollback' | 'saveFailed' | 'loadFailed';

const ProjectHistorySection: React.FC<ProjectHistorySectionProps> = ({
  collapsed,
  activeProjectId,
  onProjectsChange,
  onProjectSelect,
  resolveImmediateProjectTarget,
  onPrepareNavigation,
  batchMode = false,
  onBatchModeChange,
}) => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const sectionRef = useRef<HTMLElement | null>(null);
  const navigationIntentRef = useRef(0);
  const [projects, setProjects] = useState<SynonBiomedProject[]>([]);
  const [projectsExpanded, setProjectsExpanded] = useState(true);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const [editor, setEditor] = useState<EditorState>(null);
  const [deleteTarget, setDeleteTarget] = useState<SynonBiomedProject | null>(null);
  const [menuProjectId, setMenuProjectId] = useState<string | null>(null);
  const [mutationLoading, setMutationLoading] = useState(false);
  const [downloadProjectId, setDownloadProjectId] = useState<string | null>(null);
  const [mutationError, setMutationError] = useState<string | null>(null);
  const [selectedProjectIds, setSelectedProjectIds] = useState<Set<string>>(new Set());
  const [batchDeleteVisible, setBatchDeleteVisible] = useState(false);
  const [persistedOrder, setPersistedOrder] = useState<string[] | null>(null);
  const [orderError, setOrderError] = useState<ProjectOrderError | null>(null);
  const [orderSaving, setOrderSaving] = useState(false);
  const [orderLoadedFromServer, setOrderLoadedFromServer] = useState(false);
  const [draggedProjectId, setDraggedProjectId] = useState<string | null>(null);
  const [dropTargetId, setDropTargetId] = useState<string | null>(null);
  const [orderAnnouncement, setOrderAnnouncement] = useState('');
  const projectsRef = useRef<SynonBiomedProject[]>([]);
  const confirmedOrderRef = useRef<string[]>([]);
  const saveQueueRef = useRef<Promise<void>>(Promise.resolve());
  const saveVersionRef = useRef(0);
  const projectsRequestVersionRef = useRef(0);

  // Visible reorder handles must read the committed list before input can arrive.
  useLayoutEffect(() => {
    projectsRef.current = projects;
  }, [projects]);

  const persistOrder = useCallback((nextOrder: string[], rollbackOnFailure = false) => {
    const version = ++saveVersionRef.current;
    setPersistedOrder(nextOrder);
    setOrderError(null);
    setOrderSaving(true);

    const write = saveQueueRef.current.catch((): void => undefined).then(() => saveProjectSidebarOrder(nextOrder));
    saveQueueRef.current = write;
    void write
      .then(() => {
        confirmedOrderRef.current = nextOrder;
      })
      .catch((error: unknown) => {
        console.warn('[ProjectHistorySection] Failed to save project order:', diagnostic(error));
        if (version !== saveVersionRef.current) return;
        if (rollbackOnFailure) {
          const rollbackOrder = reconcileProjectOrder(confirmedOrderRef.current, projectIds(projectsRef.current));
          setPersistedOrder(rollbackOrder);
          setProjects((current) => orderProjects(current, rollbackOrder));
          setOrderError('saveRollback');
          return;
        }
        setOrderError('saveFailed');
      })
      .finally(() => {
        if (version === saveVersionRef.current) setOrderSaving(false);
      });
  }, []);

  useEffect(() => {
    let active = true;
    void loadProjectSidebarOrder()
      .then((savedOrder) => {
        if (!active) return;
        confirmedOrderRef.current = savedOrder;
        setOrderLoadedFromServer(true);
        setPersistedOrder(savedOrder);
        setProjects((current) => orderProjects(current, reconcileProjectOrder(savedOrder, projectIds(current))));
        setOrderError(null);
      })
      .catch((error: unknown) => {
        console.warn('[ProjectHistorySection] Failed to load project order:', diagnostic(error));
        if (!active) return;
        setOrderLoadedFromServer(false);
        setPersistedOrder(projectIds(projectsRef.current));
        setOrderError('loadFailed');
      });
    return () => {
      active = false;
    };
  }, []);

  useEffect(() => {
    if (loading || failed || persistedOrder === null || !orderLoadedFromServer) return;
    const reconciled = reconcileProjectOrder(persistedOrder, projectIds(projects));
    setProjects((current) =>
      sameProjectOrder(projectIds(current), reconciled) ? current : orderProjects(current, reconciled)
    );
    if (!sameProjectOrder(persistedOrder, reconciled)) persistOrder(reconciled);
  }, [failed, loading, orderLoadedFromServer, persistOrder, persistedOrder, projects]);

  const commitProjectOrder = useCallback(
    (nextOrder: string[], announcement: string) => {
      const previousOrder = projectIds(projectsRef.current);
      if (sameProjectOrder(previousOrder, nextOrder)) return;
      setProjects((current) => orderProjects(current, nextOrder));
      setOrderAnnouncement(announcement);
      setOrderLoadedFromServer(true);
      persistOrder(nextOrder, true);
    },
    [persistOrder]
  );

  const handleProjectDrop = useCallback(
    (targetId: string) => {
      if (!draggedProjectId) return;
      const nextOrder = reorderProjectIds(projectIds(projectsRef.current), draggedProjectId, targetId);
      const moved = projectsRef.current.find((project) => project.projectId === draggedProjectId);
      commitProjectOrder(
        nextOrder,
        t('conversation.history.projectOrder.moved', {
          name: moved?.name ?? draggedProjectId,
        })
      );
      setDraggedProjectId(null);
      setDropTargetId(null);
    },
    [commitProjectOrder, draggedProjectId, t]
  );

  const handleProjectKeyboardMove = useCallback(
    (project: SynonBiomedProject, offset: -1 | 1) => {
      const currentOrder = projectIds(projectsRef.current);
      const nextOrder = moveProjectByOffset(currentOrder, project.projectId, offset);
      const nextIndex = nextOrder.indexOf(project.projectId);
      commitProjectOrder(
        nextOrder,
        t('conversation.history.projectOrder.movedToPosition', {
          name: project.name,
          position: nextIndex + 1,
        })
      );
    },
    [commitProjectOrder, t]
  );

  useEffect(() => {
    if (!batchMode) {
      setSelectedProjectIds(new Set());
      setBatchDeleteVisible(false);
    }
  }, [batchMode]);

  useEffect(() => {
    if (!batchMode || selectedProjectIds.size === 0) return;
    const existingIds = new Set(projects.map((project) => project.projectId));
    setSelectedProjectIds((current) => new Set([...current].filter((projectId) => existingIds.has(projectId))));
  }, [batchMode, projects, selectedProjectIds.size]);

  const refreshProjects = useCallback((showLoading = false) => {
    const requestVersion = ++projectsRequestVersionRef.current;
    if (showLoading) setLoading(true);

    void loadSynonBiomedProjects()
      .then((items) => {
        if (requestVersion !== projectsRequestVersionRef.current) return;
        setProjects(items);
        setFailed(false);
      })
      .catch((error: unknown) => {
        console.warn('[ProjectHistorySection] Failed to load projects:', diagnostic(error));
        if (requestVersion !== projectsRequestVersionRef.current) return;
        setProjects([]);
        setFailed(true);
      })
      .finally(() => {
        if (requestVersion === projectsRequestVersionRef.current) setLoading(false);
      });
  }, []);

  useEffect(() => refreshProjects(true), [refreshProjects]);
  useAddEventListener('synonbiomed.projects.refresh', () => void refreshProjects(false), [refreshProjects]);
  useAddEventListener(
    'synonbiomed.projects.create',
    () => {
      setMutationError(null);
      setEditor({ mode: 'create', project: null });
    },
    []
  );

  useEffect(() => {
    if (!loading && !failed) onProjectsChange?.(projects);
  }, [failed, loading, onProjectsChange, projects]);

  useEffect(() => {
    if (!menuProjectId) return;
    const closeOnOutside = (event: MouseEvent) => {
      const target = event.target as Element | null;
      if (!sectionRef.current?.contains(target) && !target?.closest('[data-synon-sidebar-project-menu]')) {
        setMenuProjectId(null);
      }
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setMenuProjectId(null);
    };
    document.addEventListener('mousedown', closeOnOutside);
    window.addEventListener('keydown', closeOnEscape);
    return () => {
      document.removeEventListener('mousedown', closeOnOutside);
      window.removeEventListener('keydown', closeOnEscape);
    };
  }, [menuProjectId]);

  const sectionLabel = t('conversation.history.projectsList');
  const createProjectLabel = t('conversation.history.projectsSection');
  const projectMenuLabel = (name: string) => t('conversation.history.projectMenu.label', { name });

  const openProject = useCallback(
    async (projectId: string) => {
      const navigationIntent = ++navigationIntentRef.current;
      onProjectSelect?.(projectId);
      const immediateTarget = resolveImmediateProjectTarget?.(projectId) ?? null;
      if (!immediateTarget) {
        Message.error(t('conversation.history.projectOpenFailedDescription'));
        return;
      }
      await onPrepareNavigation?.(immediateTarget);
      if (navigationIntent !== navigationIntentRef.current) return;
      if (immediateTarget.state)
        void navigate(immediateTarget.pathname, {
          state: immediateTarget.state,
        });
      else void navigate(immediateTarget.pathname);
    },
    [navigate, onPrepareNavigation, onProjectSelect, resolveImmediateProjectTarget, t]
  );

  const handleEditorSubmit = async (input: SynonBiomedProjectInput) => {
    if (!editor || mutationLoading) return;
    setMutationLoading(true);
    setMutationError(null);
    try {
      if (editor.mode === 'create') {
        const created = await createSynonBiomedProject(input);
        setProjects((current) => [...current, created]);
        onProjectSelect?.(created.projectId);
        emitter.emit('synonbiomed.projects.refresh');
        setEditor(null);
        const target = resolveProjectNavigationTarget(created.projectId, []);
        void navigate(target.pathname, { state: target.state });
      } else {
        const updated = await updateSynonBiomedProject(editor.project.projectId, input);
        setProjects((current) =>
          current.map((project) => (project.projectId === updated.projectId ? updated : project))
        );
        emitter.emit('synonbiomed.projects.refresh');
        setEditor(null);
        const projectPath = `/projects/${encodeURIComponent(updated.projectId)}`;
        if (pathname === projectPath) {
          void navigate(projectPath, {
            replace: true,
            state: { synonBiomedProjectRevision: Date.now() },
          });
        }
      }
    } catch (error) {
      console.warn('[ProjectHistorySection] Failed to save project:', diagnostic(error));
      setMutationError(t('conversation.history.projectMutation.saveFailed'));
    } finally {
      setMutationLoading(false);
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget || mutationLoading) return;
    setMutationLoading(true);
    setMutationError(null);
    try {
      await deleteSynonBiomedProject(deleteTarget.projectId);
      setProjects((current) => current.filter((project) => project.projectId !== deleteTarget.projectId));
      emitter.emit('synonbiomed.projects.refresh');
      // A conversation route can be owned by the project being removed. Once
      // the backend has deleted that project and its frames, keeping the
      // conversation route leaves the user on an empty page after the next
      // history refresh. Route both project pages and the active conversation
      // back to the landing page while the deleted object is still known.
      if (
        activeProjectId === deleteTarget.projectId ||
        pathname.startsWith(`/projects/${encodeURIComponent(deleteTarget.projectId)}`)
      ) {
        void navigate('/guid', { replace: true });
      }
      setDeleteTarget(null);
    } catch (error) {
      console.warn('[ProjectHistorySection] Failed to delete project:', diagnostic(error));
      setMutationError(t('conversation.history.projectMutation.deleteFailed'));
    } finally {
      setMutationLoading(false);
    }
  };

  const toggleSelectedProject = useCallback((projectId: string) => {
    setSelectedProjectIds((current) => {
      const next = new Set(current);
      if (next.has(projectId)) next.delete(projectId);
      else next.add(projectId);
      return next;
    });
  }, []);

  const allProjectsSelected = projects.length > 0 && selectedProjectIds.size === projects.length;
  const handleToggleSelectAllProjects = useCallback(() => {
    setSelectedProjectIds((current) =>
      current.size === projects.length ? new Set() : new Set(projects.map((project) => project.projectId))
    );
  }, [projects]);

  const handleBatchDeleteProjects = async () => {
    if (selectedProjectIds.size === 0 || mutationLoading) return;
    const selectedIds = [...selectedProjectIds];
    setMutationLoading(true);
    setMutationError(null);
    try {
      const results = await Promise.allSettled(selectedIds.map((projectId) => deleteSynonBiomedProject(projectId)));
      const deletedIds = new Set(selectedIds.filter((_projectId, index) => results[index]?.status === 'fulfilled'));
      const failedIds = selectedIds.filter((_projectId, index) => results[index]?.status === 'rejected');

      if (deletedIds.size > 0) {
        setProjects((current) => current.filter((project) => !deletedIds.has(project.projectId)));
        emitter.emit('synonbiomed.projects.refresh');
        if (
          [...deletedIds].some(
            (projectId) =>
              activeProjectId === projectId || pathname.startsWith(`/projects/${encodeURIComponent(projectId)}`)
          )
        ) {
          void navigate('/guid', { replace: true });
        }
      }

      setSelectedProjectIds(new Set(failedIds));
      if (failedIds.length > 0) {
        setMutationError(t('conversation.history.projectBatchDeletePartialFailure'));
        return;
      }

      setBatchDeleteVisible(false);
      onBatchModeChange?.(false);
      Message.success(
        t('conversation.history.projectBatchDeleteSuccess', {
          count: deletedIds.size,
        })
      );
    } finally {
      setMutationLoading(false);
    }
  };

  const handleDownloadProject = async (project: SynonBiomedProject) => {
    if (downloadProjectId) return;
    setMenuProjectId(null);
    setDownloadProjectId(project.projectId);
    try {
      const artifacts = await loadSynonBiomedProjectArtifacts(project.projectId);
      const ids = artifacts
        .map((artifact) => artifact.artifactId)
        .filter(Boolean)
        .slice(0, 200);
      if (ids.length === 0) {
        Message.info(t('conversation.history.noProjectArtifacts'));
        return;
      }
      const query = new URLSearchParams({
        ids: ids.join(','),
        include_metadata: 'true',
      });
      await downloadFileFromUrl(
        `/api/artifacts/download?${query.toString()}`,
        `${safeDownloadName(project.name)}-artifacts.zip`
      );
    } catch (error) {
      console.warn('[ProjectHistorySection] Failed to download project artifacts:', diagnostic(error));
      Message.error(t('conversation.history.downloadArtifactsFailed'));
    } finally {
      setDownloadProjectId(null);
    }
  };

  return (
    <section
      ref={sectionRef}
      data-testid='sider-project-section'
      className='synon-sidebar-projects min-w-0'
      aria-label={sectionLabel}
      aria-busy={loading || orderSaving}
    >
      {!collapsed && (
        <SiderSectionHeader
          testId='sider-project-header'
          label={sectionLabel}
          expanded={projectsExpanded}
          toggleLabel={t(
            projectsExpanded
              ? 'conversation.history.projectOrder.collapseList'
              : 'conversation.history.projectOrder.expandList'
          )}
          onToggle={() => {
            setProjectsExpanded((current) => !current);
            setMenuProjectId(null);
          }}
          trailing={
            <Tooltip content={createProjectLabel} position='right'>
              <button
                type='button'
                data-testid='project-create-action'
                aria-label={createProjectLabel}
                className='size-24px rd-6px flex items-center justify-center border border-solid border-transparent bg-transparent cursor-pointer transition-colors text-t-secondary hover:text-t-primary hover:bg-fill-3'
                onClick={(event) => {
                  event.stopPropagation();
                  setMutationError(null);
                  setEditor({ mode: 'create', project: null });
                }}
              >
                <Plus theme='outline' size='14' className='block leading-none' style={{ lineHeight: 0 }} />
              </button>
            </Tooltip>
          }
        />
      )}

      {batchMode && !collapsed && (
        <React.Suspense fallback={null}>
          <BatchSelectionPanel
            scope='projects'
            selectedCount={selectedProjectIds.size}
            allSelected={allProjectsSelected}
            disabled={loading || failed || projects.length === 0 || mutationLoading}
            onToggleSelectAll={handleToggleSelectAllProjects}
            onDelete={() => setBatchDeleteVisible(true)}
          />
        </React.Suspense>
      )}

      {projectsExpanded && loading && (
        <div className='flex justify-center py-12px'>
          <Spin size={14} />
        </div>
      )}
      {projectsExpanded && !loading && failed && !collapsed && (
        <div className='px-12px py-6px text-12px text-t-tertiary'>{t('conversation.history.projectsLoadFailed')}</div>
      )}
      {projectsExpanded && orderError && !collapsed && (
        <div
          data-testid='project-order-error'
          role='alert'
          className='px-12px py-6px text-12px leading-18px text-t-primary'
        >
          {t(`conversation.history.projectOrder.${orderError}`)}
        </div>
      )}
      <div className='sr-only' aria-live='polite' aria-atomic='true'>
        {orderAnnouncement}
      </div>
      {projectsExpanded && !loading && !failed && projects.length === 0 && !collapsed && (
        <div className='px-12px py-8px'>
          <Empty description={t('conversation.history.noProjects')} />
        </div>
      )}
      {projectsExpanded &&
        !loading &&
        !failed &&
        projects.map((project, projectIndex) => (
          <ProjectRow
            key={project.projectId}
            project={project}
            collapsed={collapsed}
            selected={activeProjectId === project.projectId}
            menuOpen={menuProjectId === project.projectId}
            downloading={downloadProjectId === project.projectId}
            batchMode={batchMode}
            checked={selectedProjectIds.has(project.projectId)}
            reorderEnabled={!collapsed && !batchMode && persistedOrder !== null}
            dragging={draggedProjectId === project.projectId}
            dropPosition={
              dropTargetId === project.projectId && draggedProjectId
                ? projects.findIndex((item) => item.projectId === draggedProjectId) < projectIndex
                  ? 'after'
                  : 'before'
                : null
            }
            menuLabel={projectMenuLabel(project.name)}
            editLabel={t('conversation.history.projectMenu.edit')}
            deleteLabel={t('conversation.history.projectMenu.delete')}
            downloadLabel={t('conversation.history.projectMenu.downloadArtifacts')}
            copyIdLabel={t('conversation.history.projectMenu.copyId')}
            onOpen={() => void openProject(project.projectId)}
            onToggleMenu={() =>
              setMenuProjectId((current) => (current === project.projectId ? null : project.projectId))
            }
            onEdit={() => {
              setMenuProjectId(null);
              setMutationError(null);
              setEditor({ mode: 'edit', project });
            }}
            onDownload={() => void handleDownloadProject(project)}
            onCopyId={() => {
              void copyText(project.projectId)
                .then(() => Message.success(t('conversation.history.projectMenu.idCopied')))
                .catch(() => Message.error(t('conversation.history.projectMenu.idCopyFailed')));
            }}
            onDelete={() => {
              setMenuProjectId(null);
              setMutationError(null);
              setDeleteTarget(project);
            }}
            onToggleChecked={() => toggleSelectedProject(project.projectId)}
            onDragStart={() => {
              setMenuProjectId(null);
              setDraggedProjectId(project.projectId);
              setDropTargetId(project.projectId);
            }}
            onDragOver={() => setDropTargetId(project.projectId)}
            onDrop={() => handleProjectDrop(project.projectId)}
            onDragEnd={() => {
              setDraggedProjectId(null);
              setDropTargetId(null);
            }}
            onKeyboardMove={(offset) => handleProjectKeyboardMove(project, offset)}
          />
        ))}

      {editor && (
        <React.Suspense fallback={null}>
          <ProjectEditorModal
            visible
            project={editor.mode === 'edit' ? editor.project : null}
            loading={mutationLoading}
            errorMessage={mutationError}
            onCancel={() => {
              if (!mutationLoading) setEditor(null);
            }}
            onSubmit={(input) => void handleEditorSubmit(input)}
          />
        </React.Suspense>
      )}

      <Modal
        visible={deleteTarget !== null}
        title={t('conversation.history.projectMenu.deleteTitle')}
        footer={null}
        onCancel={() => {
          if (!mutationLoading) setDeleteTarget(null);
        }}
        unmountOnExit
        alignCenter
        getPopupContainer={() => document.body}
        style={{ width: 440, maxWidth: 'calc(100vw - 32px)', borderRadius: 8 }}
      >
        <p className='mt-0 text-14px leading-22px text-t-secondary'>
          {t('conversation.history.projectMenu.deleteConfirm', {
            name: deleteTarget?.name,
          })}
        </p>
        {mutationError && <div className='mb-12px text-12px text-t-primary'>{mutationError}</div>}
        <div className='flex justify-end gap-8px'>
          <Button type='secondary' disabled={mutationLoading} onClick={() => setDeleteTarget(null)}>
            {t('common.cancel')}
          </Button>
          <Button status='danger' loading={mutationLoading} onClick={() => void handleDelete()}>
            {t('conversation.history.projectMenu.confirmDelete')}
          </Button>
        </div>
      </Modal>

      <Modal
        visible={batchDeleteVisible}
        title={t('conversation.history.projectBatchDeleteTitle')}
        footer={null}
        onCancel={() => {
          if (!mutationLoading) setBatchDeleteVisible(false);
        }}
        unmountOnExit
        alignCenter
        getPopupContainer={() => document.body}
        style={{ width: 440, maxWidth: 'calc(100vw - 32px)', borderRadius: 8 }}
      >
        <p className='mt-0 text-14px leading-22px text-t-secondary'>
          {t('conversation.history.projectBatchDeleteConfirm', {
            count: selectedProjectIds.size,
          })}
        </p>
        {mutationError && <div className='mb-12px text-12px text-t-primary'>{mutationError}</div>}
        <div className='flex justify-end gap-8px'>
          <Button type='secondary' disabled={mutationLoading} onClick={() => setBatchDeleteVisible(false)}>
            {t('common.cancel')}
          </Button>
          <Button
            data-testid='project-batch-delete-confirm'
            status='danger'
            loading={mutationLoading}
            onClick={() => void handleBatchDeleteProjects()}
          >
            {t('conversation.history.projectMenu.confirmDelete')}
          </Button>
        </div>
      </Modal>
    </section>
  );
};

type ProjectRowProps = {
  project: SynonBiomedProject;
  collapsed: boolean;
  selected: boolean;
  menuOpen: boolean;
  downloading: boolean;
  batchMode: boolean;
  checked: boolean;
  reorderEnabled: boolean;
  dragging: boolean;
  dropPosition: 'before' | 'after' | null;
  menuLabel: string;
  editLabel: string;
  deleteLabel: string;
  downloadLabel: string;
  copyIdLabel: string;
  onOpen: () => void;
  onToggleMenu: () => void;
  onEdit: () => void;
  onDownload: () => void;
  onCopyId: () => void;
  onDelete: () => void;
  onToggleChecked: () => void;
  onDragStart: () => void;
  onDragOver: () => void;
  onDrop: () => void;
  onDragEnd: () => void;
  onKeyboardMove: (offset: -1 | 1) => void;
};

const ProjectRow: React.FC<ProjectRowProps> = ({
  project,
  collapsed,
  selected,
  menuOpen,
  downloading,
  batchMode,
  checked,
  reorderEnabled,
  dragging,
  dropPosition,
  menuLabel,
  editLabel,
  deleteLabel,
  downloadLabel,
  copyIdLabel,
  onOpen,
  onToggleMenu,
  onEdit,
  onDownload,
  onCopyId,
  onDelete,
  onToggleChecked,
  onDragStart,
  onDragOver,
  onDrop,
  onDragEnd,
  onKeyboardMove,
}) => {
  const { t } = useTranslation();
  const menuButtonRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const description = t('conversation.history.projectSummary', {
    tasks: project.conversationCount,
    files: project.artifactCount,
  });
  const menuId = `project-menu-${project.projectId}`;

  const restoreMenuButtonFocus = useCallback(() => {
    requestAnimationFrame(() => menuButtonRef.current?.focus());
  }, []);

  const closeMenuAndRestoreFocus = useCallback(() => {
    onToggleMenu();
    restoreMenuButtonFocus();
  }, [onToggleMenu, restoreMenuButtonFocus]);

  const positionMenu = useCallback(() => {
    const trigger = menuButtonRef.current;
    const menu = menuRef.current;
    if (!trigger || !menu) return;

    const margin = 8;
    const rect = trigger.getBoundingClientRect();
    const width = Math.min(224, Math.max(0, window.innerWidth - margin * 2));
    const height = menu.offsetHeight;
    const left = Math.max(margin, Math.min(rect.right - width, window.innerWidth - width - margin));
    const top =
      rect.bottom + height + margin <= window.innerHeight ? rect.bottom + 6 : Math.max(margin, rect.top - height - 6);

    Object.assign(menu.style, {
      left: `${left}px`,
      top: `${top}px`,
      width: `${width}px`,
      visibility: 'visible',
    });
  }, []);

  useLayoutEffect(() => {
    if (!menuOpen) return;

    positionMenu();
    menuRef.current?.querySelector<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')?.focus();
    window.addEventListener('resize', positionMenu);
    window.addEventListener('scroll', positionMenu, true);
    return () => {
      window.removeEventListener('resize', positionMenu);
      window.removeEventListener('scroll', positionMenu, true);
    };
  }, [menuOpen, positionMenu]);

  const handleMenuKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const items = Array.from(
      event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')
    );
    const currentIndex = items.indexOf(document.activeElement as HTMLButtonElement);

    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      closeMenuAndRestoreFocus();
      return;
    }
    if (!items.length || !['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;

    event.preventDefault();
    const nextIndex =
      event.key === 'Home'
        ? 0
        : event.key === 'End'
          ? items.length - 1
          : (currentIndex + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length;
    items[nextIndex]?.focus();
  };
  const content = (
    <div
      data-testid={`project-row-${project.projectId}`}
      className={classNames(
        'synon-sidebar-project-row relative group/project min-w-0 border-solid border-transparent',
        dragging && 'opacity-55',
        dropPosition === 'before' && 'border-t-2px !border-t-[rgb(var(--primary-6))]',
        dropPosition === 'after' && 'border-b-2px !border-b-[rgb(var(--primary-6))]'
      )}
      style={{ position: 'relative' }}
      onDragOver={(event) => {
        if (!reorderEnabled) return;
        event.preventDefault();
        onDragOver();
      }}
      onDrop={(event) => {
        event.preventDefault();
        onDrop();
      }}
    >
      {reorderEnabled && (
        <button
          type='button'
          draggable
          data-testid={`project-drag-handle-${project.projectId}`}
          aria-label={t('conversation.history.projectOrder.reorderNamed', {
            name: project.name,
          })}
          className='synon-sidebar-project-drag absolute z-10 left-1px top-1/2 -translate-y-1/2 size-18px flex items-center justify-center border-none bg-transparent rd-4px text-t-tertiary cursor-grab active:cursor-grabbing hover:bg-fill-3 hover:text-t-primary focus:bg-fill-3 focus:text-t-primary'
          onClick={(event) => event.stopPropagation()}
          onDragStart={(event) => {
            event.dataTransfer.effectAllowed = 'move';
            event.dataTransfer.setData('text/plain', project.projectId);
            onDragStart();
          }}
          onDragEnd={onDragEnd}
          onKeyDown={(event) => {
            if (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return;
            event.preventDefault();
            event.stopPropagation();
            onKeyboardMove(event.key === 'ArrowUp' ? -1 : 1);
          }}
        >
          <Drag theme='outline' size={14} />
        </button>
      )}
      <button
        type='button'
        className={classNames(
          'synon-sidebar-project-button w-full min-w-0 flex items-center gap-8px border-none bg-transparent rd-8px text-left cursor-pointer transition-colors text-t-primary hover:bg-fill-2',
          selected && 'bg-fill-3',
          batchMode && checked && 'bg-[rgba(var(--primary-6),0.08)]',
          collapsed
            ? 'justify-center h-36px px-6px'
            : reorderEnabled
              ? 'pl-22px pr-34px py-7px'
              : 'pl-12px pr-34px py-7px'
        )}
        aria-label={project.name}
        onClick={batchMode ? onToggleChecked : onOpen}
      >
        {batchMode && !collapsed && (
          <span
            data-testid={`project-batch-checkbox-${project.projectId}`}
            className='flex items-center justify-center shrink-0'
            onClick={(event) => {
              event.stopPropagation();
              onToggleChecked();
            }}
          >
            <Checkbox checked={checked} />
          </span>
        )}
        {!collapsed && (
          <span className='min-w-0 flex-1'>
            <span className='synon-sidebar-project-name block truncate text-13px font-[500]'>{project.name}</span>
            <span className='synon-sidebar-project-meta block truncate mt-2px text-11px text-t-tertiary'>
              {description}
            </span>
          </span>
        )}
      </button>
      {!collapsed && !batchMode && (
        <>
          <button
            ref={menuButtonRef}
            type='button'
            aria-label={menuLabel}
            aria-haspopup='menu'
            aria-expanded={menuOpen}
            aria-controls={menuOpen ? menuId : undefined}
            className={classNames(
              'absolute right-8px top-1/2 -translate-y-1/2 size-24px flex items-center justify-center border-none bg-transparent rd-5px text-t-tertiary cursor-pointer hover:bg-fill-3 hover:text-t-primary',
              menuOpen ? 'opacity-100' : 'opacity-0 group-hover/project:opacity-100 focus:opacity-100'
            )}
            onClick={(event) => {
              event.stopPropagation();
              onToggleMenu();
            }}
            onKeyDown={(event) => {
              if (event.key === 'ArrowDown' && !menuOpen) {
                event.preventDefault();
                onToggleMenu();
              }
              if (event.key === 'Escape' && menuOpen) {
                event.preventDefault();
                closeMenuAndRestoreFocus();
              }
            }}
          >
            <MoreOne theme='outline' size={14} />
          </button>
          {menuOpen &&
            createPortal(
              <div
                ref={menuRef}
                id={menuId}
                role='menu'
                aria-label={menuLabel}
                data-synon-sidebar-project-menu
                className='app-overlay-menu fixed z-[1000] box-border p-5px'
                style={{
                  maxWidth: 'calc(100vw - 16px)',
                  visibility: 'hidden',
                  backgroundColor: '#fff',
                  opacity: 1,
                }}
                onKeyDown={handleMenuKeyDown}
              >
                <button
                  type='button'
                  role='menuitem'
                  className='w-full h-32px px-8px flex items-center gap-8px border-none bg-transparent rd-5px text-13px text-t-primary cursor-pointer hover:bg-fill-3'
                  onClick={() => {
                    onToggleMenu();
                    onCopyId();
                  }}
                >
                  <Copy theme='outline' size={14} />
                  {copyIdLabel}
                </button>
                <button
                  type='button'
                  role='menuitem'
                  className='w-full h-32px px-8px flex items-center gap-8px border-none bg-transparent rd-5px text-13px text-t-primary cursor-pointer hover:bg-fill-3'
                  onClick={onEdit}
                >
                  <EditOne theme='outline' size={14} />
                  {editLabel}
                </button>
                <button
                  type='button'
                  role='menuitem'
                  disabled={downloading}
                  className='w-full h-32px px-8px flex items-center gap-8px border-none bg-transparent rd-5px text-13px text-t-primary cursor-pointer hover:bg-fill-3 disabled:opacity-50'
                  onClick={() => {
                    onToggleMenu();
                    onDownload();
                  }}
                >
                  <Download theme='outline' size={14} />
                  {downloadLabel}
                </button>
                <button
                  type='button'
                  role='menuitem'
                  className='w-full h-32px px-8px flex items-center gap-8px border-none bg-transparent rd-5px text-13px text-t-primary cursor-pointer hover:bg-fill-3'
                  onClick={onDelete}
                >
                  <DeleteOne theme='outline' size={14} />
                  {deleteLabel}
                </button>
              </div>,
              document.body
            )}
        </>
      )}
    </div>
  );

  return collapsed ? (
    <Tooltip content={project.name} position='right'>
      {content}
    </Tooltip>
  ) : (
    content
  );
};

const projectIds = (projects: readonly SynonBiomedProject[]): string[] => projects.map((project) => project.projectId);

const orderProjects = (projects: readonly SynonBiomedProject[], order: readonly string[]): SynonBiomedProject[] => {
  const byId = new Map(projects.map((project) => [project.projectId, project]));
  return order.flatMap((projectId) => {
    const project = byId.get(projectId);
    return project ? [project] : [];
  });
};

const safeDownloadName = (value: string): string =>
  value
    .trim()
    .replace(/[\\/:*?"<>|]+/g, '-')
    .replace(/\s+/g, ' ') || 'project';

const diagnostic = (error: unknown): string =>
  redactErrorText(error instanceof Error ? error.message : String(error || 'unknown error'));

export default ProjectHistorySection;
