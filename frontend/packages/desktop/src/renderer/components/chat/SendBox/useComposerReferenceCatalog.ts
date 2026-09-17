import { ipcBridge } from '@/common';
import type { FileOrFolderItem } from '@/renderer/utils/file/fileTypes';
import { useEffect, useRef, useState } from 'react';
import type { ComposerReferenceItem } from './composerReferenceModel';
import { loadArtifactComposerReferences, loadSessionComposerReferences } from './composerReferenceService';

type Props = {
  conversationId?: string;
  currentFrameId?: string;
  isOpen: boolean;
  projectId?: string;
  trigger?: '@' | '#';
  workspace?: string;
  workspaceSessionKey?: string | null;
};

export const useComposerReferenceCatalog = ({
  conversationId,
  currentFrameId,
  isOpen,
  projectId,
  trigger,
  workspace,
  workspaceSessionKey,
}: Props) => {
  const [workspaceItems, setWorkspaceItems] = useState<FileOrFolderItem[]>([]);
  const [workspaceLoading, setWorkspaceLoading] = useState(false);
  const [workspaceError, setWorkspaceError] = useState(false);
  const [artifactItems, setArtifactItems] = useState<ComposerReferenceItem[]>([]);
  const [artifactLoading, setArtifactLoading] = useState(false);
  const [artifactError, setArtifactError] = useState(false);
  const [sessionItems, setSessionItems] = useState<ComposerReferenceItem[]>([]);
  const [sessionLoading, setSessionLoading] = useState(false);
  const [sessionError, setSessionError] = useState(false);
  const fetchedWorkspaceKeyRef = useRef<string | null>(null);
  const fetchedArtifactProjectRef = useRef<string | null>(null);
  const fetchedSessionProjectRef = useRef<string | null>(null);

  useEffect(() => {
    if (!isOpen || !workspace || !workspaceSessionKey) {
      fetchedWorkspaceKeyRef.current = null;
      setWorkspaceItems([]);
      setWorkspaceLoading(false);
      setWorkspaceError(false);
      return;
    }
    if (fetchedWorkspaceKeyRef.current === workspaceSessionKey) return;

    let cancelled = false;
    fetchedWorkspaceKeyRef.current = workspaceSessionKey;
    setWorkspaceLoading(true);
    setWorkspaceError(false);
    void ipcBridge.fs.listWorkspaceFiles
      .invoke({ root: workspace })
      .then((result) => {
        if (cancelled) return;
        setWorkspaceItems(
          result.map((item) => ({
            path: item.fullPath,
            name: item.name,
            isFile: true,
            relativePath: item.relativePath || undefined,
          }))
        );
      })
      .catch((error) => {
        if (cancelled) return;
        fetchedWorkspaceKeyRef.current = null;
        console.warn('[SendBox] Failed to load workspace file mentions:', error);
        setWorkspaceItems([]);
        setWorkspaceError(true);
      })
      .finally(() => {
        if (!cancelled) setWorkspaceLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [isOpen, workspace, workspaceSessionKey]);

  useEffect(() => {
    if (!isOpen || trigger !== '@' || !projectId) return;
    if (fetchedArtifactProjectRef.current === projectId) return;

    let cancelled = false;
    fetchedArtifactProjectRef.current = projectId;
    setArtifactItems([]);
    setArtifactLoading(true);
    setArtifactError(false);
    void loadArtifactComposerReferences(projectId)
      .then((items) => {
        if (!cancelled) setArtifactItems(items);
      })
      .catch((error) => {
        if (cancelled) return;
        fetchedArtifactProjectRef.current = null;
        setArtifactError(true);
        console.warn('[SendBox] Failed to load artifact references:', error);
      })
      .finally(() => {
        if (!cancelled) setArtifactLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [isOpen, projectId, trigger]);

  useEffect(() => {
    if (!isOpen || trigger !== '#' || !projectId) return;
    if (fetchedSessionProjectRef.current === projectId) return;

    let cancelled = false;
    fetchedSessionProjectRef.current = projectId;
    setSessionItems([]);
    setSessionLoading(true);
    setSessionError(false);
    void loadSessionComposerReferences(projectId, currentFrameId)
      .then((items) => {
        if (!cancelled) setSessionItems(items);
      })
      .catch((error) => {
        if (cancelled) return;
        fetchedSessionProjectRef.current = null;
        setSessionError(true);
        console.warn('[SendBox] Failed to load session references:', error);
      })
      .finally(() => {
        if (!cancelled) setSessionLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [currentFrameId, isOpen, projectId, trigger]);

  useEffect(() => {
    fetchedArtifactProjectRef.current = null;
    fetchedSessionProjectRef.current = null;
    setArtifactItems([]);
    setSessionItems([]);
    setArtifactError(false);
    setSessionError(false);
  }, [conversationId]);

  return {
    artifactError,
    artifactItems,
    artifactLoading,
    sessionError,
    sessionItems,
    sessionLoading,
    workspaceError,
    workspaceItems,
    workspaceLoading,
  };
};
