import { ConfigProvider } from '@arco-design/web-react';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  ArtifactAnnotationsPanel,
  ArtifactVerificationPanel,
} from '@/renderer/pages/artifact/ArtifactAnnotationPanels';
import { renderWithI18n } from '../i18nTestUtils';

const render = (ui: React.ReactElement) => renderWithI18n(ui);

const mocks = vi.hoisted(() => ({
  loadAnnotations: vi.fn(),
  createAnnotation: vi.fn(),
  updateAnnotation: vi.fn(),
  deleteAnnotation: vi.fn(),
  suggestEdit: vi.fn(),
  applyEdit: vi.fn(),
  loadVerification: vi.fn(),
  requestAudit: vi.fn(),
}));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: {
      useMessage: () => [{ success: vi.fn(), error: vi.fn() }, null],
    },
  };
});

vi.mock('@/renderer/services/synonBiomedAnnotations', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/renderer/services/synonBiomedAnnotations')>();
  return {
    ...actual,
    loadSynonBiomedArtifactAnnotations: mocks.loadAnnotations,
    createSynonBiomedArtifactAnnotation: mocks.createAnnotation,
    updateSynonBiomedArtifactAnnotation: mocks.updateAnnotation,
    deleteSynonBiomedArtifactAnnotation: mocks.deleteAnnotation,
    suggestSynonBiomedArtifactEdit: mocks.suggestEdit,
    applySynonBiomedArtifactEdit: mocks.applyEdit,
    loadSynonBiomedArtifactVerification: mocks.loadVerification,
    requestSynonBiomedFrameAudit: mocks.requestAudit,
  };
});

const annotation = {
  id: 'annotation-1',
  artifactId: 'artifact-1',
  targetKey: 'av:version-1',
  label: '①',
  contentChecksum: 'checksum-1',
  type: 'text_selection' as const,
  text: 'Review the STAT6 assay claim',
  xPercent: null,
  yPercent: null,
  startLine: 4,
  startColumn: 2,
  endLine: 5,
  endColumn: 20,
  selectionText: 'STAT6 assay',
  pageNumber: 2,
  selectionPrefix: null,
  screenshotArtifactId: null,
  elementSelector: null,
  elementDescriptor: null,
  addressedAt: null,
  addressedInFrameId: null,
  createdAt: '2026-07-13T00:00:00.000Z',
};

describe('Artifact annotation and verification panels', () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset();
    mocks.loadAnnotations.mockResolvedValue({
      targetKey: 'av:version-1',
      currentChecksum: 'checksum-1',
      annotations: [annotation],
    });
    mocks.createAnnotation.mockResolvedValue({
      ...annotation,
      id: 'annotation-2',
      label: '②',
      type: 'point',
      text: 'New review note',
      selectionText: null,
      pageNumber: null,
      startLine: null,
    });
    mocks.loadVerification.mockResolvedValue([
      {
        id: 'check-pass',
        rootFrameId: 'frame-1',
        artifactVersionId: 'version-1',
        claimId: 'claim-1',
        claim: 'STAT6 binding improved',
        verdict: 'pass',
        severity: 'low',
        evidence: 'Two independent assay runs agree.',
        rebuttal: null,
        reviewerIndex: 0,
        reviewerModel: 'reviewer-model',
        reviewerFrameId: 'reviewer-frame',
        sourceRef: { kind: 'artifact_version' },
        status: 'resolved',
        reflagCount: 0,
        createdAt: '2026-07-13T00:00:00.000Z',
      },
      {
        id: 'check-warn',
        rootFrameId: 'frame-1',
        artifactVersionId: 'version-1',
        claimId: 'claim-2',
        claim: 'Assay variance remains acceptable',
        verdict: 'warn',
        severity: 'medium',
        evidence: 'Variance is near the threshold.',
        rebuttal: null,
        reviewerIndex: 1,
        reviewerModel: 'reviewer-model',
        reviewerFrameId: 'reviewer-frame',
        sourceRef: { kind: 'artifact_version' },
        status: 'open',
        reflagCount: 1,
        createdAt: '2026-07-13T00:00:00.000Z',
      },
    ]);
    mocks.requestAudit.mockResolvedValue({ frame_id: 'audit-frame' });
    mocks.suggestEdit.mockResolvedValue('STAT6 inhibition reduced viability.');
    mocks.applyEdit.mockResolvedValue({
      versionId: 'version-2',
      versionNumber: 2,
      artifactId: 'artifact-1',
      parentVersionId: 'version-1',
      carriedAnnotations: [{ ...annotation, id: 'annotation-carried' }],
    });
  });

  it('renders version anchors and creates a real annotation-shaped record', async () => {
    await render(
      <ConfigProvider>
        <ArtifactAnnotationsPanel artifactId='artifact-1' versionId='version-1' />
      </ConfigProvider>
    );

    expect(await screen.findByText('Review the STAT6 assay claim')).toBeInTheDocument();
    expect(screen.getByText('“STAT6 assay”')).toBeInTheDocument();
    expect(screen.getByText('第 2 页')).toBeInTheDocument();
    expect(screen.getByText('第 4 行')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '添加' }));
    fireEvent.change(screen.getByRole('textbox', { name: '批注内容' }), { target: { value: 'New review note' } });
    const addButtons = screen.getAllByRole('button', { name: '添加' });
    fireEvent.click(addButtons.at(-1)!);

    await waitFor(() =>
      expect(mocks.createAnnotation).toHaveBeenCalledWith(
        'artifact-1',
        'version-1',
        expect.objectContaining({ type: 'point', text: 'New review note' })
      )
    );
    expect(await screen.findByText('New review note')).toBeInTheDocument();
  });

  it('shows verification verdict counts and starts an audit for the creating frame', async () => {
    await render(
      <ConfigProvider>
        <ArtifactVerificationPanel versionId='version-1' rootFrameId='frame-1' />
      </ConfigProvider>
    );

    expect(await screen.findByText('STAT6 binding improved')).toBeInTheDocument();
    expect(screen.getByText('Assay variance remains acceptable')).toBeInTheDocument();
    expect(screen.getByText('Two independent assay runs agree.')).toBeInTheDocument();
    expect(screen.getAllByText('通过')[0].parentElement).toHaveTextContent('1');
    expect(screen.getAllByText('警告')[0].parentElement).toHaveTextContent('1');

    fireEvent.click(screen.getByRole('button', { name: '重新审计' }));
    await waitFor(() => expect(mocks.requestAudit).toHaveBeenCalledWith('frame-1'));
  });

  it.each([false, true])('does not refresh after unmount when audit response is pending: %s', async (pending) => {
    let completeAudit!: (value: { frame_id: string }) => void;
    if (pending)
      mocks.requestAudit.mockReturnValueOnce(
        new Promise((resolve) => {
          completeAudit = resolve;
        })
      );
    const view = await render(
      <ConfigProvider>
        <ArtifactVerificationPanel versionId='version-1' rootFrameId='frame-1' />
      </ConfigProvider>
    );
    await screen.findByText('STAT6 binding improved');
    vi.useFakeTimers();
    try {
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: '重新审计' }));
      });
      view.unmount();
      await act(async () => {
        if (pending) completeAudit({ frame_id: 'audit-frame' });
        await Promise.resolve();
        await vi.advanceTimersByTimeAsync(1200);
      });
      expect(mocks.loadVerification).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it('reviews a generated diff, allows manual revision and applies an immutable child version', async () => {
    const onVersionApplied = vi.fn();
    await render(
      <ConfigProvider>
        <ArtifactAnnotationsPanel artifactId='artifact-1' versionId='version-1' onVersionApplied={onVersionApplied} />
      </ConfigProvider>
    );

    await screen.findByText('Review the STAT6 assay claim');
    fireEvent.click(screen.getByRole('button', { name: '改进批注 ①' }));
    expect(screen.getByRole('dialog', { name: '改进选区' })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: '修改要求或问题' })).toHaveValue('Review the STAT6 assay claim');

    fireEvent.click(screen.getByRole('button', { name: '生成修改' }));
    await waitFor(() =>
      expect(mocks.suggestEdit).toHaveBeenCalledWith('artifact-1', 'version-1', {
        selectedText: 'STAT6 assay',
        annotationText: 'Review the STAT6 assay claim',
        mode: 'edit',
      })
    );
    expect(await screen.findByText('修改建议')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '手工修改建议' }));
    fireEvent.change(screen.getByRole('textbox', { name: '修改后的建议文本' }), {
      target: { value: 'STAT6 inhibition consistently reduced viability.' },
    });
    fireEvent.click(screen.getByRole('button', { name: '应用为新版本' }));

    await waitFor(() =>
      expect(mocks.applyEdit).toHaveBeenCalledWith('artifact-1', 'version-1', {
        selectedText: 'STAT6 assay',
        replacementText: 'STAT6 inhibition consistently reduced viability.',
      })
    );
    await waitFor(() =>
      expect(onVersionApplied).toHaveBeenCalledWith(expect.objectContaining({ versionId: 'version-2' }))
    );
    expect(await screen.findByText('修改已应用，新版本已创建')).toBeInTheDocument();
  });

  it('supports the v1.1 ask mode without exposing an apply action for an answer', async () => {
    mocks.suggestEdit.mockResolvedValueOnce('The selected assay statement does not specify direction or magnitude.');
    await render(
      <ConfigProvider>
        <ArtifactAnnotationsPanel artifactId='artifact-1' versionId='version-1' />
      </ConfigProvider>
    );

    await screen.findByText('Review the STAT6 assay claim');
    fireEvent.click(screen.getByRole('button', { name: '改进批注 ①' }));
    fireEvent.change(screen.getByRole('textbox', { name: '修改要求或问题' }), {
      target: { value: 'What is missing from this statement?' },
    });
    const askButtons = screen.getAllByRole('button', { name: '询问' });
    fireEvent.click(askButtons.at(-1)!);

    await waitFor(() =>
      expect(mocks.suggestEdit).toHaveBeenCalledWith('artifact-1', 'version-1', {
        selectedText: 'STAT6 assay',
        annotationText: 'What is missing from this statement?',
        mode: 'ask',
      })
    );
    expect(
      await screen.findByText('The selected assay statement does not specify direction or magnitude.')
    ).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '应用为新版本' })).not.toBeInTheDocument();
  });

  it('renders annotation anchors and actions in English', async () => {
    await renderWithI18n(
      <ConfigProvider>
        <ArtifactAnnotationsPanel artifactId='artifact-1' versionId='version-1' />
      </ConfigProvider>,
      'en-US'
    );

    expect(await screen.findByText('Version annotations')).toBeInTheDocument();
    expect(screen.getByText('Page 2')).toBeInTheDocument();
    expect(screen.getByText('Line 4')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Refine annotation ①' })).toBeInTheDocument();
    expect(screen.queryByText('版本批注')).not.toBeInTheDocument();
  });
});
