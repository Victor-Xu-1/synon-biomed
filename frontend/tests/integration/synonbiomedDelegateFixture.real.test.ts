import { describe, expect, it } from 'vitest';

const fixtureBaseUrl = process.env.SYNON_BIOMED_DELEGATE_FIXTURE_URL ?? '';

type NativeMessage = {
  id: string;
  type: string;
  conversation_id: string;
  content: Record<string, unknown>;
};

type NativeSubagentEvent = {
  kind: 'completion' | 'question' | 'info';
  frameId: string;
  ordinal: number;
  childName: string;
  text?: string;
  bullets?: string[];
  wallSeconds?: number;
};

describe.runIf(Boolean(fixtureBaseUrl))('Synon Biomed delegate child-frame fixture integration', () => {
  it('projects a real delegate turn and opens the linked child conversation', async () => {
    const parentPage = await getJson<{ items: NativeMessage[] }>(
      '/api/conversations/delegate-fixture-parent/messages?limit=50'
    );
    const delegation = parentPage.items.find((message) => message.id === 'toolu_delegate_fixture_001');

    expect(delegation).toMatchObject({
      id: 'toolu_delegate_fixture_001',
      type: 'tool_call',
      conversation_id: 'delegate-fixture-parent',
      content: {
        name: 'Agent',
        status: 'completed',
        subagent: {
          ordinal: 1,
          frameId: 'delegate-fixture-child',
          rootFrameId: 'delegate-fixture-parent',
          parentFrameId: 'delegate-fixture-parent',
          delegateName: 'literature-review',
          status: 'completed',
          messageCount: 4,
          superseded: false,
        },
      },
    });

    const subagent = delegation?.content.subagent as Record<string, unknown> | undefined;
    expect(subagent?.latestAction).toBeTruthy();

    const notificationMessage = parentPage.items.find((message) => message.content.name === 'subagent_events');
    expect(notificationMessage).toMatchObject({
      id: 'delegate-parent-notification-tool-result-001:0:subagent-events',
      type: 'tool_call',
      conversation_id: 'delegate-fixture-parent',
      content: {
        call_id: 'toolu_delegate_fixture_notifications_001:subagent-events',
        name: 'subagent_events',
        status: 'completed',
      },
    });

    const notifications = notificationMessage?.content.subagentEvents as NativeSubagentEvent[] | undefined;
    expect(notifications).toEqual([
      {
        kind: 'completion',
        frameId: 'delegate-fixture-child',
        ordinal: 1,
        childName: 'literature-review',
        bullets: ['Reviewed deterministic fixture evidence.', 'Returned two source-backed findings.'],
        wallSeconds: 4.25,
      },
      {
        kind: 'question',
        frameId: 'delegate-fixture-child',
        ordinal: 1,
        childName: 'literature-review',
        text: 'Should the review include adjacent therapeutic targets?',
      },
      {
        kind: 'info',
        frameId: 'delegate-fixture-child',
        ordinal: 1,
        childName: 'literature-review',
        text: 'The deterministic evidence set contains two retained sources.',
      },
    ]);
    expect(parentPage.items.some((message) => message.id === 'toolu_delegate_fixture_notifications_001')).toBe(false);

    const child = await getJson<{
      id: string;
      source: string;
      extra: { root_frame_id: string; parent_frame_id: string; agent_name: string };
    }>('/api/conversations/delegate-fixture-child');
    expect(child).toMatchObject({
      id: 'delegate-fixture-child',
      source: 'synonbiomed',
      extra: {
        root_frame_id: 'delegate-fixture-parent',
        parent_frame_id: 'delegate-fixture-parent',
      },
    });

    const childPage = await getJson<{ items: NativeMessage[] }>(
      '/api/conversations/delegate-fixture-child/messages?limit=50'
    );
    expect(childPage.items).toHaveLength(3);
    expect(childPage.items.every((message) => message.conversation_id === 'delegate-fixture-child')).toBe(true);
  });

  it('projects parallel children and a nested grandchild through the real trace hierarchy', async () => {
    const parent = await getJson<{
      rootFrameId: string;
      currentFrameId: string;
      directChildren: Array<Record<string, unknown>>;
      totals: Record<string, number>;
    }>('/api/conversations/delegate-fixture-parent/lineage');

    expect(parent).toMatchObject({
      rootFrameId: 'delegate-fixture-parent',
      currentFrameId: 'delegate-fixture-parent',
      directChildren: [
        {
          frameId: 'delegate-fixture-child',
          ordinal: 1,
          label: 'literature-review',
          status: 'completed',
          messageCount: 4,
          directChildCount: 1,
        },
        {
          frameId: 'delegate-fixture-child-parallel',
          ordinal: 2,
          label: 'evidence-check',
          status: 'needs-input',
          messageCount: 2,
          directChildCount: 0,
        },
      ],
      totals: { needsInput: 1, running: 0, failed: 0, completed: 1, stopped: 0 },
    });

    const child = await getJson<{
      ancestors: Array<Record<string, unknown>>;
      directChildren: Array<Record<string, unknown>>;
    }>('/api/conversations/delegate-fixture-child/lineage');
    expect(child).toMatchObject({
      ancestors: [{ frameId: 'delegate-fixture-parent', ordinal: 0 }],
      directChildren: [
        {
          frameId: 'delegate-fixture-grandchild',
          ordinal: 3,
          label: 'citation-audit',
          status: 'failed',
          messageCount: 2,
          directChildCount: 0,
        },
      ],
    });
  });

  async function getJson<T>(requestPath: string): Promise<T> {
    const response = await fetch(`${fixtureBaseUrl.replace(/\/+$/, '')}${requestPath}`, {
      headers: { accept: 'application/json' },
    });
    if (!response.ok) {
      throw new Error(`delegate fixture request failed: ${response.status} ${requestPath}`);
    }
    return (await response.json()) as T;
  }
});
