import { cleanup, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import SynonBiomedPlanReviewContent from '@/renderer/components/synonBiomed/runtime/SynonBiomedPlanReviewContent';
import type {
  SynonBiomedPlanDocument,
  SynonBiomedPlanReference,
} from '@/renderer/components/synonBiomed/runtime/runtimeOperationsModel';
import { renderWithI18n } from '../i18nTestUtils';

const document: SynonBiomedPlanDocument = {
  version: 3,
  taskSummary: 'Analyze a public single-cell dataset.',
  feasibility: {
    confidence: 'medium',
    rationale: 'The data is public and the workflow is reproducible.',
  },
  phases: [
    {
      id: 'phase-1',
      name: 'Data acquisition',
      steps: [],
      delegations: [
        {
          id: 'delegation-1',
          name: 'Acquire data',
          agentName: 'Research agent',
          steps: [
            {
              id: 'step-1',
              title: 'Download metadata',
              description: 'Fetch and validate the public metadata file.',
            },
          ],
        },
      ],
    },
  ],
};

const reference: SynonBiomedPlanReference = {
  artifactId: 'plan-current',
  versionId: 'plan-current-v1',
  revisionNumber: 2,
  revisionCount: 2,
  contentVersion: 1,
  generatedAt: '2026-08-27T09:52:00.000Z',
};

describe('SynonBiomedPlanReviewContent', () => {
  afterEach(() => cleanup());

  it('uses the shared lightweight plan surface without unsafe border utilities', async () => {
    await renderWithI18n(
      <SynonBiomedPlanReviewContent
        document={document}
        reference={reference}
        loading={false}
        error={null}
        onRetry={vi.fn()}
      />,
      'en-US'
    );

    const plan = screen.getByTestId('synon-biomed-plan-review');
    const phase = screen.getByTestId('synon-biomed-plan-phase');
    const delegation = screen.getByTestId('synon-biomed-plan-delegation');

    expect(plan).toHaveClass('synon-biomed-plan-review');
    expect(screen.getByText('Current execution plan')).toBeInTheDocument();
    expect(screen.getByText('Revision 2 of 2')).toBeInTheDocument();
    expect(screen.queryByText(/Plan version/)).not.toBeInTheDocument();
    expect(phase).toHaveClass('synon-biomed-plan-review__phase');
    expect(delegation).toHaveClass('synon-biomed-plan-review__delegation');

    for (const element of [phase, delegation]) {
      expect(element.className).not.toMatch(/(?:^|\s)border-solid(?:\s|$)/);
      expect(element.className).not.toMatch(/(?:^|\s)border-l-2(?:\s|$)/);
    }
  });
});
