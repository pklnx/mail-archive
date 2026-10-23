import { useLayoutEffect, useRef, type SyntheticEvent } from "react";
import { createPortal } from "react-dom";
import { FeatureFlags, Modal, type ModalProps } from "@carbon/react";
import { t } from "./i18n";

export type DangerModalProps = Omit<ModalProps, "open" | "danger" | "closeButtonLabel">;

/**
 * A Carbon danger Modal that exists only while it is open: the caller
 * mounts it to ask and unmounts it to close, so each opening starts fresh.
 * It sits in document.body, above the page and the header, and the focus
 * goes back to where it was (the button that opened it) when it closes.
 * Carbon focuses the element marked data-modal-primary-focus, otherwise
 * the secondary button (Cancel): the safe choice.
 */
export function DangerModal({ onRequestClose, ...props }: DangerModalProps) {
  const opener = useRef<Element | null>(null);
  useLayoutEffect(() => {
    opener.current = document.activeElement;
    return () => {
      const el = opener.current;
      if (el instanceof HTMLElement && el.isConnected) el.focus();
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
      <Modal open danger closeButtonLabel={t.carbon.closeModal} onRequestClose={(e: SyntheticEvent<HTMLElement>) => close.current?.(e)} {...props} />
    </FeatureFlags>,
    document.body,
  );
}
