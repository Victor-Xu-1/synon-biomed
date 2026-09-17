/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { TChatConversation } from '@/common/config/storage';
import { Message } from '@arco-design/web-react';
import React from 'react';

const ChatWorkspace = React.lazy(() => import('../Workspace'));

const SliderContentLoading = () => <div aria-hidden='true' className='size-full bg-fill-1 animate-pulse' />;

const ChatSlider: React.FC<{
  conversation?: TChatConversation;
}> = ({ conversation }) => {
  const [messageApi, messageContext] = Message.useMessage({ maxCount: 1 });

  let workspaceNode: React.ReactNode = null;
  if (conversation?.type === 'acp' && conversation.extra?.workspace) {
    workspaceNode = (
      <ChatWorkspace
        conversation_id={conversation.id}
        workspace={conversation.extra.workspace}
        rootFrameId={
          (conversation.extra as { root_frame_id?: string } | undefined)?.root_frame_id?.trim() || conversation.id
        }
        projectId={(conversation.extra as { project_id?: string } | undefined)?.project_id}
        projectName={(conversation.extra as { project_name?: string } | undefined)?.project_name}
        sessionTitle={conversation.name}
        isTemporaryWorkspace={
          (conversation.extra as { is_temporary_workspace?: boolean } | undefined)?.is_temporary_workspace
        }
        eventPrefix='acp'
        messageApi={messageApi}
      ></ChatWorkspace>
    );
  }

  if (!workspaceNode) {
    return <div></div>;
  }

  return (
    <>
      {messageContext}
      <React.Suspense fallback={<SliderContentLoading />}>{workspaceNode}</React.Suspense>
    </>
  );
};

export default ChatSlider;
