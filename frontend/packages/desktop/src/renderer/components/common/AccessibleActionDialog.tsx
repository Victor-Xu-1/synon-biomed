import React, { useRef } from 'react';
import AccessibleContentDialog from './AccessibleContentDialog';

type AccessibleActionDialogProps = React.PropsWithChildren<{
  title: string;
  visible: boolean;
  confirmText: string;
  cancelText: string;
  busy?: boolean;
  confirmDisabled?: boolean;
  danger?: boolean;
  initialFocus?: 'first' | 'confirm' | 'cancel';
  maxWidthClassName?: string;
  cancelTestId?: string;
  confirmTestId?: string;
  onCancel: () => void;
  onConfirm: () => void | Promise<void>;
}>;

const buttonClass =
  'inline-flex min-h-32px items-center justify-center rounded-4px border border-solid px-13px text-13px font-500 disabled:cursor-not-allowed disabled:opacity-50';

export const AccessibleActionDialog: React.FC<AccessibleActionDialogProps> = ({
  title,
  visible,
  confirmText,
  cancelText,
  busy = false,
  confirmDisabled = false,
  danger = false,
  initialFocus = 'first',
  maxWidthClassName,
  cancelTestId,
  confirmTestId,
  onCancel,
  onConfirm,
  children,
}) => {
  const cancelRef = useRef<HTMLButtonElement>(null);
  const confirmRef = useRef<HTMLButtonElement>(null);
  const initialFocusRef = initialFocus === 'confirm' ? confirmRef : initialFocus === 'cancel' ? cancelRef : undefined;
  return (
    <AccessibleContentDialog
      title={title}
      visible={visible}
      closeLabel={cancelText}
      role='alertdialog'
      busy={busy}
      showCloseButton={false}
      initialFocusRef={initialFocusRef}
      maxWidthClassName={maxWidthClassName}
      overlayClassName='accessible-action-dialog__overlay'
      dialogClassName='accessible-action-dialog__dialog'
      headerClassName='accessible-action-dialog__header'
      onClose={onCancel}
    >
      <div className='accessible-action-dialog__body px-20px py-18px'>{children}</div>
      <footer className='accessible-action-dialog__footer flex justify-end gap-8px border-t border-solid border-arco-2 px-20px py-14px'>
        <button
          ref={cancelRef}
          type='button'
          className={`${buttonClass} accessible-action-dialog__cancel border-arco-2 bg-1 text-t-primary hover:bg-fill-1`}
          data-testid={cancelTestId}
          disabled={busy}
          onClick={onCancel}
        >
          {cancelText}
        </button>
        <button
          ref={confirmRef}
          type='button'
          className={`${buttonClass} accessible-action-dialog__confirm ${
            danger
              ? 'border-danger-6 bg-danger-6 text-white hover:bg-danger-5'
              : 'border-[rgb(var(--primary-6))] bg-[rgb(var(--primary-6))] text-white hover:bg-[rgb(var(--primary-5))]'
          }`}
          aria-busy={busy}
          data-testid={confirmTestId}
          disabled={busy || confirmDisabled}
          onClick={() => void onConfirm()}
        >
          {confirmText}
        </button>
      </footer>
    </AccessibleContentDialog>
  );
};

export default AccessibleActionDialog;
