// The keyboard shortcuts overview. A lazy chunk of its own (#288): it
// brings Carbon's Modal, which the mail view does not need otherwise.
import { useLayoutEffect } from "react";
import { createPortal } from "react-dom";
import { FeatureFlags, Modal } from "@carbon/react";
import { t } from "./i18n";

export const shortcutKeys = () =>
  [
    [["/"], t.keySearch],
    [["j"], t.keyNext],
    [["k"], t.keyPrevious],
    [["u", "Esc"], t.keyBack],
    [["g", "a"], t.keyAllMail],
    [["?"], t.keyHelp],
  ] as const;

/**
 * The shortcuts overview: a passive Carbon Modal (no buttons, Escape, the
 * close button and a click outside close it). It exists only while open,
 * and the focus goes back to the opener when it closes. The opener is the
 * element that had the focus when the user asked for the overview: the
 * caller captures it then, before this chunk has loaded.
 */
export function ShortcutsModal({ onClose, opener }: { onClose: () => void; opener: Element | null }) {
  useLayoutEffect(
    () => () => {
      if (opener instanceof HTMLElement && opener.isConnected) opener.focus();
    },
    [opener],
  );
  return createPortal(
    // Without the flag, Carbon adds two hidden "Focus sentinel" tab stops.
    <FeatureFlags enableFocusWrapWithoutSentinels>
      <Modal open passiveModal size="sm" modalHeading={t.shortcuts} closeButtonLabel={t.carbon.closeModal} onRequestClose={onClose}>
        {/* Focus starts on the list, not on the close button, whose tooltip would take the first Escape. */}
        <dl className="shortcut-list" tabIndex={-1} data-modal-primary-focus>
          {shortcutKeys().map(([keys, label]) => (
            <div key={label} className="shortcut-list__row">
              <dt>
                {keys.map((k) => (
                  <kbd key={k} className="kbd">
                    {k}
                  </kbd>
                ))}
              </dt>
              <dd>{label}</dd>
            </div>
          ))}
        </dl>
      </Modal>
    </FeatureFlags>,
    document.body,
  );
}
