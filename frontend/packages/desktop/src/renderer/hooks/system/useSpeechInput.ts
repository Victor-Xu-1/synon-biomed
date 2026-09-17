/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useLatestRef } from '@/renderer/hooks/ui/useLatestRef';
import { isElectronDesktop } from '@/renderer/utils/platform';

export type SpeechInputAvailability = 'record' | 'file' | 'unsupported';
export type SpeechInputStatus = 'idle' | 'starting' | 'recording' | 'transcribing' | 'error';
export type SpeechInputErrorCode =
  | 'aborted'
  | 'audio-capture'
  | 'empty-transcript'
  | 'file-too-large'
  | 'network'
  | 'not-configured'
  | 'permission-denied'
  | 'recording-unsupported'
  | 'transcription-failed'
  | 'unknown';

type SpeechInputEnvironment = {
  hasFileInput: boolean;
  hasMediaDevices: boolean;
  hasMediaRecorder: boolean;
  hostname: string;
  isElectronDesktop: boolean;
  isSecureContext: boolean;
};

type UseSpeechInputOptions = {
  /**
   * Live transcript of the current STREAMING session (finals + trailing
   * partial). Called with `null` exactly once per streaming session to clear
   * the live display, always before the terminal `onTranscript` or error.
   * Never called on the non-streaming MediaRecorder path.
   */
  onLiveTranscript?: (sessionText: string | null) => void;
  onTranscript: (transcript: string) => void;
};

const LOCAL_HOSTNAMES = new Set(['localhost', '127.0.0.1', '::1']);
const RECORDING_MIME_TYPES = ['audio/webm;codecs=opus', 'audio/webm', 'audio/mp4', 'audio/ogg;codecs=opus'];
const SPEECH_WAVEFORM_SAMPLE_COUNT = 40;
const SPEECH_WAVEFORM_MIN_LEVEL = 0.015;
const SPEECH_WAVEFORM_MAX_LEVEL = 1;
const SPEECH_VISUALIZER_INTERVAL_MS = 80;

const createInitialWaveformLevels = (): number[] =>
  Array.from({ length: SPEECH_WAVEFORM_SAMPLE_COUNT }, (_, index) => ((index + 1) % 6 === 0 ? 0.04 : 0.015));

const clampWaveformLevel = (value: number): number =>
  Math.max(SPEECH_WAVEFORM_MIN_LEVEL, Math.min(SPEECH_WAVEFORM_MAX_LEVEL, value));

const createNextWaveformLevels = (previous: number[], nextLevel: number): number[] => [
  ...previous.slice(1),
  clampWaveformLevel(nextLevel),
];

export const appendSpeechTranscript = (base: string, transcript: string): string => {
  const normalizedTranscript = transcript.trim();
  if (!normalizedTranscript) {
    return base;
  }

  const normalizedBase = base.trimEnd();
  if (!normalizedBase) {
    return normalizedTranscript;
  }

  return `${normalizedBase}\n${normalizedTranscript}`;
};

/** CJK Unified Ideographs + extension A + CJK/fullwidth punctuation. */
const CJK_BOUNDARY_CHAR = /[　-〿㐀-䶿一-鿿＀-￯]/;

/** Join transcript segments: CJK-adjacent boundaries concatenate directly, otherwise a single space. */
export const joinTranscriptSegments = (segments: string[]): string => {
  return segments
    .map((segment) => segment.trim())
    .filter(Boolean)
    .reduce((joined, segment) => {
      if (!joined) {
        return segment;
      }
      const isCjkBoundary = CJK_BOUNDARY_CHAR.test(joined[joined.length - 1]) || CJK_BOUNDARY_CHAR.test(segment[0]);
      return `${joined}${isCjkBoundary ? '' : ' '}${segment}`;
    }, '');
};

/**
 * Compose the live display text for a streaming session: committed finals
 * plus the in-flight partial, joined as one continuous utterance
 * (VAD pause boundaries must not introduce line breaks).
 */
export const composeLiveTranscript = (finals: string[], partial: string): string => {
  return joinTranscriptSegments(partial ? [...finals, partial] : finals);
};

export const getSpeechInputErrorMessageKey = (errorCode: SpeechInputErrorCode): string => {
  switch (errorCode) {
    case 'audio-capture':
      return 'conversation.chat.speech.audioCaptureError';
    case 'empty-transcript':
      return 'conversation.chat.speech.emptyTranscript';
    case 'file-too-large':
      return 'conversation.chat.speech.fileTooLarge';
    case 'network':
      return 'conversation.chat.speech.networkError';
    case 'not-configured':
      return 'conversation.chat.speech.notConfigured';
    case 'permission-denied':
      return 'conversation.chat.speech.permissionDenied';
    case 'recording-unsupported':
      return 'conversation.chat.speech.recordingUnsupported';
    case 'transcription-failed':
      return 'conversation.chat.speech.transcriptionFailed';
    case 'aborted':
    case 'unknown':
    default:
      return 'conversation.chat.speech.genericError';
  }
};

const getSpeechInputEnvironment = (): SpeechInputEnvironment => {
  if (typeof window === 'undefined' || typeof document === 'undefined') {
    return {
      hasFileInput: false,
      hasMediaDevices: false,
      hasMediaRecorder: false,
      hostname: '',
      isElectronDesktop: false,
      isSecureContext: false,
    };
  }

  return {
    hasFileInput: typeof document.createElement === 'function',
    hasMediaDevices: typeof navigator !== 'undefined' && Boolean(navigator.mediaDevices?.getUserMedia),
    hasMediaRecorder: typeof MediaRecorder !== 'undefined',
    hostname: window.location.hostname,
    isElectronDesktop: isElectronDesktop(),
    isSecureContext: window.isSecureContext,
  };
};

export const getSpeechInputAvailabilityForEnvironment = (
  environment: SpeechInputEnvironment
): SpeechInputAvailability => {
  const canUseLiveRecording =
    environment.hasMediaDevices &&
    environment.hasMediaRecorder &&
    (environment.isElectronDesktop || environment.isSecureContext || LOCAL_HOSTNAMES.has(environment.hostname));

  if (canUseLiveRecording) {
    return 'record';
  }

  if (environment.hasFileInput) {
    return 'file';
  }

  return 'unsupported';
};

export const getSpeechInputAvailability = (): SpeechInputAvailability => {
  return getSpeechInputAvailabilityForEnvironment(getSpeechInputEnvironment());
};

export const pickRecordingMimeType = (): string => {
  if (typeof MediaRecorder === 'undefined' || typeof MediaRecorder.isTypeSupported !== 'function') {
    return '';
  }

  return RECORDING_MIME_TYPES.find((mimeType) => MediaRecorder.isTypeSupported(mimeType)) || '';
};

const mapSpeechInputError = (error: unknown): SpeechInputErrorCode => {
  if (error instanceof DOMException) {
    switch (error.name) {
      case 'NotAllowedError':
      case 'SecurityError':
        return 'permission-denied';
      case 'NotFoundError':
      case 'DevicesNotFoundError':
        return 'audio-capture';
      case 'AbortError':
        return 'aborted';
      default:
        return 'unknown';
    }
  }

  const message = error instanceof Error ? error.message : String(error);

  if (
    message.includes('STT_OPENAI_NOT_CONFIGURED') ||
    message.includes('STT_DEEPGRAM_NOT_CONFIGURED') ||
    message.includes('STT_DISABLED')
  ) {
    return 'not-configured';
  }
  if (message.includes('STT_FILE_TOO_LARGE')) {
    return 'file-too-large';
  }
  if (message.includes('STT_NETWORK_ERROR')) {
    return 'network';
  }
  if (message.includes('STT_ABORTED')) {
    return 'aborted';
  }
  if (message.includes('STT_REQUEST_FAILED') || message.includes('STT_LOCAL_')) {
    return 'transcription-failed';
  }

  return 'unknown';
};

export const useSpeechInput = ({ onTranscript }: UseSpeechInputOptions) => {
  const [status, setStatus] = useState<SpeechInputStatus>('idle');
  const [errorCode, setErrorCode] = useState<SpeechInputErrorCode | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [recordingDurationMs, setRecordingDurationMs] = useState(0);
  const [recordingLevels, setRecordingLevels] = useState<number[]>(() => createInitialWaveformLevels());
  const recorderRef = useRef<MediaRecorder | null>(null);
  const recorderEventCleanupRef = useRef<(() => void) | null>(null);
  const streamRef = useRef<MediaStream | null>(null);
  const chunksRef = useRef<Blob[]>([]);
  const recordingStartedAtRef = useRef<number | null>(null);
  const visualizerIntervalRef = useRef<number | null>(null);
  const audioContextRef = useRef<AudioContext | null>(null);
  const analyserRef = useRef<AnalyserNode | null>(null);
  const mediaSourceRef = useRef<MediaStreamAudioSourceNode | null>(null);
  const analyserDataRef = useRef<Uint8Array<ArrayBuffer> | null>(null);
  const onTranscriptRef = useLatestRef(onTranscript);
  const transcriptionAbortControllerRef = useRef<AbortController | null>(null);
  const transcriptionAudioBlobRef = useRef<Blob | null>(null);
  const startInFlightRef = useRef(false);
  const startAbortControllerRef = useRef<AbortController | null>(null);
  const availability = useMemo(() => getSpeechInputAvailability(), []);

  const pauseSpeechVisualizer = useCallback(() => {
    if (visualizerIntervalRef.current !== null) {
      window.clearInterval(visualizerIntervalRef.current);
      visualizerIntervalRef.current = null;
    }
  }, []);

  const resetSpeechVisualizer = useCallback(() => {
    pauseSpeechVisualizer();
    recordingStartedAtRef.current = null;
    setRecordingDurationMs(0);
    setRecordingLevels(createInitialWaveformLevels());
  }, [pauseSpeechVisualizer]);

  const cleanupAudioAnalysis = useCallback(async () => {
    if (mediaSourceRef.current) {
      try {
        mediaSourceRef.current.disconnect();
      } catch {
        // Ignore disconnect failures during teardown.
      }
      mediaSourceRef.current = null;
    }

    if (analyserRef.current) {
      try {
        analyserRef.current.disconnect();
      } catch {
        // Ignore disconnect failures during teardown.
      }
      analyserRef.current = null;
    }

    analyserDataRef.current = null;

    if (audioContextRef.current) {
      try {
        await audioContextRef.current.close();
      } catch {
        // Ignore close failures during teardown.
      }
      audioContextRef.current = null;
    }
  }, []);

  const startSpeechVisualizer = useCallback(
    async (stream: MediaStream) => {
      resetSpeechVisualizer();
      recordingStartedAtRef.current = Date.now();

      const AudioContextCtor =
        typeof AudioContext !== 'undefined'
          ? AudioContext
          : typeof window !== 'undefined'
            ? (window as Window & { webkitAudioContext?: typeof AudioContext }).webkitAudioContext
            : undefined;

      if (AudioContextCtor) {
        try {
          const audioContext = new AudioContextCtor();
          const analyser = audioContext.createAnalyser();
          analyser.fftSize = 128;
          analyser.smoothingTimeConstant = 0.82;
          const source = audioContext.createMediaStreamSource(stream);
          source.connect(analyser);
          audioContextRef.current = audioContext;
          analyserRef.current = analyser;
          mediaSourceRef.current = source;
          analyserDataRef.current = new Uint8Array(analyser.fftSize);
        } catch {
          void cleanupAudioAnalysis();
        }
      }

      visualizerIntervalRef.current = window.setInterval(() => {
        const startedAt = recordingStartedAtRef.current;
        if (startedAt) {
          setRecordingDurationMs(Date.now() - startedAt);
        }

        const analyser = analyserRef.current;
        const analyserData = analyserDataRef.current;
        if (!analyser || !analyserData) {
          setRecordingLevels((previous) => createNextWaveformLevels(previous, SPEECH_WAVEFORM_MIN_LEVEL));
          return;
        }

        analyser.getByteTimeDomainData(analyserData);
        let sum = 0;
        for (const sample of analyserData) {
          const normalized = (sample - 128) / 128;
          sum += normalized * normalized;
        }

        const rms = Math.sqrt(sum / analyserData.length);
        const scaledLevel = clampWaveformLevel(rms * 5.6);
        setRecordingLevels((previous) => createNextWaveformLevels(previous, scaledLevel));
      }, SPEECH_VISUALIZER_INTERVAL_MS);
    },
    [cleanupAudioAnalysis, resetSpeechVisualizer]
  );

  const cleanupRecorder = useCallback(() => {
    pauseSpeechVisualizer();
    recorderEventCleanupRef.current?.();
    recorderEventCleanupRef.current = null;
    if (streamRef.current) {
      streamRef.current.getTracks().forEach((track) => track.stop());
      streamRef.current = null;
    }
    recorderRef.current = null;
    chunksRef.current = [];
    void cleanupAudioAnalysis();
  }, [cleanupAudioAnalysis, pauseSpeechVisualizer]);

  const clearError = useCallback(() => {
    setErrorCode(null);
    setErrorMessage(null);
    setStatus('idle');
    resetSpeechVisualizer();
  }, [resetSpeechVisualizer]);

  const transcribeBlob = useCallback(
    async (inputBlob: Blob) => {
      const controller = new AbortController();
      transcriptionAbortControllerRef.current?.abort();
      transcriptionAbortControllerRef.current = controller;
      transcriptionAudioBlobRef.current = inputBlob;
      let audioBlob: Blob | null = inputBlob;

      try {
        setStatus('transcribing');
        setErrorCode(null);
        setErrorMessage(null);
        // No languageHint: the configured STT language (or provider-native
        // auto detection) is the only language signal.
        const { transcribeAudioBlob } = await import('@/renderer/services/SpeechToTextService');
        if (!audioBlob) {
          throw new Error('STT_AUDIO_UNAVAILABLE');
        }
        const result = await transcribeAudioBlob(audioBlob, undefined, controller.signal);
        if (controller.signal.aborted || transcriptionAbortControllerRef.current !== controller) {
          return;
        }
        const transcript = result.text.trim();
        if (!transcript) {
          setErrorCode('empty-transcript');
          setErrorMessage(null);
          setStatus('error');
          resetSpeechVisualizer();
          return;
        }
        onTranscriptRef.current(transcript);
        setStatus('idle');
        resetSpeechVisualizer();
      } catch (error) {
        const mappedError = mapSpeechInputError(error);
        if (controller.signal.aborted || mappedError === 'aborted') {
          setErrorCode(null);
          setErrorMessage(null);
          setStatus('idle');
          resetSpeechVisualizer();
          return;
        }
        setErrorCode(mappedError);
        const message = error instanceof Error ? error.message : String(error);
        const detail = message.match(/^STT_[A-Z0-9_]+:\s*(.+)$/)?.[1]?.trim();
        setErrorMessage(detail || null);
        setStatus('error');
        resetSpeechVisualizer();
      } finally {
        if (transcriptionAudioBlobRef.current === inputBlob) {
          transcriptionAudioBlobRef.current = null;
        }
        // Blob has no delete API; dropping the last renderer reference is the
        // browser-side equivalent of deleting this transient recording.
        audioBlob = null;
        if (transcriptionAbortControllerRef.current === controller) {
          transcriptionAbortControllerRef.current = null;
        }
      }
    },
    [onTranscriptRef, resetSpeechVisualizer]
  );

  const startRecording = useCallback(async () => {
    if (startInFlightRef.current || status === 'starting' || status === 'recording' || status === 'transcribing') {
      return;
    }
    if (availability !== 'record') {
      setErrorCode('recording-unsupported');
      setStatus('error');
      return;
    }
    startInFlightRef.current = true;
    setStatus('starting');
    const startController = new AbortController();
    startAbortControllerRef.current = startController;

    // The canonical backend currently exposes the bounded multipart `/api/stt`
    // contract, not `/api/voice/stream`. Use MediaRecorder as the authoritative
    // capture path so a microphone click cannot deterministically fail with a
    // WebSocket error before transcription begins.
    try {
      const { prepareLocalSpeech } = await import('@/renderer/services/SpeechToTextService');
      await prepareLocalSpeech(startController.signal);
      if (!startInFlightRef.current) {
        return;
      }
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      if (!startInFlightRef.current) {
        stream.getTracks().forEach((track) => track.stop());
        return;
      }
      const mimeType = pickRecordingMimeType();
      const recorder = mimeType ? new MediaRecorder(stream, { mimeType }) : new MediaRecorder(stream);

      streamRef.current = stream;
      recorderRef.current = recorder;
      chunksRef.current = [];
      await startSpeechVisualizer(stream);
      if (!startInFlightRef.current) {
        cleanupRecorder();
        return;
      }

      const handleDataAvailable = (event: BlobEvent) => {
        if (event.data.size > 0) {
          chunksRef.current.push(event.data);
        }
      };

      const handleRecorderError = () => {
        cleanupRecorder();
        setErrorCode('unknown');
        setStatus('error');
      };

      const handleRecorderStop = () => {
        const audioBlob = new Blob(chunksRef.current, {
          type: recorder.mimeType || mimeType || 'audio/webm',
        });
        cleanupRecorder();
        void transcribeBlob(audioBlob);
      };

      recorder.addEventListener('dataavailable', handleDataAvailable);
      recorder.addEventListener('error', handleRecorderError);
      recorder.addEventListener('stop', handleRecorderStop);
      recorderEventCleanupRef.current = () => {
        recorder.removeEventListener('dataavailable', handleDataAvailable);
        recorder.removeEventListener('error', handleRecorderError);
        recorder.removeEventListener('stop', handleRecorderStop);
      };

      setErrorCode(null);
      setErrorMessage(null);
      setStatus('recording');
      recorder.start();
    } catch (error) {
      cleanupRecorder();
      if (!startInFlightRef.current) {
        return;
      }
      setErrorCode(mapSpeechInputError(error));
      setErrorMessage(null);
      setStatus('error');
      resetSpeechVisualizer();
    } finally {
      if (startAbortControllerRef.current === startController) {
        startAbortControllerRef.current = null;
      }
      startInFlightRef.current = false;
    }
  }, [availability, cleanupRecorder, resetSpeechVisualizer, startSpeechVisualizer, status, transcribeBlob]);

  const stopRecording = useCallback(() => {
    if (status !== 'recording') {
      return;
    }

    const recorder = recorderRef.current;
    if (!recorder) {
      return;
    }

    setStatus('transcribing');
    pauseSpeechVisualizer();
    try {
      recorder.stop();
    } catch (error) {
      // If the recorder rejects stop(), discard any buffered chunks instead of
      // leaving the just-recorded audio reachable from the hook.
      cleanupRecorder();
      setErrorCode(mapSpeechInputError(error));
      setErrorMessage(null);
      setStatus('error');
      resetSpeechVisualizer();
    }
  }, [cleanupRecorder, pauseSpeechVisualizer, resetSpeechVisualizer, status]);

  const cancelTranscription = useCallback(() => {
    if (status === 'starting') {
      startInFlightRef.current = false;
      startAbortControllerRef.current?.abort();
      setStatus('idle');
      resetSpeechVisualizer();
      return;
    }
    if (status !== 'transcribing') {
      return;
    }
    transcriptionAbortControllerRef.current?.abort();
    transcriptionAbortControllerRef.current = null;
    transcriptionAudioBlobRef.current = null;
    setErrorCode(null);
    setErrorMessage(null);
    setStatus('idle');
    resetSpeechVisualizer();
  }, [resetSpeechVisualizer, status]);

  const transcribeFile = useCallback(
    async (file: Blob) => {
      await transcribeBlob(file);
    },
    [transcribeBlob]
  );

  useEffect(() => {
    return () => {
      startInFlightRef.current = false;
      startAbortControllerRef.current?.abort();
      transcriptionAbortControllerRef.current?.abort();
      transcriptionAbortControllerRef.current = null;
      transcriptionAudioBlobRef.current = null;
      const recorder = recorderRef.current;
      recorderEventCleanupRef.current?.();
      recorderEventCleanupRef.current = null;
      if (recorder?.state !== 'inactive') {
        try {
          recorder.stop();
        } catch {
          // Ignore teardown failures from partially started recording sessions.
        }
      }
      cleanupRecorder();
    };
  }, [cleanupRecorder]);

  return {
    availability,
    clearError,
    errorCode,
    errorMessage,
    recordingDurationMs,
    recordingLevels,
    startRecording,
    cancelTranscription,
    status,
    stopRecording,
    transcribeFile,
  };
};
