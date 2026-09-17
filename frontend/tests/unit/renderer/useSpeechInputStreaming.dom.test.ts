/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 *
 * Recording orchestration tests for useSpeechInput. The canonical v0.1.0
 * backend exposes bounded multipart `/api/stt`, so the hook uses the browser
 * MediaRecorder path and does not call the unavailable WebSocket endpoint.
 */

import { act, renderHook, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => {
  class AudioWorkletUnavailableError extends Error {
    constructor() {
      super('AudioWorklet is not available in this environment');
      this.name = 'AudioWorkletUnavailableError';
    }
  }

  return {
    AudioWorkletUnavailableError,
    createPcmRecorder: vi.fn(),
    moduleLoads: {
      pcmRecorder: 0,
      speechStreamClient: 0,
      speechToTextService: 0,
    },
    startSpeechStream: vi.fn(),
    prepareLocalSpeech: vi.fn(),
    transcribeAudioBlob: vi.fn(),
  };
});

vi.mock('@/renderer/services/SpeechToTextService', () => {
  mocks.moduleLoads.speechToTextService += 1;
  return {
    transcribeAudioBlob: mocks.transcribeAudioBlob,
    prepareLocalSpeech: mocks.prepareLocalSpeech,
  };
});

vi.mock('@/renderer/services/speech/pcmRecorder', () => {
  mocks.moduleLoads.pcmRecorder += 1;
  return {
    AudioWorkletUnavailableError: mocks.AudioWorkletUnavailableError,
    createPcmRecorder: mocks.createPcmRecorder,
    STREAM_SAMPLE_RATE: 16000,
  };
});

vi.mock('@/renderer/services/speech/SpeechStreamClient', () => {
  mocks.moduleLoads.speechStreamClient += 1;
  return {
    startSpeechStream: mocks.startSpeechStream,
  };
});

import { composeLiveTranscript, joinTranscriptSegments, useSpeechInput } from '@/renderer/hooks/system/useSpeechInput';

class FakeMediaRecorder {
  static isTypeSupported = () => true;
  state = 'inactive';
  mimeType: string;
  private listeners = new Map<string, Set<(event: Event) => void>>();

  constructor(_stream: MediaStream, options?: { mimeType?: string }) {
    this.mimeType = options?.mimeType ?? 'audio/webm';
  }

  addEventListener(type: string, listener: (event: Event) => void) {
    const listeners = this.listeners.get(type) ?? new Set<(event: Event) => void>();
    listeners.add(listener);
    this.listeners.set(type, listeners);
  }

  removeEventListener(type: string, listener: (event: Event) => void) {
    this.listeners.get(type)?.delete(listener);
  }

  private dispatch(type: string, event: Event) {
    this.listeners.get(type)?.forEach((listener) => listener(event));
  }

  start() {
    this.state = 'recording';
  }

  stop() {
    this.state = 'inactive';
    queueMicrotask(() => {
      this.dispatch('dataavailable', { data: new Blob(['audio'], { type: this.mimeType }) } as unknown as Event);
      this.dispatch('stop', new Event('stop'));
    });
  }
}

const getUserMedia = vi.fn();

const renderSpeechInput = () => {
  const onTranscript = vi.fn();
  const onLiveTranscript = vi.fn();
  const rendered = renderHook(() => useSpeechInput({ onLiveTranscript, onTranscript }));
  return { ...rendered, onLiveTranscript, onTranscript };
};

beforeEach(() => {
  vi.clearAllMocks();
  Object.defineProperty(navigator, 'mediaDevices', {
    configurable: true,
    value: { getUserMedia },
  });
  vi.stubGlobal('MediaRecorder', FakeMediaRecorder);
  getUserMedia.mockResolvedValue({ getTracks: () => [] });
  mocks.prepareLocalSpeech.mockResolvedValue({ phase: 'not_required', progress: 100, ready: true });
  mocks.transcribeAudioBlob.mockResolvedValue({ model: 'm', provider: 'openai', text: 'fallback text' });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('joinTranscriptSegments', () => {
  it('joins latin segments with a single space', () => {
    expect(joinTranscriptSegments(['hello there', 'how are you'])).toBe('hello there how are you');
  });

  it('joins CJK-adjacent segments directly', () => {
    expect(joinTranscriptSegments(['你好', '世界'])).toBe('你好世界');
  });

  it('joins directly when only one side of the boundary is CJK', () => {
    expect(joinTranscriptSegments(['我在用', 'SynonAI'])).toBe('我在用SynonAI');
    expect(joinTranscriptSegments(['open', '设置页'])).toBe('open设置页');
  });

  it('treats CJK punctuation as a CJK boundary', () => {
    expect(joinTranscriptSegments(['你好。', 'then'])).toBe('你好。then');
  });

  it('trims leading/trailing whitespace of each segment', () => {
    expect(joinTranscriptSegments(['  hello ', ' world  '])).toBe('hello world');
  });

  it('drops empty and whitespace-only segments', () => {
    expect(joinTranscriptSegments(['', 'a', '   ', 'b'])).toBe('a b');
    expect(joinTranscriptSegments([])).toBe('');
  });
});

describe('composeLiveTranscript', () => {
  it('joins finals as one continuous utterance', () => {
    expect(composeLiveTranscript(['a', 'b'], '')).toBe('a b');
    expect(composeLiveTranscript(['你好', '世界'], '')).toBe('你好世界');
  });

  it('returns the partial alone when there are no finals', () => {
    expect(composeLiveTranscript([], 'typing')).toBe('typing');
  });

  it('appends the partial after finals with the same boundary rule', () => {
    expect(composeLiveTranscript(['a', 'b'], 'c')).toBe('a b c');
    expect(composeLiveTranscript(['你好'], '世界')).toBe('你好世界');
  });

  it('returns an empty string when both are empty', () => {
    expect(composeLiveTranscript([], '')).toBe('');
  });
});

describe('useSpeechInput recording orchestration', () => {
  it('does not load speech runtime modules while the hook is only mounted', () => {
    const { result } = renderSpeechInput();

    expect(result.current.status).toBe('idle');
    expect(mocks.moduleLoads).toEqual({
      pcmRecorder: 0,
      speechStreamClient: 0,
      speechToTextService: 0,
    });
  });

  it('records through MediaRecorder and transcribes the captured blob', async () => {
    const { result, onTranscript } = renderSpeechInput();

    await act(async () => {
      await result.current.startRecording();
    });

    expect(result.current.status).toBe('recording');
    expect(mocks.prepareLocalSpeech).toHaveBeenCalledTimes(1);
    expect(getUserMedia).toHaveBeenCalledWith({ audio: true });
    expect(mocks.createPcmRecorder).not.toHaveBeenCalled();
    expect(mocks.startSpeechStream).not.toHaveBeenCalled();

    act(() => {
      result.current.stopRecording();
    });
    expect(result.current.status).toBe('transcribing');

    await waitFor(() => expect(onTranscript).toHaveBeenCalledWith('fallback text'));
    expect(result.current.status).toBe('idle');
    expect(mocks.moduleLoads.speechToTextService).toBe(1);
  });

  it('loads the speech-to-text service on the first file transcription', async () => {
    const { result, onTranscript } = renderSpeechInput();

    await act(async () => {
      await result.current.transcribeFile(new Blob(['audio'], { type: 'audio/webm' }));
    });

    expect(mocks.moduleLoads.speechToTextService).toBe(1);
    expect(mocks.transcribeAudioBlob).toHaveBeenCalledTimes(1);
    expect(onTranscript).toHaveBeenCalledWith('fallback text');
    expect(result.current.status).toBe('idle');
  });

  it('surfaces empty-transcript when the provider returns no text', async () => {
    mocks.transcribeAudioBlob.mockResolvedValue({ model: 'm', provider: 'openai', text: '  ' });
    const { result, onTranscript } = renderSpeechInput();

    await act(async () => {
      await result.current.transcribeFile(new Blob(['audio'], { type: 'audio/webm' }));
    });

    expect(onTranscript).not.toHaveBeenCalled();
    expect(result.current.status).toBe('error');
    expect(result.current.errorCode).toBe('empty-transcript');
  });

  it('maps a missing speech provider configuration to a recoverable error', async () => {
    mocks.transcribeAudioBlob.mockRejectedValue(new Error('STT_OPENAI_NOT_CONFIGURED'));
    const { result } = renderSpeechInput();

    await act(async () => {
      await result.current.transcribeFile(new Blob(['audio'], { type: 'audio/webm' }));
    });

    expect(result.current.status).toBe('error');
    expect(result.current.errorCode).toBe('not-configured');
  });

  it('maps provider/network failures without exposing the dead WebSocket path', async () => {
    mocks.transcribeAudioBlob.mockRejectedValue(new Error('STT_NETWORK_ERROR'));
    const { result } = renderSpeechInput();

    await act(async () => {
      await result.current.transcribeFile(new Blob(['audio'], { type: 'audio/webm' }));
    });

    expect(result.current.status).toBe('error');
    expect(result.current.errorCode).toBe('network');
    expect(mocks.startSpeechStream).not.toHaveBeenCalled();
  });

  it('does not start a second capture while the first permission request is pending', async () => {
    let resolveCapture: ((stream: MediaStream) => void) | undefined;
    getUserMedia.mockImplementation(
      () =>
        new Promise<MediaStream>((resolve) => {
          resolveCapture = resolve;
        })
    );
    const { result } = renderSpeechInput();

    let firstStart: Promise<void>;
    act(() => {
      firstStart = result.current.startRecording();
    });
    await waitFor(() => expect(result.current.status).toBe('starting'));
    await act(async () => {
      await result.current.startRecording();
    });
    expect(getUserMedia).toHaveBeenCalledTimes(1);

    act(() => {
      result.current.cancelTranscription();
    });
    expect(result.current.status).toBe('idle');
    act(() => {
      resolveCapture?.({ getTracks: () => [] } as unknown as MediaStream);
    });
    await act(async () => {
      await firstStart;
    });
  });

  it('cancels an in-flight transcription without showing an error toast state', async () => {
    mocks.transcribeAudioBlob.mockImplementation(
      (_blob: Blob, _language: string | undefined, signal?: AbortSignal) =>
        new Promise((_resolve, reject) => {
          signal?.addEventListener('abort', () => reject(new Error('STT_ABORTED')), { once: true });
        })
    );
    const { result, onTranscript } = renderSpeechInput();

    let pending: Promise<void>;
    act(() => {
      pending = result.current.transcribeFile(new Blob(['audio'], { type: 'audio/webm' }));
    });
    await waitFor(() => expect(result.current.status).toBe('transcribing'));
    await act(async () => {
      result.current.cancelTranscription();
      await pending;
    });

    expect(result.current.status).toBe('idle');
    expect(result.current.errorCode).toBeNull();
    expect(onTranscript).not.toHaveBeenCalled();
  });

  it('surfaces microphone permission errors before recording starts', async () => {
    getUserMedia.mockRejectedValue(new DOMException('denied', 'NotAllowedError'));
    const { result } = renderSpeechInput();

    await act(async () => {
      await result.current.startRecording();
    });

    expect(result.current.status).toBe('error');
    expect(result.current.errorCode).toBe('permission-denied');
  });
});
