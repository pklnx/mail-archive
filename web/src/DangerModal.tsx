import { useLayoutEffect, useRef, type SyntheticEvent } from "react";
import { createPortal } from "react-dom";
import { FeatureFlags, Modal, type ModalProps } from "@carbon/react";
import { t } from "./i18n";

export type DangerModalProps = Omit<ModalProps, "open" | "danger" | "closeButtonLabel"> & {
  /**
   * Where the focus goes when the Modal closes, if the element that opened
   * it is gone by then (a menu item of an OverflowMenu, for example).
   */
  returnFocus?: () => HTMLElement | null | undefined;
};

/**
 * A Carbon danger Modal that exists only while it is open: the caller
 * mounts it to ask and unmounts it to close, so each opening starts fresh.
 * It sits in document.body, above the page and the header, and the focus
 * goes back to where it was (the button that opened it) when it closes.
 * Carbon focuses the element marked data-modal-primary-focus, otherwise
 * the secondary button (Cancel): the safe choice.
 */
export function DangerModal(props: DangerModalProps) {
  return <AppModal danger {...props} />;
}

/** The same Modal without the danger style: for forms like Add user. */
export function AppModal({ onRequestClose, returnFocus, danger = false, ...props }: DangerModalProps & { danger?: boolean }) {
  const opener = useRef<Element | null>(null);
  const fallback = useRef(returnFocus);
  useLayoutEffect(() => {
    fallback.current = returnFocus;
  });
  useLayoutEffect(() => {
    opener.current = document.activeElement;
    return () => {
      const el = opener.current;
      if (el instanceof HTMLElement && el.isConnected && el !== document.body) {
        el.focus();
        return;
      }
      const other = fallback.current?.();
      if (other?.isConnected) other.focus();
    };
  }, []);
  // Carbon's Escape listener keeps the handler of the render that opened
  // the Modal; this one always calls the current handler, so a guard like
  // "not while it sends" sees the current state.
  const close = useRef(onRequestClose);
  useLayoutEffect(() => {
    close.current = onRequestClose;
  });
  return createPortal(
    // Without the flag, Carbon adds two hidden "Focus sentinel" tab stops.
    <FeatureFlags enableFocusWrapWithoutSentinels>
      <Modal
        open
        danger={danger}
        closeButtonLabel={t.carbon.closeModal}
        onRequestClose={(e: SyntheticEvent<HTMLElement>) => close.current?.(e)}
        {...props}
      />
    </FeatureFlags>,
    document.body,
  );
}
