import { existsSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';

type FrameFixture = {
  frameId: string;
  status?: string;
  name?: string;
  taskSummary?: string;
  statusDescription?: string;
  model?: string;
  outputData?: Record<string, unknown>;
  contextData?: Record<string, unknown>;
};

export type DelegateFixture = {
  userId: string;
  projectId: string;
  parentFrameId: string;
  childFrameId: string;
  secondChildFrameId: string;
  grandchildFrameId: string;
};

export type AskUserFixture = {
  frameId: string;
  toolId: string;
  questions: Array<Record<string, unknown>>;
};

export type TranscriptRebasePreparation = {
  frameId: string;
  cutoverId: string;
  sourceBranchId: string;
  sourceGeneration: number;
  sourceThroughPublicationSequence: number;
  eventCount: number;
  cursorCount: number;
};

export type TranscriptRebaseActivation = {
  frameId: string;
  cutoverId: string;
  activationId: string;
  targetEpoch: number;
  authorityGeneration: number;
  activeBranchId: string;
  eventCount: number;
};

export type TranscriptStreamRun = {
  frameId: string;
  runId: string;
  streamUid: string;
  ownerId: string;
  runnerId: string;
  attempt: number;
  claimToken: string;
  claimedInputRevision: number;
  resumeSource: string;
  resumeCheckpoint: number;
  resumeCheckpointAttempt: number;
  claimedAt: string;
  expiresAt: string;
};

export type StreamingParityCopy = {
  thinking: string;
  search: string;
  compute: string;
  final: string;
};

export type StreamingRecoveryCopy = {
  intro: string;
  first: string;
  recovery: string;
  second: string;
  final: string;
};

type TranscriptFixtureCommand = {
  action:
    | 'begin-transcript-stream'
    | 'append-transcript-deltas'
    | 'append-transcript-assistant'
    | 'begin-streaming-parity'
    | 'complete-streaming-parity'
    | 'begin-streaming-recovery'
    | 'complete-streaming-recovery';
  frameId: string;
  transcript: {
    run?: TranscriptStreamRun;
    batchId: string;
    text?: string;
    chunks?: string[];
    copy?: StreamingParityCopy;
    recovery?: StreamingRecoveryCopy;
    artifactRefs?: Array<{ artifactId: string; versionId: string }>;
  };
};

const fixtureBinary = process.env.SYNON_GO_E2E_FIXTURE_BIN?.trim() ?? '';
const workspaceDatabase = process.env.SYNON_GO_E2E_WORKSPACE_DB?.trim() ?? '';
const fixtureWSLDistro = process.env.SYNON_GO_E2E_FIXTURE_WSL_DISTRO?.trim() ?? '';

export const synonGoFrameFixtureUnavailableReason = (() => {
  if (!fixtureBinary) return 'SYNON_GO_E2E_FIXTURE_BIN is not configured';
  if (!workspaceDatabase) return 'SYNON_GO_E2E_WORKSPACE_DB is not configured';
  if (fixtureWSLDistro && !/^[A-Za-z0-9._-]{1,64}$/u.test(fixtureWSLDistro)) {
    return 'SYNON_GO_E2E_FIXTURE_WSL_DISTRO is invalid';
  }
  if (!fixtureWSLDistro && !existsSync(fixtureBinary)) {
    return `Go frame fixture binary is unavailable: ${fixtureBinary}`;
  }
  if (!fixtureWSLDistro && !existsSync(workspaceDatabase)) {
    return `Go workspace database is unavailable: ${workspaceDatabase}`;
  }
  return '';
})();

export function applySynonGoFrameFixture(input: FrameFixture): void {
  const response = runFixture(input) as { ok?: boolean; frameId?: string };
  if (response.ok !== true || response.frameId !== input.frameId) {
    throw new Error(`Go frame fixture returned an invalid response: ${JSON.stringify(response)}`);
  }
}

export function seedSynonGoDelegateFixture(): DelegateFixture {
  const response = runFixture({ action: 'seed-delegate' }) as {
    ok?: boolean;
    fixture?: DelegateFixture;
  };
  if (response.ok !== true || !response.fixture?.parentFrameId || !response.fixture.childFrameId) {
    throw new Error(`Go delegate fixture returned an invalid response: ${JSON.stringify(response)}`);
  }
  return response.fixture;
}

export function seedSynonGoAskUserFixture(input: AskUserFixture): { alreadyPending: boolean } {
  const response = runFixture({ action: 'seed-ask-user', ...input }) as {
    ok?: boolean;
    frameId?: string;
    toolId?: string;
    alreadyPending?: boolean;
  };
  if (
    response.ok !== true ||
    response.frameId !== input.frameId ||
    response.toolId !== input.toolId ||
    typeof response.alreadyPending !== 'boolean'
  ) {
    throw new Error(`Go ask-user fixture returned an invalid response: ${JSON.stringify(response)}`);
  }
  return { alreadyPending: response.alreadyPending };
}

export function seedSynonGoScrollHistory(frameId: string, historyCount = 36): string[] {
  const response = runFixture({ action: 'seed-scroll-history', frameId, historyCount }) as {
    ok?: boolean;
    frameId?: string;
    messageIds?: unknown;
  };
  if (
    response.ok !== true ||
    response.frameId !== frameId ||
    !Array.isArray(response.messageIds) ||
    response.messageIds.length !== historyCount ||
    response.messageIds.some((id) => typeof id !== 'string' || !id)
  ) {
    throw new Error(`Go scroll-history fixture returned an invalid response: ${JSON.stringify(response)}`);
  }
  return response.messageIds as string[];
}

export function beginSynonGoTranscriptStream(frameId: string, runId = randomUUID()): TranscriptStreamRun {
  const response = runFixture({
    action: 'begin-transcript-stream',
    frameId,
    transcript: { batchId: runId },
  }) as { ok?: boolean; frameId?: string; run?: unknown };
  if (response.ok !== true || response.frameId !== frameId || !isTranscriptStreamRun(response.run, frameId, runId)) {
    throw new Error('Go transcript stream fixture returned an invalid begin response');
  }
  return response.run;
}

export function appendSynonGoTranscriptDeltas(
  run: TranscriptStreamRun,
  chunks: string[],
  batchId = randomUUID()
): void {
  assertTranscriptFixtureResponse(
    runFixture({
      action: 'append-transcript-deltas',
      frameId: run.frameId,
      transcript: { run, batchId, chunks },
    }),
    run.frameId,
    chunks.length
  );
}

export function appendSynonGoAssistantMessage(run: TranscriptStreamRun, text: string, batchId = randomUUID()): void {
  assertTranscriptFixtureResponse(
    runFixture({
      action: 'append-transcript-assistant',
      frameId: run.frameId,
      transcript: { run, batchId, text },
    }),
    run.frameId,
    2
  );
}

export function beginSynonGoStreamingParity(run: TranscriptStreamRun, copy: StreamingParityCopy): void {
  assertTranscriptFixtureResponse(
    runFixture({
      action: 'begin-streaming-parity',
      frameId: run.frameId,
      transcript: { run, batchId: run.runId, copy },
    }),
    run.frameId,
    4
  );
}

export function completeSynonGoStreamingParity(
  run: TranscriptStreamRun,
  copy: StreamingParityCopy,
  artifactRefs: Array<{ artifactId: string; versionId: string }>
): void {
  assertTranscriptFixtureResponse(
    runFixture({
      action: 'complete-streaming-parity',
      frameId: run.frameId,
      transcript: { run, batchId: run.runId, copy, artifactRefs },
    }),
    run.frameId,
    3
  );
}

export function beginSynonGoStreamingRecovery(run: TranscriptStreamRun, recovery: StreamingRecoveryCopy): void {
  assertTranscriptFixtureResponse(
    runFixture({
      action: 'begin-streaming-recovery',
      frameId: run.frameId,
      transcript: { run, batchId: run.runId, recovery },
    }),
    run.frameId,
    5
  );
}

export function completeSynonGoStreamingRecovery(run: TranscriptStreamRun, recovery: StreamingRecoveryCopy): void {
  assertTranscriptFixtureResponse(
    runFixture({
      action: 'complete-streaming-recovery',
      frameId: run.frameId,
      transcript: { run, batchId: run.runId, recovery },
    }),
    run.frameId,
    3
  );
}

export function removeSynonGoDelegateFixture(): void {
  const response = runFixture({ action: 'remove-delegate' }) as {
    ok?: boolean;
    projectId?: string;
  };
  if (response.ok !== true || response.projectId !== 'delegate-fixture-project') {
    throw new Error(`Go delegate fixture cleanup returned an invalid response: ${JSON.stringify(response)}`);
  }
}

export function prepareSynonGoTranscriptRebase(frameId: string, historyCount = 120): TranscriptRebasePreparation {
  const response = runFixture({ action: 'prepare-transcript-rebase', frameId, historyCount }) as {
    ok?: boolean;
  } & Partial<TranscriptRebasePreparation>;
  if (
    response.ok !== true ||
    response.frameId !== frameId ||
    typeof response.cutoverId !== 'string' ||
    response.cutoverId.length !== 64 ||
    typeof response.sourceBranchId !== 'string' ||
    typeof response.sourceGeneration !== 'number' ||
    typeof response.sourceThroughPublicationSequence !== 'number' ||
    typeof response.eventCount !== 'number' ||
    typeof response.cursorCount !== 'number'
  ) {
    throw new Error(`Go transcript rebase preparation returned an invalid response: ${JSON.stringify(response)}`);
  }
  return response as TranscriptRebasePreparation;
}

export function activateSynonGoTranscriptRebase(frameId: string, cutoverId: string): TranscriptRebaseActivation {
  const response = runFixture({ action: 'activate-transcript-rebase', frameId, cutoverId }) as {
    ok?: boolean;
  } & Partial<TranscriptRebaseActivation>;
  if (
    response.ok !== true ||
    response.frameId !== frameId ||
    response.cutoverId !== cutoverId ||
    typeof response.activationId !== 'string' ||
    response.activationId.length !== 64 ||
    typeof response.targetEpoch !== 'number' ||
    typeof response.authorityGeneration !== 'number' ||
    typeof response.activeBranchId !== 'string' ||
    typeof response.eventCount !== 'number'
  ) {
    throw new Error(`Go transcript rebase activation returned an invalid response: ${JSON.stringify(response)}`);
  }
  return response as TranscriptRebaseActivation;
}

function runFixture(
  input:
    | FrameFixture
    | { action: 'seed-delegate' | 'remove-delegate' }
    | ({ action: 'seed-ask-user' } & AskUserFixture)
    | { action: 'seed-scroll-history'; frameId: string; historyCount: number }
    | { action: 'prepare-transcript-rebase'; frameId: string; historyCount: number }
    | { action: 'activate-transcript-rebase'; frameId: string; cutoverId: string }
    | TranscriptFixtureCommand
): unknown {
  if (synonGoFrameFixtureUnavailableReason) throw new Error(synonGoFrameFixtureUnavailableReason);
  const command = fixtureWSLDistro ? 'wsl.exe' : fixtureBinary;
  const args = fixtureWSLDistro
    ? ['-d', fixtureWSLDistro, '--', 'env', `SYNON_GO_E2E_WORKSPACE_DB=${workspaceDatabase}`, fixtureBinary]
    : [];
  const result = spawnSync(command, args, {
    encoding: 'utf8',
    env: { ...process.env, SYNON_GO_E2E_WORKSPACE_DB: workspaceDatabase },
    input: JSON.stringify(input),
    maxBuffer: 1024 * 1024,
    timeout: 15_000,
  });
  if (result.error) throw result.error;
  if (result.status !== 0) {
    throw new Error(`Go frame fixture failed (${result.status ?? 'signal'}): ${result.stderr.trim()}`);
  }
  return JSON.parse(result.stdout) as unknown;
}

function isTranscriptStreamRun(value: unknown, frameId: string, runId: string): value is TranscriptStreamRun {
  if (!value || typeof value !== 'object') return false;
  const run = value as Partial<TranscriptStreamRun>;
  return (
    run.frameId === frameId &&
    run.runId === runId &&
    typeof run.streamUid === 'string' &&
    run.streamUid.length > 0 &&
    typeof run.ownerId === 'string' &&
    run.ownerId.length > 0 &&
    typeof run.runnerId === 'string' &&
    run.runnerId.length > 0 &&
    typeof run.attempt === 'number' &&
    run.attempt > 0 &&
    typeof run.claimToken === 'string' &&
    run.claimToken.length > 0 &&
    typeof run.claimedInputRevision === 'number' &&
    run.claimedInputRevision > 0 &&
    typeof run.resumeSource === 'string' &&
    typeof run.resumeCheckpoint === 'number' &&
    typeof run.resumeCheckpointAttempt === 'number' &&
    typeof run.claimedAt === 'string' &&
    typeof run.expiresAt === 'string'
  );
}

function assertTranscriptFixtureResponse(value: unknown, frameId: string, eventCount: number): void {
  if (!value || typeof value !== 'object') {
    throw new Error('Go transcript stream fixture returned a non-object response');
  }
  const response = value as { ok?: unknown; frameId?: unknown; eventCount?: unknown };
  if (response.ok !== true || response.frameId !== frameId || response.eventCount !== eventCount) {
    throw new Error('Go transcript stream fixture returned an invalid append response');
  }
}
