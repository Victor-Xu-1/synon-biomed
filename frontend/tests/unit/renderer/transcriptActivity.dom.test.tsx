import React from 'react';
import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { createDefaultConversationRuntimeView } from '@/renderer/pages/conversation/runtime/conversationRuntimeViewStore';
import TranscriptActivity from '@/renderer/pages/conversation/Messages/components/TranscriptActivity';
import { projectTranscriptActivity } from '@/renderer/pages/conversation/Messages/transcriptActivityModel';
import type { IMessageToolCall } from '@/common/chat/chatLib';

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }));

const running = {
  ...createDefaultConversationRuntimeView('task'),
  hydrated: true,
  hasBackendRuntime: true,
  hasTask: true,
  isProcessing: true,
  state: 'running' as const,
};
const tool = (status: string): IMessageToolCall =>
  ({
    id: 'step',
    conversation_id: 'task',
    type: 'tool_call',
    content: { call_id: 'step', name: 'read_file', status, args: { path: 'data.csv' } },
  }) as IMessageToolCall;

describe('transcript activity continuity', () => {
  it('bridges the gap after a settled step without changing its status', () => {
    const completed = tool('completed');
    const activity = projectTranscriptActivity(running, 'connected', completed);
    expect(activity?.spinning).toBe(true);
    expect(completed.content.status).toBe('completed');
    const { rerender } = render(<TranscriptActivity activity={activity} />);
    expect(screen.getByRole('status')).toHaveAccessibleName('messages.processing');
    expect(screen.getByRole('status').tagName).toBe('SPAN');
    expect(screen.queryByText('messages.processing')).not.toBeInTheDocument();
    expect(screen.getByTestId('transcript-activity-spinner')).toBeInTheDocument();
    rerender(<TranscriptActivity activity={projectTranscriptActivity(running, 'connected', tool('running'))} />);
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
    rerender(<TranscriptActivity activity={projectTranscriptActivity(running, 'connected', tool('completed'))} />);
    expect(screen.getByTestId('transcript-activity-spinner')).toBeInTheDocument();
  });

  it.each(['waiting_confirmation', 'waiting_approval', 'waiting_input', 'paused', 'cancelling'] as const)(
    'never spins for %s',
    (state) => {
      const activity = projectTranscriptActivity({ ...running, state }, 'connected', tool('completed'));
      expect(activity).not.toBeNull();
      expect(activity?.spinning).toBe(false);
    }
  );

  it.each(['reconnecting', 'offline', 'protocol-error', 'auth-required', 'connecting', 'idle'] as const)(
    'does not animate stale running state while %s',
    (connection) => {
      expect(projectTranscriptActivity(running, connection, tool('completed'))?.spinning).toBe(false);
    }
  );

  it('does not manufacture work from a stale tool or a missing authority', () => {
    expect(
      projectTranscriptActivity(
        { ...running, state: 'idle', isProcessing: false, taskStatus: 'finished' },
        'connected',
        tool('running')
      )
    ).toBeNull();
    expect(
      projectTranscriptActivity({ ...running, hydrationError: 'unavailable' }, 'connected', tool('completed'))?.spinning
    ).toBe(false);
    expect(
      projectTranscriptActivity({ ...running, hasBackendRuntime: false }, 'connected', tool('completed'))?.spinning
    ).toBe(false);
    expect(projectTranscriptActivity(createDefaultConversationRuntimeView('other'), 'connected', undefined)).toBeNull();
  });

  it('keeps the tail active while text is streaming and removes it on completion', () => {
    const text = {
      id: 'text',
      conversation_id: 'task',
      type: 'text' as const,
      position: 'left' as const,
      status: 'work' as const,
      content: { content: 'Partial reply' },
    };
    expect(projectTranscriptActivity(running, 'connected', text)?.spinning).toBe(true);
    expect(projectTranscriptActivity({ ...running, state: 'idle', isProcessing: false }, 'connected', text)).toBeNull();
  });
});
