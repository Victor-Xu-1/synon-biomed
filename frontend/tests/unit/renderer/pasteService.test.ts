import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/renderer/services/FileService', () => ({
  getFileExtension: (fileName: string) => {
    const index = fileName.lastIndexOf('.');
    return index > -1 ? fileName.substring(index).toLowerCase() : '';
  },
  UPLOAD_ABORTED_ERROR: 'Upload aborted',
  uploadFileViaHttp: vi.fn(async (file: File) => `/uploads/${file.name}`),
}));

vi.mock('@/renderer/hooks/file/useUploadState', () => ({
  trackUpload: vi.fn(() => ({
    finish: vi.fn(),
    onProgress: vi.fn(),
  })),
}));

import { PasteService, getClipboardFiles } from '@/renderer/services/PasteService';

function createClipboardEvent(files: File[], text = ''): ClipboardEvent {
  const items = files.map((file) => ({
    getAsFile: () => file,
    kind: 'file' as const,
    type: file.type,
  }));

  return {
    clipboardData: {
      files: files.length > 1 ? files : [],
      getData: vi.fn(() => text),
      items,
    },
    stopPropagation: vi.fn(),
  } as unknown as ClipboardEvent;
}

afterEach(() => {
  vi.clearAllMocks();
});

describe('PasteService clipboard file handling', () => {
  it('merges files exposed only through clipboard items', () => {
    const file = new File(['video'], 'clip.mp4', { type: 'video/mp4' });
    const event = createClipboardEvent([file]);

    expect(getClipboardFiles(event.clipboardData)).toEqual([file]);
  });

  it('accepts a video exposed through clipboard items and uploads it', async () => {
    const file = new File(['video'], 'clip.mp4', { type: 'video/mp4' });
    const onFilesAdded = vi.fn();

    const handled = await PasteService.handlePaste(
      createClipboardEvent([file]),
      ['.mp4'],
      onFilesAdded,
      undefined,
      'conversation-1'
    );

    expect(handled).toBe(true);
    expect(onFilesAdded).toHaveBeenCalledWith([
      expect.objectContaining({
        name: 'clip.mp4',
        path: '/uploads/clip.mp4',
        type: 'video/mp4',
      }),
    ]);
  });

  it('uses the wildcard selector for arbitrary files and names nameless media', async () => {
    const file = new File(['audio'], 'blob', { type: 'audio/webm' });
    const onFilesAdded = vi.fn();

    await PasteService.handlePaste(
      createClipboardEvent([file]),
      ['*'],
      onFilesAdded,
      undefined,
      'conversation-1',
      'guid',
      {
        next: () => 1,
      }
    );

    expect(onFilesAdded).toHaveBeenCalledWith([
      expect.objectContaining({
        name: 'pasted_audio-1.webm',
        path: '/uploads/pasted_audio-1.webm',
        type: 'audio/webm',
      }),
    ]);
  });
});
