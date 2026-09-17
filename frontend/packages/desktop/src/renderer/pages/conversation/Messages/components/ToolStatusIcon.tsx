import { CheckSmall, CloseSmall } from '@icon-park/react';
import React from 'react';
import type { NormalizedToolStatus } from '@/common/chat/normalizeToolCall';
import type { ToolExecutionDisposition } from './toolExecutionDisposition';

const ToolStatusIcon: React.FC<{ status: NormalizedToolStatus; disposition?: ToolExecutionDisposition | null }> = ({
  status,
  disposition,
}) => {
  if (disposition === 'not-executed' || disposition === 'preflight-blocked') {
    return <span className='tool-status-icon tool-status-icon--not-executed'>−</span>;
  }
  const icon = (() => {
    switch (status) {
      case 'completed':
        return <CheckSmall theme='outline' size='16' />;
      case 'error':
        return <CloseSmall theme='outline' size='15' />;
      case 'running':
        return <span className='tool-status-icon__pulse tool-status-icon__pulse--filled' />;
      case 'waiting':
        return <span className='tool-status-icon__pulse' />;
      case 'blocked':
        return <span aria-hidden='true'>!</span>;
      case 'interrupted':
        return <span aria-hidden='true'>−</span>;
      case 'unknown':
        return <span aria-hidden='true'>?</span>;
      case 'canceled':
        return <CloseSmall theme='outline' size='15' />;
      case 'pending':
      default:
        return <span className='tool-status-icon__pulse' />;
    }
  })();

  return <span className={`tool-status-icon tool-status-icon--${status}`}>{icon}</span>;
};

export default ToolStatusIcon;
