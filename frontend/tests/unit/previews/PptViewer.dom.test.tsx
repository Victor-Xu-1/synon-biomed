/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { render } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';

vi.mock('@/renderer/pages/conversation/Preview/components/viewers/LightweightOfficeViewer', () => ({
  default: ({ docType, file_path, artifactId, versionId }: Record<string, string>) => (
    <div
      data-testid='lightweight-office-viewer'
      data-doctype={docType}
      data-filepath={file_path}
      data-artifact-id={artifactId}
      data-version-id={versionId}
    />
  ),
}));

import PptViewer from '@/renderer/pages/conversation/Preview/components/viewers/PptViewer';

describe('PptViewer', () => {
  it('renders the lightweight viewer with docType ppt', () => {
    const { getByTestId } = render(<PptViewer file_path='/test.pptx' />);
    const viewer = getByTestId('lightweight-office-viewer');
    expect(viewer.getAttribute('data-doctype')).toBe('ppt');
  });

  it('forwards project artifact identity to the lightweight viewer', () => {
    const { getByTestId } = render(<PptViewer artifactId='artifact-3' versionId='version-3' />);
    const viewer = getByTestId('lightweight-office-viewer');
    expect(viewer.getAttribute('data-artifact-id')).toBe('artifact-3');
    expect(viewer.getAttribute('data-version-id')).toBe('version-3');
  });

  it('renders without file_path', () => {
    const { getByTestId } = render(<PptViewer />);
    const viewer = getByTestId('lightweight-office-viewer');
    expect(viewer).toBeInTheDocument();
  });
});
