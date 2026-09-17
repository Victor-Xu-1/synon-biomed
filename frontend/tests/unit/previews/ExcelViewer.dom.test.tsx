/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 *
 * ExcelViewer is a thin wrapper around the internal lightweight workbook preview.
 */

import { render, screen } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';

vi.mock('@/renderer/pages/conversation/Preview/components/viewers/LightweightOfficeViewer', () => ({
  default: vi.fn(({ docType, file_path, workspace, artifactId, versionId }: Record<string, string>) =>
    React.createElement('div', {
      'data-testid': 'lightweight-office-stub',
      'data-doctype': docType,
      'data-path': file_path ?? '',
      'data-workspace': workspace ?? '',
      'data-artifact-id': artifactId ?? '',
      'data-version-id': versionId ?? '',
    })
  ),
}));

import ExcelViewer from '@/renderer/pages/conversation/Preview/components/viewers/ExcelViewer';
import LightweightOfficeViewer from '@/renderer/pages/conversation/Preview/components/viewers/LightweightOfficeViewer';

describe('ExcelViewer', () => {
  it('is a function component that can be rendered', () => {
    expect(typeof ExcelViewer).toBe('function');
    const { container } = render(React.createElement(ExcelViewer, { file_path: '/tmp/a.xlsx' }));
    expect(container.firstChild).toBeTruthy();
  });

  it('forwards project artifact props with docType="excel"', () => {
    render(React.createElement(ExcelViewer, { artifactId: 'artifact-2', versionId: 'version-2' }));
    expect(LightweightOfficeViewer).toHaveBeenCalled();
    const stub = screen.getByTestId('lightweight-office-stub');
    expect(stub.dataset.doctype).toBe('excel');
    expect(stub.dataset.artifactId).toBe('artifact-2');
    expect(stub.dataset.versionId).toBe('version-2');
  });

  it('accepts empty props without crashing', () => {
    const { container } = render(React.createElement(ExcelViewer, {}));
    expect(container.firstChild).toBeTruthy();
    const stub = screen.getByTestId('lightweight-office-stub');
    expect(stub.dataset.doctype).toBe('excel');
  });
});
