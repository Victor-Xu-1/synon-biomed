import { cleanup, fireEvent, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import SynonBiomedReviewFindingsContent from '@/renderer/components/synonBiomed/runtime/SynonBiomedReviewFindingsContent';
import type { SynonBiomedVerificationCheck } from '@/renderer/services/synonBiomedAnnotations';
import { renderWithI18n } from '../i18nTestUtils';

const check = (overrides: Partial<SynonBiomedVerificationCheck> = {}): SynonBiomedVerificationCheck => ({
  id: 'check-1',
  rootFrameId: 'root-frame',
  artifactVersionId: null,
  claimId: null,
  claim: 'The proposed compound meets the stated target criteria.',
  verdict: 'fail',
  severity: 'high',
  evidence: 'The cited source does not support the target claim.',
  rebuttal: 'Confirm the source and update the claim before delivery.',
  reviewerIndex: 0,
  reviewerModel: 'reviewer-model',
  reviewerFrameId: 'review-frame',
  sourceRef: null,
  status: 'unaddressed',
  reflagCount: null,
  createdAt: '2026-08-10T08:02:03Z',
  ...overrides,
});

describe('SynonBiomedReviewFindingsContent', () => {
  afterEach(() => cleanup());

  it('renders the complete review record while keeping repair limited to actionable checks', async () => {
    const onRepairAndRegenerate = vi.fn();
    await renderWithI18n(
      <SynonBiomedReviewFindingsContent
        checks={[
          check(),
          check({
            id: 'check-2',
            verdict: 'warn',
            severity: 'medium',
            status: 'resolved',
            claim: 'Add a clearer control comparison.',
          }),
          check({
            id: 'check-3',
            verdict: 'pass',
            severity: null,
            status: 'resolved',
            claim: 'The reviewed evidence supports the target claim.',
            evidence: 'The cited primary source matches the delivered claim.',
            rebuttal: null,
          }),
          check({
            id: 'check-4',
            verdict: 'inconclusive',
            severity: null,
            status: 'resolved',
            claim: 'The available evidence is insufficient for one secondary claim.',
            evidence: null,
            rebuttal: null,
          }),
        ]}
        loading={false}
        error={null}
        onRetry={vi.fn()}
        onRepairAndRegenerate={onRepairAndRegenerate}
      />,
      'en-US'
    );

    expect(screen.getByTestId('synon-biomed-review-findings')).toBeInTheDocument();
    expect(screen.getByTestId('synon-biomed-review-findings')).toHaveTextContent(
      'The reviewer traces claims against the recorded execution and relevant artifacts; it does not re-run the analysis.'
    );
    expect(screen.getAllByTestId('synon-biomed-review-finding')).toHaveLength(4);
    expect(screen.getByText('The proposed compound meets the stated target criteria.')).toBeInTheDocument();
    expect(screen.getByText('Add a clearer control comparison.')).toBeInTheDocument();
    expect(screen.getByText('Needs revision')).toBeInTheDocument();
    expect(screen.getByText('Warning')).toBeInTheDocument();
    expect(screen.getByText('The reviewed evidence supports the target claim.')).toBeInTheDocument();
    expect(screen.getByText('Passed')).toBeInTheDocument();
    expect(screen.getByText('Inconclusive')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Fix issues and regenerate' }));
    expect(onRepairAndRegenerate).toHaveBeenCalledOnce();
  });

  it('shows a passing review as durable content rather than an empty state', async () => {
    await renderWithI18n(
      <SynonBiomedReviewFindingsContent
        checks={[
          check({
            verdict: 'pass',
            severity: null,
            status: 'resolved',
            claim: 'The final report is supported by the cited evidence.',
            evidence: 'The reviewer verified the report and its source inventory.',
            rebuttal: null,
          }),
        ]}
        loading={false}
        error={null}
        onRetry={vi.fn()}
      />,
      'en-US'
    );

    expect(screen.getByTestId('synon-biomed-review-findings')).toHaveTextContent(
      'The final report is supported by the cited evidence.'
    );
    expect(screen.getByTestId('synon-biomed-review-finding')).toHaveTextContent('Passed');
    expect(screen.queryByText('This review has no issues to show.')).not.toBeInTheDocument();
  });

  it('states the full-session scope and chunk coverage of a manual audit', async () => {
    await renderWithI18n(
      <SynonBiomedReviewFindingsContent
        checks={[
          check({
            verdict: 'pass',
            severity: null,
            status: 'resolved',
            sourceRef: {
              review_scope: 'full_root_session',
              message_count: 27,
              review_chunk_count: 3,
            },
          }),
        ]}
        loading={false}
        error={null}
        onRetry={vi.fn()}
      />,
      'en-US'
    );

    expect(screen.getByTestId('synon-biomed-review-scope')).toHaveTextContent(
      'Manual audit covered the full root-session history: 27 messages across 3 review windows.'
    );
  });

  it('states automatic background-checkpoint and terminal coverage', async () => {
    await renderWithI18n(
      <SynonBiomedReviewFindingsContent
        checks={[
          check({
            verdict: 'pass',
            severity: null,
            status: 'resolved',
            sourceRef: {
              review_scope: 'logical_task_terminal',
              automatic_checkpoint_count: 2,
              review_chunk_count: 3,
            },
          }),
        ]}
        loading={false}
        error={null}
        onRetry={vi.fn()}
      />,
      'en-US'
    );

    expect(screen.getByTestId('synon-biomed-review-scope')).toHaveTextContent(
      'Background checkpoints reviewed: 2; terminal tail included; 3 review windows aggregated.'
    );
  });

  it('offers a retry action when loading the review findings fails', async () => {
    const onRetry = vi.fn();
    await renderWithI18n(
      <SynonBiomedReviewFindingsContent
        checks={[]}
        loading={false}
        error='Failed to load review issues.'
        onRetry={onRetry}
      />,
      'en-US'
    );

    fireEvent.click(screen.getByRole('button', { name: 'Reload review result' }));
    expect(onRetry).toHaveBeenCalledOnce();
  });
});
