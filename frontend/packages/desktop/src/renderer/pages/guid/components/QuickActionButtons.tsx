/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { loadSynonBiomedCatalogSummary } from '@/renderer/services/synonBiomedCatalog';
import {
  createSynonBiomedProject,
  loadSynonBiomedProjects,
  type SynonBiomedProjectInput,
} from '@/renderer/services/synonBiomedGateway';
import { emitter } from '@/renderer/utils/emitter';
import { Earth, Lightning, System, Toolkit } from '@icon-park/react';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import styles from '../index.module.css';
import ProjectEditorModal from '../../conversation/GroupedHistory/ProjectEditorModal';
import { prefetchProjectRoute, prefetchSettingsRoute } from '@/renderer/components/layout/routeModules';

type QuickActionButtonsProps = {
  onOpenLink?: (url: string) => void;
  onOpenBugReport?: () => void;
  inactiveBorderColor: string;
  activeShadow: string;
};

type SynonBiomedQuickStatus = 'checking' | 'healthy' | 'offline' | 'error';
type SynonBiomedQuickActionId = 'projects' | 'skills' | 'mcp' | 'runtime';

const SYNON_BIOMED_STATUS_CACHE_TTL_MS = 3000;

let synonBiomedStatusCache: {
  quickStatus: SynonBiomedQuickStatus;
  at: number;
} | null = null;

const QuickActionButtons: React.FC<QuickActionButtonsProps> = ({ inactiveBorderColor, activeShadow }) => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [hoveredQuickAction, setHoveredQuickAction] = useState<SynonBiomedQuickActionId | null>(null);
  const [synonBiomedQuickStatus, setSynonBiomedQuickStatus] = useState<SynonBiomedQuickStatus>('checking');
  const [createProjectOpen, setCreateProjectOpen] = useState(false);
  const [createProjectLoading, setCreateProjectLoading] = useState(false);
  const [createProjectError, setCreateProjectError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;

    const loadStatus = async () => {
      const now = Date.now();
      if (synonBiomedStatusCache && now - synonBiomedStatusCache.at < SYNON_BIOMED_STATUS_CACHE_TTL_MS) {
        setSynonBiomedQuickStatus(synonBiomedStatusCache.quickStatus);
        return;
      }

      try {
        const summary = await loadSynonBiomedCatalogSummary();
        if (!alive) return;
        const quickStatus: SynonBiomedQuickStatus = summary?.backendStatus === 'healthy' ? 'healthy' : 'offline';
        setSynonBiomedQuickStatus(quickStatus);
        synonBiomedStatusCache = { quickStatus, at: Date.now() };
      } catch {
        if (!alive) return;
        setSynonBiomedQuickStatus('error');
        synonBiomedStatusCache = { quickStatus: 'error', at: Date.now() };
      }
    };

    void loadStatus();

    return () => {
      alive = false;
    };
  }, []);

  const quickActionStyle = useCallback(
    (isActive: boolean) => ({
      borderWidth: '1px',
      borderStyle: 'solid',
      borderColor: inactiveBorderColor,
      boxShadow: isActive ? activeShadow : 'none',
    }),
    [activeShadow, inactiveBorderColor]
  );

  const handleNavigate = useCallback(
    (path: string) => {
      void navigate(path);
    },
    [navigate]
  );

  const handleQuickActionIntent = useCallback((actionId: SynonBiomedQuickActionId) => {
    if (actionId === 'projects') {
      void prefetchProjectRoute().catch((): undefined => undefined);
      return;
    }
    void prefetchSettingsRoute().catch((): undefined => undefined);
  }, []);

  const handleOpenProject = useCallback(async () => {
    try {
      const projects = await loadSynonBiomedProjects();
      const projectId = projects[0]?.projectId;
      if (projectId) {
        void navigate(`/projects/${encodeURIComponent(projectId)}`);
        return;
      }
      setCreateProjectError(null);
      setCreateProjectOpen(true);
    } catch (error) {
      console.error('Failed to load projects from the quick action:', error);
      setCreateProjectError(t('conversation.history.projectsLoadFailed'));
      setCreateProjectOpen(true);
    }
  }, [navigate, t]);

  const handleCreateProject = useCallback(
    async (input: SynonBiomedProjectInput) => {
      if (createProjectLoading) return;
      setCreateProjectLoading(true);
      setCreateProjectError(null);
      try {
        const project = await createSynonBiomedProject(input);
        emitter.emit('synonbiomed.projects.refresh');
        setCreateProjectOpen(false);
        void navigate(`/projects/${encodeURIComponent(project.projectId)}`);
      } catch (error) {
        console.error('Failed to create a project from the quick action:', error);
        setCreateProjectError(t('conversation.history.projectCreateFailed'));
      } finally {
        setCreateProjectLoading(false);
      }
    },
    [createProjectLoading, navigate, t]
  );

  const synonBiomedStatusLabel =
    synonBiomedQuickStatus === 'healthy'
      ? t('guid.synonBiomedStatus.healthy')
      : synonBiomedQuickStatus === 'checking'
        ? t('guid.synonBiomedStatus.checking')
        : synonBiomedQuickStatus === 'offline'
          ? t('guid.synonBiomedStatus.offline')
          : t('guid.synonBiomedStatus.unavailable');
  const synonBiomedIconColor =
    synonBiomedQuickStatus === 'healthy'
      ? 'rgb(var(--success-6))'
      : synonBiomedQuickStatus === 'checking'
        ? 'rgb(var(--primary-6))'
        : synonBiomedQuickStatus === 'error'
          ? 'var(--color-text-3)'
          : 'var(--color-text-4)';

  const quickActions = useMemo(
    () => [
      {
        id: 'projects' as const,
        label: t('guid.quickActions.projects'),
        maxWidthClass: 'hover:max-w-150px',
        icon: <Earth theme='outline' size={20} fill='currentColor' />,
        iconColor: 'rgb(var(--primary-6))',
        path: '/guid',
      },
      {
        id: 'skills' as const,
        label: t('guid.quickActions.skills'),
        maxWidthClass: 'hover:max-w-130px',
        icon: <Lightning theme='outline' size={20} fill='currentColor' />,
        iconColor: 'rgb(var(--warning-6))',
        path: '/settings/skills',
      },
      {
        id: 'mcp' as const,
        label: t('guid.quickActions.mcp'),
        maxWidthClass: 'hover:max-w-130px',
        icon: <Toolkit theme='outline' size={20} fill='currentColor' />,
        iconColor: 'rgb(var(--arcoblue-6))',
        path: '/settings/tools',
      },
      {
        id: 'runtime' as const,
        label: `${t('guid.quickActions.runtime')} \u00b7 ${synonBiomedStatusLabel}`,
        maxWidthClass: 'hover:max-w-170px',
        icon: <System theme='outline' size={20} fill='currentColor' />,
        iconColor: synonBiomedIconColor,
        path: '/settings/compute',
      },
    ],
    [synonBiomedIconColor, synonBiomedStatusLabel, t]
  );

  return (
    <>
      <div
        className={`absolute left-50% -translate-x-1/2 flex flex-col justify-center items-center ${styles.guidQuickActions}`}
      >
        <div className='flex justify-center items-center gap-24px'>
          {quickActions.map((action) => (
            <button
              key={action.id}
              type='button'
              aria-label={action.label}
              className={`group inline-flex items-center justify-center h-36px min-w-36px max-w-36px px-0 rd-999px bg-fill-0 cursor-pointer overflow-hidden whitespace-nowrap ${action.maxWidthClass} hover:px-14px hover:justify-start hover:gap-8px transition-[max-width,padding,border-radius,box-shadow] duration-420 ease-in-out border-none`}
              style={quickActionStyle(hoveredQuickAction === action.id)}
              onMouseEnter={() => {
                setHoveredQuickAction(action.id);
                handleQuickActionIntent(action.id);
              }}
              onFocus={() => handleQuickActionIntent(action.id)}
              onMouseLeave={() => setHoveredQuickAction(null)}
              onClick={() => {
                if (action.id === 'projects') {
                  void handleOpenProject();
                  return;
                }
                handleNavigate(action.path);
              }}
            >
              <span
                className='flex-shrink-0 transition-colors duration-300 leading-none'
                style={{ color: action.iconColor }}
              >
                {action.icon}
              </span>
              <span className='opacity-0 max-w-0 overflow-hidden text-14px text-[var(--color-text-2)] group-hover:opacity-100 group-hover:max-w-140px transition-all duration-360 ease-in-out'>
                {action.label}
              </span>
            </button>
          ))}
        </div>
      </div>
      <ProjectEditorModal
        visible={createProjectOpen}
        loading={createProjectLoading}
        errorMessage={createProjectError}
        onCancel={() => {
          if (!createProjectLoading) setCreateProjectOpen(false);
        }}
        onSubmit={(input) => void handleCreateProject(input)}
      />
    </>
  );
};

export default QuickActionButtons;
