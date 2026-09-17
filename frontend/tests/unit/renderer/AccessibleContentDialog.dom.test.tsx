import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import AccessibleContentDialog from '@/renderer/components/common/AccessibleContentDialog';

describe('AccessibleContentDialog', () => {
  afterEach(() => {
    cleanup();
    document.body.replaceChildren();
  });

  it('focuses its content, traps keyboard focus and restores the opener', () => {
    const opener = document.createElement('button');
    const firstRef = React.createRef<HTMLButtonElement>();
    document.body.append(opener);
    opener.focus();
    const view = render(
      <AccessibleContentDialog
        title='Files'
        visible
        closeLabel='Close'
        showCloseButton={false}
        initialFocusRef={firstRef}
        onClose={vi.fn()}
      >
        <button ref={firstRef}>First</button>
        <button>Last</button>
      </AccessibleContentDialog>
    );

    const first = screen.getByRole('button', { name: 'First' });
    const last = screen.getByRole('button', { name: 'Last' });
    expect(first).toHaveFocus();
    last.focus();
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Tab' });
    expect(first).toHaveFocus();

    view.rerender(
      <AccessibleContentDialog
        title='Files'
        visible={false}
        closeLabel='Close'
        showCloseButton={false}
        initialFocusRef={firstRef}
        onClose={vi.fn()}
      >
        <button>First</button>
      </AccessibleContentDialog>
    );
    expect(opener).toHaveFocus();
  });

  it('supports close controls while refusing dismissal during a busy operation', () => {
    const onClose = vi.fn();
    const view = render(
      <AccessibleContentDialog title='Files' visible closeLabel='Close' onClose={onClose}>
        Content
      </AccessibleContentDialog>
    );

    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
    expect(onClose).toHaveBeenCalledOnce();
    onClose.mockClear();
    view.rerender(
      <AccessibleContentDialog title='Files' visible closeLabel='Close' busy onClose={onClose}>
        Content
      </AccessibleContentDialog>
    );
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(onClose).not.toHaveBeenCalled();
  });
});
