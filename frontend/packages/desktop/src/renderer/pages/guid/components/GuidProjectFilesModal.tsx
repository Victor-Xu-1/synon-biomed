import { Button, Modal, Spin } from '@arco-design/web-react';
import { Check, FolderOpen } from '@icon-park/react';
import React, { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { SynonBiomedProject, SynonBiomedProjectArtifact } from '@/renderer/services/synonBiomedGateway';
import { loadSynonBiomedProjectArtifacts, loadSynonBiomedProjects } from '@/renderer/services/synonBiomedGateway';
import {
  readPreferredProjectId,
  resolveActiveProjectId,
} from '@/renderer/pages/conversation/GroupedHistory/projectSelectionModel';
import { formatProjectArtifactSize, getReferenceableProjectArtifacts } from '../utils/guidProjectFilesModel';

type GuidProjectFilesModalProps = {
  visible: boolean;
  projectId?: string | null;
  projectName?: string;
  onCancel: () => void;
  onSelect: (artifacts: SynonBiomedProjectArtifact[]) => void;
};

type ResolvedProject = Pick<SynonBiomedProject, 'projectId' | 'name'>;

const GuidProjectFilesModal: React.FC<GuidProjectFilesModalProps> = ({
  visible,
  projectId,
  projectName,
  onCancel,
  onSelect,
}) => {
  const { t } = useTranslation();
  const [status, setStatus] = useState<'idle' | 'loading' | 'ready' | 'empty' | 'error'>('idle');
  const [project, setProject] = useState<ResolvedProject | null>(null);
  const [artifacts, setArtifacts] = useState<SynonBiomedProjectArtifact[]>([]);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [reloadKey, setReloadKey] = useState(0);

  useEffect(() => {
    if (!visible) return;
    let active = true;
    setStatus('loading');
    setProject(null);
    setArtifacts([]);
    setSelectedIds(new Set());

    const load = async () => {
      try {
        let targetProject: ResolvedProject | null = null;
        if (projectId?.trim()) {
          targetProject = { projectId: projectId.trim(), name: projectName?.trim() || projectId.trim() };
        } else {
          const projects = await loadSynonBiomedProjects();
          const activeProjectId = resolveActiveProjectId({
            preferredProjectId: readPreferredProjectId(),
            availableProjectIds: projects.map((candidate) => candidate.projectId),
          });
          const selectedProject = projects.find((candidate) => candidate.projectId === activeProjectId);
          if (selectedProject) targetProject = selectedProject;
        }

        if (!active) return;
        setProject(targetProject);
        if (!targetProject) {
          setStatus('empty');
          return;
        }

        const loadedArtifacts = await loadSynonBiomedProjectArtifacts(targetProject.projectId);
        if (!active) return;
        const referenceableArtifacts = getReferenceableProjectArtifacts(loadedArtifacts, targetProject.projectId);
        setArtifacts(referenceableArtifacts);
        setStatus(referenceableArtifacts.length > 0 ? 'ready' : 'empty');
      } catch (error) {
        console.error('[GuidProjectFilesModal] Failed to load project files:', error);
        if (active) setStatus('error');
      }
    };

    void load();
    return () => {
      active = false;
    };
  }, [projectId, projectName, reloadKey, visible]);

  const toggleArtifact = useCallback((artifactId: string) => {
    setSelectedIds((current) => {
      const next = new Set(current);
      if (next.has(artifactId)) next.delete(artifactId);
      else next.add(artifactId);
      return next;
    });
  }, []);

  const handleConfirm = useCallback(() => {
    const selected = artifacts
      .filter((artifact) => selectedIds.has(artifact.artifactId))
      .map((artifact) =>
        artifact.projectId || !project?.projectId
          ? artifact
          : Object.assign({}, artifact, { projectId: project.projectId })
      );
    if (selected.length > 0) onSelect(selected);
  }, [artifacts, onSelect, project?.projectId, selectedIds]);

  const emptyMessage = project
    ? t('conversation.attachMenu.projectFilesEmpty')
    : t('conversation.attachMenu.projectFilesNoProject');

  return (
    <Modal
      visible={visible}
      title={t('conversation.attachMenu.projectFilesTitle')}
      onCancel={onCancel}
      onOk={handleConfirm}
      okText={t('conversation.attachMenu.projectFilesSelect')}
      cancelText={t('common.cancel')}
      okButtonProps={{ disabled: status !== 'ready' || selectedIds.size === 0 }}
      unmountOnExit
      style={{ width: 620, maxWidth: 'calc(100vw - 24px)' }}
    >
      <div className='flex flex-col gap-12px' data-testid='guid-project-files-modal'>
        <div className='text-13px text-t-secondary'>
          {project
            ? `${project.name} - ${t('conversation.attachMenu.projectFilesHint')}`
            : t('conversation.attachMenu.projectFilesHint')}
        </div>

        {status === 'loading' ? (
          <div className='min-h-160px flex flex-col items-center justify-center gap-8px text-t-secondary'>
            <Spin />
            <span>{t('conversation.attachMenu.projectFilesLoading')}</span>
          </div>
        ) : status === 'error' ? (
          <div className='min-h-160px flex flex-col items-center justify-center gap-10px text-t-secondary' role='alert'>
            <span>{t('conversation.attachMenu.projectFilesLoadFailed')}</span>
            <Button size='small' onClick={() => setReloadKey((value) => value + 1)}>
              {t('conversation.attachMenu.projectFilesRetry')}
            </Button>
          </div>
        ) : status === 'empty' ? (
          <div
            className='min-h-160px flex items-center justify-center text-13px text-t-secondary'
            data-testid='guid-project-files-empty'
          >
            {emptyMessage}
          </div>
        ) : (
          <div
            className='max-h-[min(52vh,360px)] overflow-y-auto'
            role='group'
            aria-label={t('conversation.attachMenu.projectFilesTitle')}
          >
            {artifacts.map((artifact) => {
              const selected = selectedIds.has(artifact.artifactId);
              return (
                <button
                  key={`${artifact.artifactId}:${artifact.versionId ?? artifact.versionNumber}`}
                  type='button'
                  role='checkbox'
                  aria-checked={selected}
                  data-testid='guid-project-file-option'
                  className={`flex w-full items-center gap-10px border-b border-arco-2 px-8px py-10px text-left transition-colors last:border-b-0 ${selected ? 'bg-fill-2' : 'bg-transparent hover:bg-fill-1'}`}
                  onClick={() => toggleArtifact(artifact.artifactId)}
                >
                  <span className='flex size-28px shrink-0 items-center justify-center rounded-6px bg-fill-2 text-t-secondary'>
                    <FolderOpen theme='outline' size='15' />
                  </span>
                  <span className='min-w-0 flex-1'>
                    <span className='block truncate text-13px text-t-primary'>{artifact.filename}</span>
                    <span className='mt-2px block text-12px text-t-tertiary'>
                      {t(
                        artifact.isUserUpload
                          ? 'conversation.attachMenu.projectFilesUploaded'
                          : 'conversation.attachMenu.projectFilesGenerated'
                      )}{' '}
                      - {formatProjectArtifactSize(artifact.sizeBytes)}
                    </span>
                  </span>
                  <span
                    className='flex size-20px shrink-0 items-center justify-center text-t-secondary'
                    aria-hidden='true'
                  >
                    {selected ? <Check size='16' /> : null}
                  </span>
                </button>
              );
            })}
          </div>
        )}
      </div>
    </Modal>
  );
};

export default GuidProjectFilesModal;
