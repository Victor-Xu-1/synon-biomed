import { fireEvent, render, renderHook, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useAttachEntry } from '@/renderer/components/chat/MobileActionSheet';

const { processDroppedFilesMock } = vi.hoisted(() => ({
  processDroppedFilesMock: vi.fn(),
}));

vi.mock('@/renderer/utils/platform', () => ({
  isElectronDesktop: () => false,
}));
vi.mock('@/renderer/hooks/context/ConversationContext', () => ({
  useConversationContextSafe: () => ({ conversation_id: 'conversation-1' }),
}));
vi.mock('@/renderer/services/FileService', () => ({
  FileService: {
    processDroppedFiles: (...args: unknown[]) => processDroppedFilesMock(...args),
  },
}));
vi.mock('@arco-design/web-react', () => ({ Message: { error: vi.fn() } }));
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

describe('mobile attachment entry', () => {
  beforeEach(() => {
    processDroppedFilesMock.mockReset();
  });

  it('routes browser files to the canonical raw-file handler when supplied', async () => {
    const onBrowserFilesAdded = vi.fn().mockResolvedValue(undefined);
    const { result } = renderHook(() =>
      useAttachEntry({
        openFileSelector: vi.fn(),
        onLocalFilesAdded: vi.fn(),
        onBrowserFilesAdded,
      })
    );
    render(result.current.hiddenFileInput);
    const file = new File(['binary-cdx'], 'ligands.cdx', {
      type: 'application/octet-stream',
    });

    fireEvent.change(screen.getByTestId('mobile-sheet-file-upload-input'), {
      target: { files: [file] },
    });

    await waitFor(() => expect(onBrowserFilesAdded).toHaveBeenCalledWith([file]));
    expect(processDroppedFilesMock).not.toHaveBeenCalled();
  });
});
