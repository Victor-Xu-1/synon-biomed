import { Close } from '@icon-park/react';
import React, { useEffect, useId, useRef } from 'react';

const FOCUSABLE_SELECTOR =
  'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

type AccessibleContentDialogProps = React.PropsWithChildren<{
  title: React.ReactNode;
  visible: boolean;
  closeLabel: string;
  role?: 'dialog' | 'alertdialog';
  busy?: boolean;
  showCloseButton?: boolean;
  maxWidthClassName?: string;
  overlayClassName?: string;
  dialogClassName?: string;
  headerClassName?: string;
  initialFocusRef?: React.RefObject<HTMLElement | null>;
  onClose: () => void;
}>;

export const AccessibleContentDialog: React.FC<AccessibleContentDialogProps> = ({
  title,
  visible,
  closeLabel,
  role = 'dialog',
  busy = false,
  showCloseButton = true,
  maxWidthClassName = 'max-w-480px',
  overlayClassName = '',
  dialogClassName = '',
  headerClassName = '',
  initialFocusRef,
  onClose,
  children,
}) => {
  const titleId = useId();
  const dialogRef = useRef<HTMLDialogElement>(null);
  const previousFocusRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    if (!visible) return;
    previousFocusRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const firstFocusable = dialogRef.current?.querySelector<HTMLElement>('[autofocus], ' + FOCUSABLE_SELECTOR);
    const requestedFocus = initialFocusRef?.current;
    (requestedFocus?.matches(':disabled') ? firstFocusable : (requestedFocus ?? firstFocusable))?.focus();
    return () => {
      previousFocusRef.current?.focus();
      previousFocusRef.current = null;
    };
  }, [initialFocusRef, visible]);

  if (!visible) return null;
  return (
    <div
      className={`accessible-content-dialog__overlay fixed inset-0 z-1001 flex items-center justify-center p-16px ${overlayClassName}`}
      onMouseDown={(event) => {
        if (event.target === event.currentTarget && !busy) onClose();
      }}
    >
      <dialog
        ref={dialogRef}
        open
        role={role}
        aria-modal='true'
        aria-labelledby={titleId}
        className={`accessible-content-dialog__surface relative m-0 w-full ${maxWidthClassName} border border-solid p-0 text-t-primary ${dialogClassName}`}
        onCancel={(event) => {
          event.preventDefault();
          if (!busy) onClose();
        }}
        onKeyDown={(event) => {
          if (event.key === 'Escape' && !busy) {
            event.preventDefault();
            onClose();
            return;
          }
          if (event.key !== 'Tab') return;
          const focusable = [...(dialogRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR) ?? [])];
          if (focusable.length === 0) return;
          const first = focusable[0];
          const last = focusable.at(-1)!;
          if (event.shiftKey && document.activeElement === first) {
            event.preventDefault();
            last.focus();
          } else if (!event.shiftKey && document.activeElement === last) {
            event.preventDefault();
            first.focus();
          }
        }}
      >
        <header
          className={`accessible-content-dialog__header flex items-center gap-12px border-b border-solid px-20px py-16px ${headerClassName}`}
        >
          <h2 id={titleId} className='m-0 min-w-0 flex-1 text-16px font-600'>
            {title}
          </h2>
          {showCloseButton && (
            <button
              type='button'
              aria-label={closeLabel}
              disabled={busy}
              className='accessible-content-dialog__close flex h-32px w-32px shrink-0 items-center justify-center border-0 bg-transparent text-t-secondary disabled:opacity-50'
              onClick={onClose}
            >
              <Close theme='outline' size={16} />
            </button>
          )}
        </header>
        {children}
      </dialog>
    </div>
  );
};

export default AccessibleContentDialog;
