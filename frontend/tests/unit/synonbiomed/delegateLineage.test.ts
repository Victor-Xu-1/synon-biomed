import { describe, expect, it, vi } from 'vitest';
import {
  loadSynonBiomedDelegateLineage,
  orderSynonBiomedDelegates,
  summarizeSynonBiomedDelegates,
  type SynonBiomedDelegateLineage,
} from '@/renderer/services/synonBiomedDelegateLineage';

const lineage: SynonBiomedDelegateLineage = {
  rootFrameId: 'root',
  currentFrameId: 'child-running',
  current: {
    frameId: 'child-running',
    rootFrameId: 'root',
    parentFrameId: 'root',
    ordinal: 2,
    label: 'structure-search',
    agentName: 'STRUCTURE',
    status: 'running',
    statusDescription: 'Searching structures',
    taskSummary: null,
    messageCount: 4,
    directChildCount: 1,
  },
  ancestors: [],
  directChildren: [],
  rootChildren: [
    {
      frameId: 'child-done',
      rootFrameId: 'root',
      parentFrameId: 'root',
      ordinal: 1,
      label: 'literature-review',
      agentName: 'RESEARCHER',
      status: 'completed',
      statusDescription: null,
      taskSummary: null,
      messageCount: 5,
      directChildCount: 0,
    },
    {
      frameId: 'child-running',
      rootFrameId: 'root',
      parentFrameId: 'root',
      ordinal: 2,
      label: 'structure-search',
      agentName: 'STRUCTURE',
      status: 'running',
      statusDescription: 'Searching structures',
      taskSummary: null,
      messageCount: 4,
      directChildCount: 1,
    },
    {
      frameId: 'child-question',
      rootFrameId: 'root',
      parentFrameId: 'root',
      ordinal: 4,
      label: 'assay-review',
      agentName: 'RESEARCHER',
      status: 'needs-input',
      statusDescription: 'Choose an assay',
      taskSummary: null,
      messageCount: 2,
      directChildCount: 0,
    },
  ],
  totals: { needsInput: 1, running: 1, failed: 0, completed: 1, stopped: 0 },
};

describe('Synon Biomed delegate lineage model', () => {
  it('loads the typed hierarchy from the native conversation route', async () => {
    const fetchImpl = vi.fn(async () => new Response(JSON.stringify(lineage), { status: 200 }));

    await expect(loadSynonBiomedDelegateLineage('child-running', { fetchImpl })).resolves.toEqual(lineage);
    expect(fetchImpl).toHaveBeenCalledWith('/api/conversations/child-running/lineage', {
      headers: { accept: 'application/json' },
      signal: undefined,
    });
  });

  it('orders needs-input and running children ahead of completed work while preserving ordinals', () => {
    expect(orderSynonBiomedDelegates(lineage.rootChildren).map((child) => child.frameId)).toEqual([
      'child-question',
      'child-running',
      'child-done',
    ]);
  });

  it('summarizes the currently viewed frame children independently from root totals', () => {
    expect(summarizeSynonBiomedDelegates([lineage.rootChildren[1], lineage.rootChildren[2]])).toEqual({
      needsInput: 1,
      running: 1,
      failed: 0,
      completed: 0,
      stopped: 0,
    });
  });

  it('rejects malformed hierarchy payloads instead of leaking backend records into the UI', async () => {
    const fetchImpl = vi.fn(async () => new Response(JSON.stringify({ rootFrameId: 'root' }), { status: 200 }));

    await expect(loadSynonBiomedDelegateLineage('root', { fetchImpl })).rejects.toThrow(
      'Synon Biomed delegate lineage payload is invalid'
    );
  });
});
