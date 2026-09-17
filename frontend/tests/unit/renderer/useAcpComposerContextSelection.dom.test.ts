import { act, renderHook } from '@testing-library/react';
import { useState } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ComposerContextItem } from '@/renderer/components/chat/SendBox/composerCompositionModel';
import { useAcpComposerContextSelection } from '@/renderer/pages/conversation/platforms/acp/useAcpComposerContextSelection';

const { uploadAttachmentMock } = vi.hoisted(() => ({
  uploadAttachmentMock: vi.fn(),
}));

vi.mock('@/renderer/services/onboardingService', () => ({
  uploadSynonBiomedProjectAttachment: (...args: unknown[]) => uploadAttachmentMock(...args),
}));

describe('useAcpComposerContextSelection browser attachments', () => {
  beforeEach(() => {
    uploadAttachmentMock.mockReset();
  });

  it('uploads browser files to the canonical project and adds exact artifact references in order', async () => {
    uploadAttachmentMock
      .mockResolvedValueOnce({
        artifactId: 'artifact-cdx',
        versionId: 'version-cdx',
        filename: 'ligands.cdx',
        sizeBytes: 12,
        checksum: 'a'.repeat(64),
      })
      .mockResolvedValueOnce({
        artifactId: 'artifact-pdb',
        versionId: 'version-pdb',
        filename: 'receptor.pdb',
        sizeBytes: 34,
        checksum: 'b'.repeat(64),
      });

    const { result } = renderHook(() => {
      const [items, setItems] = useState<ComposerContextItem[]>([]);
      return {
        items,
        selection: useAcpComposerContextSelection('project-1', items, setItems),
      };
    });
    const files = [
      new File(['cdx-content'], 'ligands.cdx', {
        type: 'application/octet-stream',
      }),
      new File(['pdb-content'], 'receptor.pdb', { type: 'chemical/x-pdb' }),
    ];

    await act(async () => {
      await result.current.selection.handleLocalFilesSelected(files);
    });

    expect(uploadAttachmentMock).toHaveBeenNthCalledWith(1, 'project-1', files[0], expect.any(Object));
    expect(uploadAttachmentMock).toHaveBeenNthCalledWith(2, 'project-1', files[1], expect.any(Object));
    expect(result.current.items).toMatchObject([
      {
        kind: 'artifact',
        artifactId: 'artifact-cdx',
        versionId: 'version-cdx',
        label: 'ligands.cdx',
      },
      {
        kind: 'artifact',
        artifactId: 'artifact-pdb',
        versionId: 'version-pdb',
        label: 'receptor.pdb',
      },
    ]);
  });

  it('does not route browser attachments through a temporary path without a project', async () => {
    const { result } = renderHook(() => {
      const [items, setItems] = useState<ComposerContextItem[]>([]);
      return useAcpComposerContextSelection(undefined, items, setItems);
    });

    await expect(result.current.handleLocalFilesSelected([new File(['x'], 'input.cdx')])).rejects.toThrow(
      'project is required'
    );
    expect(uploadAttachmentMock).not.toHaveBeenCalled();
  });
});
