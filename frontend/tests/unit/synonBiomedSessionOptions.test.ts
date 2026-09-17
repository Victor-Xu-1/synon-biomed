import { describe, expect, it } from 'vitest';

import {
  loadSynonBiomedSessionOptions,
  toSynonBiomedMessageSessionOptions,
} from '../../packages/desktop/src/renderer/services/synonBiomedSessionOptions';

describe('Synon Biomed session review defaults', () => {
  it('keeps automatic Reviewer and Bookmarker checkpoints opt-in by default', async () => {
    const fetchImpl = async () =>
      new Response(
        JSON.stringify({
          id: 'frame-1',
          agent_name: 'OPERON',
          input_data: {},
          context_data: { _original_input: {} },
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } }
      );

    const options = await loadSynonBiomedSessionOptions('frame-1', fetchImpl);
    expect(options.autoReview).toBe(false);
    expect(toSynonBiomedMessageSessionOptions(options).verifier_mode).toBe('off');
  });

  it('preserves an explicit per-session opt-out', async () => {
    const fetchImpl = async () =>
      new Response(
        JSON.stringify({
          id: 'frame-2',
          agent_name: 'OPERON',
          input_data: { verifier_mode: 'off' },
          context_data: {},
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } }
      );

    const options = await loadSynonBiomedSessionOptions('frame-2', fetchImpl);
    expect(options.autoReview).toBe(false);
  });
});
