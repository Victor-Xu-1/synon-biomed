import { Down, Left, Right } from '@icon-park/react';
import SynonBiomedAvatar from '@/renderer/components/synonBiomed/SynonBiomedAvatar';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import {
  loadSynonBiomedDelegateLineage,
  orderSynonBiomedDelegates,
  summarizeSynonBiomedDelegates,
  type SynonBiomedDelegateFrame,
  type SynonBiomedDelegateLineage,
  type SynonBiomedDelegateStatus,
} from '@/renderer/services/synonBiomedDelegateLineage';
import './SynonBiomedDelegateDock.css';
import SynonBiomedLineageMessagesDrawer from './SynonBiomedLineageMessagesDrawer';
import { subscribeSynonBiomedConversationWake } from '@/renderer/services/synonBiomedConversationWake';

const EVENT_COALESCE_MS = 250;
const LINEAGE_RETRY_DELAYS_MS = [2_000, 5_000, 15_000] as const;

const DelegateStatus: React.FC<{ status: SynonBiomedDelegateStatus }> = ({ status }) => {
  const { t } = useTranslation();
  return (
    <span className={`delegate-dock__status delegate-dock__status--${status}`}>
      <span className='delegate-dock__status-dot' aria-hidden='true' />
      {t(`conversation.delegateDock.status.${status}`)}
    </span>
  );
};

const DelegateRow: React.FC<{
  child: SynonBiomedDelegateFrame;
  onOpen: () => void;
  compact?: boolean;
}> = ({ child, onOpen, compact = false }) => {
  const { t } = useTranslation();
  return (
    <button
      type='button'
      className={compact ? 'delegate-dock__row delegate-dock__row--compact' : 'delegate-dock__row'}
      aria-label={t('conversation.delegateDock.openChild', { index: child.ordinal, label: child.label })}
      onClick={onOpen}
    >
      <SynonBiomedAvatar size={15} className='delegate-dock__agent-icon' />
      <span className='delegate-dock__row-main'>
        <span className='delegate-dock__row-title'>
          {t('conversation.delegateDock.childTitle', { index: child.ordinal, label: child.label })}
        </span>
        {!compact && (child.statusDescription || child.taskSummary) && (
          <span className='delegate-dock__row-description'>{child.statusDescription || child.taskSummary}</span>
        )}
      </span>
      {!compact && child.directChildCount > 0 && (
        <span className='delegate-dock__child-count'>
          {t('conversation.delegateDock.nestedCount', { count: child.directChildCount })}
        </span>
      )}
      {!compact && child.messageCount > 0 && (
        <span className='delegate-dock__message-count'>
          {t('conversation.delegateDock.messageCount', { count: child.messageCount })}
        </span>
      )}
      <DelegateStatus status={child.status} />
      <Right theme='outline' size='12' className='delegate-dock__arrow' />
    </button>
  );
};

export const SynonBiomedDelegateDockView: React.FC<{ lineage: SynonBiomedDelegateLineage }> = ({ lineage }) => {
  const navigate = useNavigate();
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [selectedChild, setSelectedChild] = useState<SynonBiomedDelegateFrame | null>(null);
  const orderedChildren = useMemo(() => orderSynonBiomedDelegates(lineage.directChildren), [lineage.directChildren]);
  const totals = useMemo(() => summarizeSynonBiomedDelegates(orderedChildren), [orderedChildren]);
  const parent = lineage.ancestors.at(-1);
  const path = [...lineage.ancestors, lineage.current];

  useEffect(() => {
    if (orderedChildren.length < 2) setOpen(false);
  }, [orderedChildren.length]);

  return (
    <div className='delegate-dock' data-testid='synonbiomed-delegate-dock'>
      {parent && (
        <div className='delegate-dock__lineage'>
          <button
            type='button'
            className='delegate-dock__back'
            aria-label={t('conversation.delegateDock.backToParent', { label: parent.label })}
            onClick={() => navigate(`/conversation/${parent.frameId}`)}
          >
            <Left theme='outline' size='14' />
          </button>
          <nav className='delegate-dock__breadcrumbs' aria-label={t('conversation.delegateDock.hierarchy')}>
            {path.map((frame, index) => (
              <React.Fragment key={frame.frameId}>
                {index > 0 && <span className='delegate-dock__separator'>/</span>}
                {frame.frameId === lineage.currentFrameId ? (
                  <span className='delegate-dock__breadcrumb delegate-dock__breadcrumb--current'>{frame.label}</span>
                ) : (
                  <button
                    type='button'
                    className='delegate-dock__breadcrumb'
                    onClick={() => navigate(`/conversation/${frame.frameId}`)}
                  >
                    {frame.label}
                  </button>
                )}
              </React.Fragment>
            ))}
          </nav>
        </div>
      )}

      {orderedChildren.length === 1 && (
        <div className='delegate-dock__single'>
          <DelegateRow child={orderedChildren[0]} compact onOpen={() => setSelectedChild(orderedChildren[0])} />
        </div>
      )}

      {orderedChildren.length > 1 && (
        <div className='delegate-dock__summary-wrap'>
          <button
            type='button'
            className='delegate-dock__summary'
            aria-label={t('conversation.delegateDock.openList', { count: orderedChildren.length })}
            aria-expanded={open}
            onClick={() => setOpen((value) => !value)}
          >
            <SynonBiomedAvatar size={15} />
            <span>{t('conversation.delegateDock.subagentCount', { count: orderedChildren.length })}</span>
            {totals.needsInput > 0 && (
              <span className='delegate-dock__summary-alert'>
                {t('conversation.delegateDock.needsInputCount', { count: totals.needsInput })}
              </span>
            )}
            {totals.running > 0 && (
              <span className='delegate-dock__summary-running'>
                {t('conversation.delegateDock.runningCount', { count: totals.running })}
              </span>
            )}
            <Down
              theme='outline'
              size='12'
              className={open ? 'delegate-dock__chevron is-open' : 'delegate-dock__chevron'}
            />
          </button>
          {open && (
            <div className='delegate-dock__popover'>
              <div className='delegate-dock__popover-title'>
                <span>{t('conversation.delegateDock.subagents')}</span>
                <span>{orderedChildren.length}</span>
              </div>
              <div className='delegate-dock__list' role='list' aria-label={t('conversation.delegateDock.subagents')}>
                {orderedChildren.slice(0, 8).map((child) => (
                  <DelegateRow key={child.frameId} child={child} onOpen={() => setSelectedChild(child)} />
                ))}
              </div>
            </div>
          )}
        </div>
      )}
      <SynonBiomedLineageMessagesDrawer
        child={selectedChild}
        onClose={() => setSelectedChild(null)}
        onOpenFull={(frameId) => {
          setSelectedChild(null);
          navigate(`/conversation/${frameId}`);
        }}
      />
    </div>
  );
};

export const SynonBiomedDelegateDock: React.FC<{ conversationId: string; polling: boolean }> = ({
  conversationId,
  polling,
}) => {
  const [lineage, setLineage] = useState<SynonBiomedDelegateLineage | null>(null);

  useEffect(() => {
    setLineage(null);
  }, [conversationId]);

  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let loading = false;
    let pendingWake = false;
    let retryAttempt = 0;
    const scheduleLoad = (delayMs: number) => {
      if (controller.signal.aborted || timer) return;
      timer = setTimeout(() => {
        timer = undefined;
        void load();
      }, delayMs);
    };
    const load = async () => {
      if (loading) {
        pendingWake = true;
        return;
      }
      loading = true;
      try {
        const next = await loadSynonBiomedDelegateLineage(conversationId, { signal: controller.signal });
        setLineage(next);
        retryAttempt = 0;
      } catch {
        if (!controller.signal.aborted) {
          // Delegate lineage is an optional enhancement. A transient or
          // legacy-frame read failure must never cover the completed task or
          // imply that the main conversation is unavailable. Retry a bounded
          // number of times in the background; durable wake events can start
          // another attempt after the backoff budget is exhausted.
          const retryDelay = LINEAGE_RETRY_DELAYS_MS[retryAttempt];
          if (retryDelay !== undefined) {
            retryAttempt += 1;
            scheduleLoad(retryDelay);
          }
        }
      } finally {
        loading = false;
        if (pendingWake && !controller.signal.aborted) {
          pendingWake = false;
          scheduleLoad(0);
        }
      }
    };
    const unsubscribeWake: () => void = polling
      ? subscribeSynonBiomedConversationWake(conversationId, () => scheduleLoad(EVENT_COALESCE_MS), {
          includeRelatedConversations: true,
        })
      : () => {};
    scheduleLoad(0);
    return () => {
      unsubscribeWake();
      controller.abort();
      if (timer) clearTimeout(timer);
    };
  }, [conversationId, polling]);

  const hasVisibleLineage = Boolean(lineage && (lineage.ancestors.length > 0 || lineage.directChildren.length > 0));
  if (!hasVisibleLineage || !lineage) return null;
  return <SynonBiomedDelegateDockView lineage={lineage} />;
};

export default React.memo(SynonBiomedDelegateDock);
