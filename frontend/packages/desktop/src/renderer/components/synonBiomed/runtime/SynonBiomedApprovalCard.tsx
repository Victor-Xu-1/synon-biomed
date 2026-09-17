import { Drawer } from '@arco-design/web-react';
import { Check, Down } from '@icon-park/react';
import React, { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useLayoutContext } from '@/renderer/hooks/context/LayoutContext';
import type { SynonBiomedApprovalScope, SynonBiomedPendingInputRequest } from './runtimeOperationsModel';
import {
  getSynonBiomedApprovalGrantHint,
  getSynonBiomedApprovalHeadline,
  getSynonBiomedApprovalScopeCopy,
  getSynonBiomedApprovalScopePolicy,
} from './approvalScopeModel';
import SynonBiomedPermissionIcon from './SynonBiomedPermissionIcon';
import './SynonBiomedApprovalCard.css';

type SynonBiomedApprovalCardProps = {
  request: SynonBiomedPendingInputRequest;
  queueIndex: number;
  queueTotal: number;
  busy: boolean;
  onAllow: (scope: SynonBiomedApprovalScope) => void | Promise<void>;
  onDeny: () => void | Promise<void>;
};

const SynonBiomedApprovalCard: React.FC<SynonBiomedApprovalCardProps> = ({
  request,
  queueIndex,
  queueTotal,
  busy,
  onAllow,
  onDeny,
}) => {
  const { t, i18n } = useTranslation();
  const language = i18n?.resolvedLanguage ?? i18n?.language ?? 'zh-CN';
  const scopeCopy = getSynonBiomedApprovalScopeCopy(language);
  const layout = useLayoutContext();
  const policy = useMemo(
    () => getSynonBiomedApprovalScopePolicy(request.kind, request.tool, request.rememberable),
    [request.kind, request.rememberable, request.tool]
  );
  const [scope, setScope] = useState<SynonBiomedApprovalScope>(policy.defaultScope);
  const [scopeMenuOpen, setScopeMenuOpen] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [submitFailed, setSubmitFailed] = useState(false);
  const submittingRef = useRef(false);
  const inactive = busy || submitting;
  const scopePickerRef = useRef<HTMLDivElement>(null);
  const approvalIdentity = `${request.requestId}\u0000${policy.defaultScope}`;
  const previousApprovalIdentityRef = useRef(approvalIdentity);
  const effectiveScope =
    previousApprovalIdentityRef.current === approvalIdentity && policy.scopes.includes(scope)
      ? scope
      : policy.defaultScope;

  useEffect(() => {
    // The card is intentionally reused while a task emits consecutive
    // approval requests. A broader scope selected for one request must never
    // leak into the next request; every new operation starts at the policy's
    // safest explicit scope and still requires its own click.
    // The state initializers already establish the correct values on mount.
    // Avoid scheduling a redundant passive reset for that first render: on a
    // busy event loop it can otherwise race with the user's first scope-menu
    // click and immediately close the menu again.
    if (previousApprovalIdentityRef.current === approvalIdentity) return;
    previousApprovalIdentityRef.current = approvalIdentity;
    setScope(policy.defaultScope);
    setScopeMenuOpen(false);
  }, [approvalIdentity, policy.defaultScope]);

  useEffect(() => {
    if (!scopeMenuOpen || layout?.isMobile) return;
    const closeOutside = (event: MouseEvent) => {
      if (!scopePickerRef.current?.contains(event.target as Node)) setScopeMenuOpen(false);
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setScopeMenuOpen(false);
    };
    document.addEventListener('mousedown', closeOutside);
    window.addEventListener('keydown', closeOnEscape);
    return () => {
      document.removeEventListener('mousedown', closeOutside);
      window.removeEventListener('keydown', closeOnEscape);
    };
  }, [layout?.isMobile, scopeMenuOpen]);

  const chooseScope = (nextScope: SynonBiomedApprovalScope) => {
    if (inactive || !policy.scopes.includes(nextScope)) return;
    setScope(nextScope);
    setScopeMenuOpen(false);
  };

  const decide = async (decision: 'allow' | 'deny') => {
    if (busy || submittingRef.current) return;
    submittingRef.current = true;
    setSubmitFailed(false);
    setSubmitting(true);
    setScopeMenuOpen(false);
    try {
      await (decision === 'allow' ? onAllow(effectiveScope) : onDeny());
    } catch {
      setSubmitFailed(true);
    } finally {
      submittingRef.current = false;
      setSubmitting(false);
    }
  };

  const showMultipleScopes = policy.scopes.length > 1;
  // Every permission request uses the same compact surface. The request kind
  // only changes the details revealed by "更多" and the server-side policy;
  // it must not create a second verbose approval UI for Python, MCP, or ACP
  // operations.
  const compactAllowLabel = t('conversation.synonRuntime.approval.compactAllow');
  const compactSandboxNote = t('conversation.synonRuntime.approval.compactSandboxNote');
  const scopeOptions = policy.scopes.map((candidate) => {
    const copy = scopeCopy[candidate];
    const selected = candidate === effectiveScope;
    return (
      <button
        key={candidate}
        type='button'
        role='menuitemradio'
        aria-checked={selected}
        aria-label={`${copy.label} ${copy.hint}`}
        className='synon-approval-card__scope-option'
        disabled={inactive}
        onClick={() => chooseScope(candidate)}
      >
        <span className='synon-approval-card__scope-check'>
          {selected ? <Check theme='outline' size={14} /> : null}
        </span>
        <span className='synon-approval-card__scope-copy'>
          <span className='synon-approval-card__scope-label'>{copy.label}</span>
          <span className='synon-approval-card__scope-hint'>{copy.hint}</span>
        </span>
      </button>
    );
  });

  return (
    <>
      <section
        className='synon-approval-card mb-8px synon-approval-card--compact'
        aria-label={t('conversation.synonRuntime.approval.awaiting')}
        data-testid='synon-approval-card'
      >
        <div className='synon-approval-card__header'>
          <div className='synon-approval-card__heading'>
            <div className='flex items-center gap-8px synon-approval-card__title'>
              <SynonBiomedPermissionIcon variant='request' size={20} />
              <span>{getSynonBiomedApprovalHeadline(request, language)}</span>
            </div>
          </div>
          {queueTotal > 1 ? (
            <span className='synon-approval-card__queue'>
              {queueIndex} / {queueTotal}
            </span>
          ) : null}
        </div>

        {request.description ? <p className='synon-approval-card__description'>{request.description}</p> : null}
        <details className='synon-approval-card__details'>
          <summary>{t('common.more')}</summary>
          <div className='synon-approval-card__details-content'>
            {request.environment ? (
              <div className='synon-approval-card__meta'>
                <code>{request.environment}</code>
                <span>conda env</span>
              </div>
            ) : null}
            {request.target ? <code className='synon-approval-card__code'>{request.target}</code> : null}
            {request.code ? (
              <div>
                <div className='synon-approval-card__code-label'>{t('conversation.synonRuntime.approval.code')}</div>
                <pre className='synon-approval-card__code'>{request.code}</pre>
              </div>
            ) : null}
            <p className='synon-approval-card__sandbox-note'>{compactSandboxNote}</p>
          </div>
        </details>

        {submitFailed ? (
          <p role='alert'>{t('conversation.synonRuntime.runtimeOperations.approvalSubmitFailed')}</p>
        ) : null}
        <div className='synon-approval-card__footer'>
          <div ref={scopePickerRef} className='synon-approval-card__allow-wrap'>
            <button
              type='button'
              className={`synon-approval-card__allow ${showMultipleScopes ? '' : 'synon-approval-card__allow--single'}`}
              aria-label={t('conversation.synonRuntime.approval.allowNamed', {
                scope: scopeCopy[effectiveScope].label,
              })}
              disabled={inactive}
              onClick={() => void decide('allow')}
            >
              {`${compactAllowLabel} · ${scopeCopy[effectiveScope].label}`}
            </button>
            {showMultipleScopes ? (
              <button
                type='button'
                className='synon-approval-card__scope-toggle'
                aria-label={t('conversation.synonRuntime.approval.changeScope')}
                aria-haspopup='menu'
                aria-expanded={scopeMenuOpen}
                disabled={inactive}
                onClick={() => setScopeMenuOpen((open) => !open)}
              >
                <Down theme='outline' size={13} />
              </button>
            ) : null}
            {!layout?.isMobile && scopeMenuOpen ? (
              <div
                className='app-overlay-menu synon-approval-card__scope-menu'
                role='menu'
                aria-label={t('conversation.synonRuntime.approval.allowScope')}
              >
                {scopeOptions}
              </div>
            ) : null}
          </div>
          <button
            type='button'
            className='synon-approval-card__deny'
            disabled={inactive}
            onClick={() => void decide('deny')}
          >
            {t('conversation.synonRuntime.approval.deny')}
          </button>
        </div>
      </section>

      {layout?.isMobile && showMultipleScopes ? (
        <Drawer
          visible={scopeMenuOpen}
          placement='bottom'
          height='auto'
          title={t('conversation.synonRuntime.approval.allowScope')}
          footer={null}
          unmountOnExit
          wrapClassName='synon-approval-scope-drawer'
          onCancel={() => setScopeMenuOpen(false)}
        >
          <div
            className='app-overlay-menu synon-approval-card__scope-options'
            role='menu'
            aria-label={t('conversation.synonRuntime.approval.allowScope')}
          >
            {scopeOptions}
          </div>
          <div className='synon-approval-card__hint mt-8px'>{getSynonBiomedApprovalGrantHint(request, language)}</div>
        </Drawer>
      ) : null}
    </>
  );
};

export default SynonBiomedApprovalCard;
