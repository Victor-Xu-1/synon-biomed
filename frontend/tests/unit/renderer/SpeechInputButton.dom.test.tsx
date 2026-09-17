import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import SpeechInputButton from '@/renderer/components/chat/SpeechInputButton';

const mocks = vi.hoisted(() => ({
  startRecording: vi.fn(),
  info: vi.fn(),
  warning: vi.fn(),
  error: vi.fn(),
}));

vi.mock('@/renderer/hooks/system/useSpeechInput', () => ({
  getSpeechInputErrorMessageKey: () => 'conversation.chat.speech.genericError',
  useSpeechInput: () => ({
    availability: 'record',
    clearError: vi.fn(),
    errorCode: null,
    errorMessage: null,
    recordingDurationMs: 0,
    recordingLevels: [],
    startRecording: mocks.startRecording,
    status: 'idle',
    stopRecording: vi.fn(),
    cancelTranscription: vi.fn(),
    transcribeFile: vi.fn(),
  }),
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) =>
      ({
        'conversation.chat.speech.recordTooltip': '开始语音输入',
        'conversation.chat.speech.notConfigured': '语音转文字尚未配置',
      })[key] ?? key,
  }),
}));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: { ...actual.Message, info: mocks.info, warning: mocks.warning, error: mocks.error },
  };
});

describe('SpeechInputButton', () => {
  beforeEach(() => {
    mocks.startRecording.mockResolvedValue(undefined);
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it('starts the native Synon Biomed voice flow without a legacy STT setting', async () => {
    render(<SpeechInputButton onTranscript={vi.fn()} />);

    const button = await screen.findByRole('button', { name: '开始语音输入' });
    fireEvent.click(button);
    await waitFor(() => expect(mocks.startRecording).toHaveBeenCalledTimes(1));
  });

  it('starts the existing real speech capture flow when configured', async () => {
    render(<SpeechInputButton onTranscript={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: '开始语音输入' }));
    await waitFor(() => expect(mocks.startRecording).toHaveBeenCalledTimes(1));
  });
});
