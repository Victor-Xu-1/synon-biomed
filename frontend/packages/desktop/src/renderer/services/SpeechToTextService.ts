/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { getBaseUrl } from '@/common/adapter/httpBridge';
import type { SpeechToTextResult } from '@/common/types/provider/speech';
import { applyCsrfHeaderToSameOriginXHR } from './csrf';

/** Dispatched on window whenever the speech-to-text config is saved. */
export const SPEECH_TO_TEXT_CONFIG_CHANGED_EVENT = 'synon-ai:speech-to-text-config-changed';

const MAX_AUDIO_FILE_SIZE_MB = 30;
const MAX_AUDIO_FILE_SIZE_BYTES = MAX_AUDIO_FILE_SIZE_MB * 1024 * 1024;

const getAudioExtension = (mimeType: string) => {
  switch (mimeType) {
    case 'audio/mp4':
    case 'audio/x-m4a':
      return 'm4a';
    case 'audio/mpeg':
      return 'mp3';
    case 'audio/ogg':
    case 'audio/ogg;codecs=opus':
      return 'ogg';
    case 'audio/wav':
    case 'audio/wave':
      return 'wav';
    default:
      return 'webm';
  }
};

const createAudioFileName = (mimeType: string) => {
  return `speech-input.${getAudioExtension(mimeType)}`;
};

const ensureAudioSize = (blob: Blob) => {
  if (blob.size > MAX_AUDIO_FILE_SIZE_BYTES) {
    throw new Error('STT_FILE_TOO_LARGE');
  }
};

const parseSuccessResponse = (response: XMLHttpRequest): SpeechToTextResult => {
  const payload = JSON.parse(response.responseText) as {
    data?: SpeechToTextResult;
    msg?: string;
    success: boolean;
  };

  if (!payload.success || !payload.data) {
    throw new Error(payload.msg || 'STT_REQUEST_FAILED');
  }

  return payload.data;
};

const parseDataResponse = <T>(response: XMLHttpRequest): T => {
  const payload = JSON.parse(response.responseText) as { data?: T; success: boolean; msg?: string };
  if (!payload.success || payload.data === undefined) {
    throw new Error(payload.msg || 'STT_REQUEST_FAILED');
  }
  return payload.data;
};
// Surface the backend error code (STT_DISABLED, STT_OPENAI_NOT_CONFIGURED, ...)
// so useSpeechInput can map it to a localized error state.
const parseErrorResponse = (response: XMLHttpRequest): Error => {
  if (response.status === 413) {
    return new Error('STT_FILE_TOO_LARGE');
  }

  try {
    const payload = JSON.parse(response.responseText) as {
      code?: string;
      error?: string;
      msg?: string;
    };
    const code = payload.code;
    const detail = payload.error || payload.msg;
    if (code || detail) {
      return new Error([code, detail].filter(Boolean).join(': '));
    }
  } catch {
    // Non-JSON error body — fall back to the status line below.
  }

  return new Error(`STT_REQUEST_FAILED:${response.status} ${response.statusText}`);
};

export async function transcribeAudioBlob(
  blob: Blob,
  languageHint?: string,
  signal?: AbortSignal
): Promise<SpeechToTextResult> {
  ensureAudioSize(blob);

  // The recorded Blob is request-scoped. It is never written to renderer
  // storage; the FormData reference is cleared as soon as the XHR settles.
  const mimeType = blob.type || 'audio/webm';
  const file_name = createAudioFileName(mimeType);

  return new Promise<SpeechToTextResult>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    const endpoint = `${getBaseUrl()}/api/stt`;
    let formData: FormData | null = null;
    let requestBody: FormData | null = null;
    let transientBlob: Blob | null = blob;

    const cleanup = () => {
      signal?.removeEventListener('abort', abortRequest);
      formData = null;
      requestBody = null;
      transientBlob = null;
    };
    const abortRequest = () => xhr.abort();

    if (signal?.aborted) {
      reject(new Error('STT_ABORTED'));
      return;
    }

    // Backend /api/stt only accepts multipart with these exact field names.
    formData = new FormData();
    if (!transientBlob) {
      reject(new Error('STT_AUDIO_UNAVAILABLE'));
      return;
    }
    formData.append('file', transientBlob, file_name);
    transientBlob = null;
    formData.append('fileName', file_name);
    formData.append('mimeType', mimeType);
    if (languageHint) {
      formData.append('languageHint', languageHint);
    }

    xhr.open('POST', endpoint);
    applyCsrfHeaderToSameOriginXHR(xhr, endpoint);
    signal?.addEventListener('abort', abortRequest, { once: true });
    // No withCredentials: the desktop backend allows origin `*`, which the
    // browser rejects for credentialed requests; WebUI is same-origin anyway.

    xhr.addEventListener('load', () => {
      cleanup();
      if (xhr.status < 200 || xhr.status >= 300) {
        reject(parseErrorResponse(xhr));
        return;
      }

      try {
        resolve(parseSuccessResponse(xhr));
      } catch (error) {
        reject(error instanceof Error ? error : new Error(String(error)));
      }
    });

    xhr.addEventListener('error', () => {
      cleanup();
      reject(new Error('STT_NETWORK_ERROR'));
    });

    xhr.addEventListener('abort', () => {
      cleanup();
      reject(new Error('STT_ABORTED'));
    });

    requestBody = formData;
    formData = null;
    if (!requestBody) {
      reject(new Error('STT_REQUEST_FAILED'));
      return;
    }
    xhr.send(requestBody);
  });
}

export type LocalSpeechPreparationStatus = {
  enabled?: boolean;
  model?: string;
  phase: string;
  progress: number;
  provider?: string;
  ready: boolean;
  runtime?: string;
};

export async function prepareLocalSpeech(signal?: AbortSignal): Promise<LocalSpeechPreparationStatus> {
  return new Promise<LocalSpeechPreparationStatus>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    const endpoint = getBaseUrl() + '/api/stt/local/prepare';
    const cleanup = () => {
      signal?.removeEventListener('abort', abortRequest);
    };
    const abortRequest = () => xhr.abort();

    if (signal?.aborted) {
      reject(new Error('STT_ABORTED'));
      return;
    }

    xhr.open('POST', endpoint);
    applyCsrfHeaderToSameOriginXHR(xhr, endpoint);
    signal?.addEventListener('abort', abortRequest, { once: true });
    xhr.addEventListener('load', () => {
      cleanup();
      if (xhr.status < 200 || xhr.status >= 300) {
        reject(parseErrorResponse(xhr));
        return;
      }
      try {
        resolve(parseDataResponse<LocalSpeechPreparationStatus>(xhr));
      } catch (error) {
        reject(error instanceof Error ? error : new Error(String(error)));
      }
    });
    xhr.addEventListener('error', () => {
      cleanup();
      reject(new Error('STT_NETWORK_ERROR'));
    });
    xhr.addEventListener('abort', () => {
      cleanup();
      reject(new Error('STT_ABORTED'));
    });
    xhr.send();
  });
}
