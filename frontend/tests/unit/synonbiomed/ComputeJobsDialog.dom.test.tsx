import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const mocks = vi.hoisted(() => ({
  loadJob: vi.fn(),
  loadLog: vi.fn(),
  logChunk: null as null | ((event: { job_id: string; stream: 'out' | 'err'; chunk: string }) => void),
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    compute: {
      jobLogChunk: {
        on: (callback: (event: { job_id: string; stream: 'out' | 'err'; chunk: string }) => void) => {
          mocks.logChunk = callback;
          return () => {
            if (mocks.logChunk === callback) mocks.logChunk = null;
          };
        },
      },
    },
  },
}));
vi.mock('@icon-park/react', () => ({
  ArrowLeft: () => <span />,
  Copy: () => <span />,
  LinkOne: () => <span />,
  Right: () => <span />,
}));
vi.mock('@/renderer/services/synonBiomedCompute', () => ({
  loadSynonBiomedComputeJob: mocks.loadJob,
  loadSynonBiomedComputeJobLog: mocks.loadLog,
}));

import ComputeJobsDialog from '@/renderer/components/synonBiomed/runtime/ComputeJobsDialog';

const job = {
  jobId: 'job-1',
  environment: 'python',
  tierType: 'gpu',
  provider: 'byoc:modal',
  frameId: 'frame-1',
  projectId: 'project-1',
  state: 'running',
  startedAt: '2026-08-21T08:00:00Z',
  startedAtIso: '2026-08-21T08:00:00Z',
  intent: 'Dock 30 CRBN ligands',
  hardwareDetails: 'A100',
  originToolUseId: 'tool-1',
  rootFrameId: 'root-1',
  providerFamily: 'byoc',
  providerLabel: 'Modal',
  externalId: 'sandbox-1',
  externalUrl: 'https://example.invalid/sandbox-1',
  supportsTail: true,
  endedAtIso: null,
  harvest: null,
  errorKind: null,
  leftOnRemote: [],
  systemHint: null,
};

describe('ComputeJobsDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.logChunk = null;
    mocks.loadJob.mockResolvedValue(job);
    mocks.loadLog.mockImplementation((_jobId: string, options: { stream?: 'stdout' | 'stderr' }) =>
      Promise.resolve({
        exists: true,
        size: options.stream === 'stderr' ? 8 : 12,
        content: options.stream === 'stderr' ? 'warning\n' : 'started\n',
        truncated: false,
      })
    );
  });

  it('shows the workspace job detail and separate stdout/stderr streams', async () => {
    await renderWithI18n(
      <ComputeJobsDialog open jobs={[job]} initialJobId='job-1' otherCount={0} loadError={false} onClose={vi.fn()} />,
      'en-US'
    );

    expect(await screen.findByTestId('compute-job-detail')).toHaveTextContent('Dock 30 CRBN ligands');
    expect(screen.getByTestId('compute-job-detail')).toHaveTextContent('Modal');
    expect(screen.getByTestId('compute-job-detail')).toHaveTextContent('A100');
    expect(await screen.findByText('started')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('tab', { name: /stderr/ }));
    expect(screen.getByText('warning')).toBeInTheDocument();

    act(() => mocks.logChunk?.({ job_id: 'job-1', stream: 'err', chunk: 'live tail\n' }));
    await waitFor(() => expect(screen.getByText(/live tail/)).toBeInTheDocument());
  });

  it('does not render a non-HTTP remote job URL from persisted provider data', async () => {
    const unsafeJob = { ...job, externalUrl: 'javascript:alert(1)' };
    mocks.loadJob.mockResolvedValue(unsafeJob);
    await renderWithI18n(
      <ComputeJobsDialog
        open
        jobs={[unsafeJob]}
        initialJobId='job-1'
        otherCount={0}
        loadError={false}
        onClose={vi.fn()}
      />,
      'en-US'
    );

    expect(await screen.findByTestId('compute-job-detail')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Open remote job' })).toBeNull();
  });
});
