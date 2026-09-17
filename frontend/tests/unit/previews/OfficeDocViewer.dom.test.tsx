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

import OfficeDocViewer from '@/renderer/pages/conversation/Preview/components/viewers/OfficeDocViewer';

describe('OfficeDocViewer', () => {
  it('renders the lightweight viewer with docType word', () => {
    const { getByTestId } = render(<OfficeDocViewer file_path='/test.docx' />);
    const viewer = getByTestId('lightweight-office-viewer');
    expect(viewer.getAttribute('data-doctype')).toBe('word');
  });

  it('forwards project artifact identity to the lightweight viewer', () => {
    const { getByTestId } = render(<OfficeDocViewer artifactId='artifact-1' versionId='version-1' />);
    const viewer = getByTestId('lightweight-office-viewer');
    expect(viewer.getAttribute('data-artifact-id')).toBe('artifact-1');
    expect(viewer.getAttribute('data-version-id')).toBe('version-1');
  });

  it('renders without file_path', () => {
    const { getByTestId } = render(<OfficeDocViewer />);
    const viewer = getByTestId('lightweight-office-viewer');
    expect(viewer).toBeInTheDocument();
  });
});
