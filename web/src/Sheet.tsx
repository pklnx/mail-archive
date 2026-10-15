import { useEffect, useId, useRef, type ReactNode } from "react";
import { CloseIcon, iconButton } from "./icons";
import { t } from "./i18n";

interface Props {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
}

/**
 * A modal sheet: from the bottom on phones, centered on larger screens. It is
 * a native <dialog> opened with showModal, so the browser keeps focus inside,
 * closes it on Escape and returns focus to where it was. A tap on the
 * backdrop closes it too.
 */
export function Sheet({ open, onClose, title, children }: Props) {
  const ref = useRef<HTMLDialogElement>(null);
  const id = useId();
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) {
      if (d.showModal) d.showModal();
      else d.setAttribute("open", ""); // test environments without <dialog>
    } else if (!open && d.open) d.close();
  }, [open]);
  return (
    <dialog
      ref={ref}
      aria-labelledby={id}
      onClose={onClose}
      // The dialog element itself is only hit outside the content, on the backdrop.
      onClick={(e) => e.target === e.currentTarget && onClose()}
      className="sheet fixed inset-x-0 top-auto bottom-0 m-0 max-h-[85dvh] w-full max-w-none overflow-y-auto rounded-t-2xl bg-surface-raised p-0 text-ink shadow-2xl backdrop:bg-scrim md:inset-0 md:m-auto md:h-fit md:max-w-md md:rounded-2xl"
    >
      <div className="flex flex-col gap-3 px-4 pt-2 pb-[calc(1rem+env(safe-area-inset-bottom))]">
        <span aria-hidden className="mx-auto h-1 w-9 rounded-full bg-line-control md:hidden" />
        <div className="flex items-center justify-between gap-2">
          <h2 id={id} className="text-lg font-semibold">
            {title}
          </h2>
          <button type="button" aria-label={t.closeDialog} title={t.closeDialog} className={`${iconButton} -mr-2`} onClick={onClose}>
            <CloseIcon />
          </button>
        </div>
        {children}
      </div>
    </dialog>
  );
}
