import { normalizeConversationStreamingSnapshot } from '@/renderer/services/runtime/conversationStreamingModel';
import { describe, expect, it } from 'vitest';

describe('conversation streaming model', () => {
  it('normalizes the root-scoped auxiliary stdout recovery slice', () => {
    expect(
      normalizeConversationStreamingSnapshot({
        root_frame_id: 'frame-root',
        history_revision: 42,
        buffers: [
          {
            frame_id: 'frame-root',
            text: 'Final answer in progress',
            thinking: 'Compare structures',
            tool_stdout: [
              {
                tool_use_id: 'call-python-1',
                stdout: 'Loaded 12 structures\n',
              },
            ],
          },
          {
            frame_id: 'frame-child',
            text: '',
            thinking: 'Searching UniProt',
            tool_stdout: [],
          },
        ],
      })
    ).toEqual({
      rootFrameId: 'frame-root',
      historyRevision: 42,
      frames: [
        {
          frameId: 'frame-root',
          toolStdout: [
            {
              toolUseId: 'call-python-1',
              stdout: 'Loaded 12 structures\n',
              stderr: '',
            },
          ],
        },
        {
          frameId: 'frame-child',
          toolStdout: [],
        },
      ],
    });
  });

  it('rejects malformed batches instead of silently hiding a backend contract change', () => {
    expect(() => normalizeConversationStreamingSnapshot({ root_frame_id: 'frame-root', buffers: {} })).toThrow(
      'streaming response is invalid'
    );
  });

  it('fails closed on ambiguous identities, duplicate records, and mistyped stream fields', () => {
    const batch = (buffers: unknown[], rootFrameId = 'frame-root') => ({
      root_frame_id: rootFrameId,
      buffers,
    });
    const buffer = (overrides: Record<string, unknown> = {}) => ({
      frame_id: 'frame-root',
      text: '',
      thinking: '',
      tool_stdout: [],
      ...overrides,
    });

    for (const value of [
      batch([buffer()], ' frame-root'),
      batch([buffer({ frame_id: 'frame-root\n' })]),
      batch([buffer(), buffer()]),
      batch([buffer({ tool_stdout: [{}] })]),
      { ...batch([buffer()]), history_revision: -1 },
      { ...batch([buffer()]), history_revision: 1.5 },
      { ...batch([buffer()]), history_revision: '1' },
      batch([buffer({ tool_stdout: [{ id: 'guessed-id', stdout: 'unsafe fallback' }] })]),
      batch([
        buffer({
          tool_stdout: [
            { tool_use_id: 'call-1', stdout: 'first' },
            { tool_use_id: 'call-1', stdout: 'second' },
          ],
        }),
      ]),
      batch([
        buffer({
          tool_stdout: [
            { exec_id: 'exec-duplicate', tool_use_id: 'call-1', stdout: 'first' },
            { exec_id: 'exec-duplicate', tool_use_id: 'call-2', stdout: 'second' },
          ],
        }),
      ]),
      batch([
        buffer({
          tool_stdout: [
            {
              exec_id: 'exec-offset',
              tool_use_id: 'call-offset',
              stdout: '生',
              stdout_start_byte: 0,
              stdout_end_byte: 1,
            },
          ],
        }),
      ]),
    ]) {
      expect(() => normalizeConversationStreamingSnapshot(value)).toThrow('streaming response is invalid');
    }

    expect(
      normalizeConversationStreamingSnapshot(
        batch([buffer({ tool_stdout: [{ exec_id: 'exec-1', stdout: 'explicit execution' }] })])
      ).frames[0].toolStdout[0].toolUseId
    ).toBe('exec-1');
  });
});
