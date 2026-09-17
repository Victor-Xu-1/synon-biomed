/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/** Models valid for the file-based /audio/transcriptions endpoint and the streaming /api/stt/stream endpoint. */
export const LOCAL_SPEECH_MODEL = 'sherpa-onnx-streaming-zipformer-small-ctc-zh-int8-2025-04-01';

export const OPENAI_SPEECH_MODEL_PRESETS = ['gpt-4o-transcribe', 'gpt-4o-mini-transcribe', 'whisper-1'];

export const DEEPGRAM_SPEECH_MODEL_PRESETS = ['nova-3', 'nova-2'];
