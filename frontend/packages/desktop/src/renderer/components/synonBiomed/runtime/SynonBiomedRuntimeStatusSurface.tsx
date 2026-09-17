import type { SynonBiomedAskUserResponse } from '@/renderer/services/synonBiomedRuntimeOperations';
import type { RefObject } from 'react';
import React from 'react';
import AskUserCard from './AskUserCard';
import SynonBiomedApprovalCard from './SynonBiomedApprovalCard';
import type {
  SynonBiomedApprovalScope,
  SynonBiomedPendingInputRequest,
  SynonBiomedRuntimeSnapshot,
} from './runtimeOperationsModel';
import type { SynonBiomedTaskCenterMetrics } from './SynonBiomedTaskCenterPanel';
import SynonBiomedTaskStatus, { type SynonBiomedTaskStatusError } from './SynonBiomedTaskStatus';

type Props = {
  snapshot: SynonBiomedRuntimeSnapshot | null;
  loading: boolean;
  snapshotError: SynonBiomedTaskStatusError;
  runtimeState: string;
  terminalProjectionPending: boolean;
  runtimeAuthorityUnavailable: boolean;
  showLoadingStatus: boolean;
  pausing: boolean;
  resuming: boolean;
  pendingInputCount: number;
  taskCenterMetrics: SynonBiomedTaskCenterMetrics;
  planAvailable: boolean;
  recoveryModelLabel: string | null;
  onResume?: () => void;
  onChooseModel?: () => void;
  onStop?: () => void;
  onRefresh: () => void;
  onOpenReviewFindings: () => void;
  onOpenPlan?: () => void;
  onOpenPendingInput?: () => void;
  pendingRequest?: SynonBiomedPendingInputRequest;
  pendingInputRef: RefObject<HTMLDivElement | null>;
  resolvingRequestId: string | null;
  pendingAriaLabel: string;
  onResolveAskUser: (
    request: SynonBiomedPendingInputRequest,
    response: SynonBiomedAskUserResponse
  ) => void | Promise<void>;
  onResolveInput: (
    request: SynonBiomedPendingInputRequest,
    decision: 'allow' | 'deny',
    scope?: SynonBiomedApprovalScope
  ) => void | Promise<void>;
};

const SynonBiomedRuntimeStatusSurface = ({
  snapshot,
  loading,
  snapshotError,
  runtimeState,
  terminalProjectionPending,
  runtimeAuthorityUnavailable,
  showLoadingStatus,
  pausing,
  resuming,
  pendingInputCount,
  taskCenterMetrics,
  planAvailable,
  recoveryModelLabel,
  onResume,
  onChooseModel,
  onStop,
  onRefresh,
  onOpenReviewFindings,
  onOpenPlan,
  onOpenPendingInput,
  pendingRequest,
  pendingInputRef,
  resolvingRequestId,
  pendingAriaLabel,
  onResolveAskUser,
  onResolveInput,
}: Props) => {
  const question = pendingRequest?.questions[0] ?? null;
  return (
    <>
      <SynonBiomedTaskStatus
        snapshot={snapshot}
        loading={loading}
        snapshotError={snapshotError}
        runtimeState={runtimeState}
        terminalProjectionPending={terminalProjectionPending}
        runtimeAuthorityUnavailable={runtimeAuthorityUnavailable}
        showLoadingStatus={showLoadingStatus}
        pausing={pausing}
        resuming={resuming}
        pendingInputCount={pendingInputCount}
        taskCenterMetrics={taskCenterMetrics}
        planAvailable={planAvailable}
        recoveryModelLabel={recoveryModelLabel}
        onResume={onResume}
        onChooseModel={onChooseModel}
        onStop={onStop}
        onRefresh={onRefresh}
        onOpenReviewFindings={onOpenReviewFindings}
        onOpenPlan={onOpenPlan}
        onOpenPendingInput={onOpenPendingInput}
      />
      {pendingRequest ? (
        <div
          key={`${pendingRequest.requestId}:${pendingRequest.kind}`}
          ref={pendingInputRef}
          className='mb-8px'
          data-testid='synon-biomed-pending-input-anchor'
          tabIndex={-1}
          aria-label={pendingAriaLabel}
        >
          {question ? (
            <AskUserCard
              question={question}
              questions={pendingRequest.questions}
              busy={resolvingRequestId === pendingRequest.requestId}
              disabled={Boolean(resolvingRequestId) && resolvingRequestId !== pendingRequest.requestId}
              onResolve={(response) => onResolveAskUser(pendingRequest, response)}
            />
          ) : (
            <SynonBiomedApprovalCard
              request={pendingRequest}
              queueIndex={1}
              queueTotal={pendingInputCount || 1}
              busy={resolvingRequestId === pendingRequest.requestId}
              onAllow={(scope) => onResolveInput(pendingRequest, 'allow', scope)}
              onDeny={() => onResolveInput(pendingRequest, 'deny')}
            />
          )}
        </div>
      ) : null}
    </>
  );
};

export default SynonBiomedRuntimeStatusSurface;
