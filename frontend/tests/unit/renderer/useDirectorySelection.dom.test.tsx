import { act, fireEvent, render, screen } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

let provider: ((data?: { properties?: string[] }) => Promise<string[] | undefined>) | undefined;

vi.mock('@/common', () => ({
  ipcBridge: {
    dialog: {
      showOpen: {
        provider: vi.fn((next) => {
          provider = next;
        }),
      },
    },
  },
}));

vi.mock('@renderer/components/settings/DirectorySelectionModal', () => ({
  default: ({ visible, isFileMode, onConfirm, onCancel }) =>
    visible ? (
      <div role='dialog' aria-label={isFileMode ? 'file-picker' : 'directory-picker'}>
        <button onClick={() => onConfirm(['/data/result.csv'])}>confirm</button>
        <button onClick={onCancel}>cancel</button>
      </div>
    ) : null,
}));

import { useDirectorySelection } from '@/renderer/hooks/file/useDirectorySelection';

function Harness() {
  const { contextHolder } = useDirectorySelection();
  return contextHolder;
}

describe('useDirectorySelection', () => {
  beforeEach(() => {
    provider = undefined;
  });

  it('settles a browser show-open invocation with the selected file', async () => {
    render(<Harness />);
    let selection: Promise<string[] | undefined> | undefined;
    await act(async () => {
      selection = provider?.({ properties: ['openFile', 'multiSelections'] });
    });
    expect(screen.getByRole('dialog', { name: 'file-picker' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'confirm' }));
    await expect(selection).resolves.toEqual(['/data/result.csv']);
  });

  it('settles cancellation instead of leaking the bridge request', async () => {
    render(<Harness />);
    let selection: Promise<string[] | undefined> | undefined;
    await act(async () => {
      selection = provider?.({ properties: ['openDirectory'] });
    });
    expect(screen.getByRole('dialog', { name: 'directory-picker' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'cancel' }));
    await expect(selection).resolves.toBeUndefined();
  });
});
