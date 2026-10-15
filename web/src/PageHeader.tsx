import type { ReactNode } from "react";
import { BackIcon, iconButton } from "./icons";
import { t } from "./i18n";

interface Props {
  title: string;
  /** Back to the mail; a button on phones and tablets, where there is no sidebar. */
  close: () => void;
  /** The page's actions, the main one last. */
  children?: ReactNode;
}

/**
 * The head of a management page (accounts, users, retention, log, profile).
 * Below the sidebar breakpoint it stays at the top while the page scrolls.
 * Place it first in the page's padded (p-4) column.
 */
export function PageHeader({ title, close, children }: Props) {
  return (
    <header className="sticky top-0 z-10 -mx-4 -mt-4 flex flex-wrap items-center gap-2 border-b border-line bg-bg/95 px-4 py-2 backdrop-blur lg:static lg:mx-0 lg:mt-0 lg:border-0 lg:bg-transparent lg:p-0 lg:backdrop-blur-none">
      <button type="button" aria-label={t.backToMailLabel} title={t.backToMailLabel} className={`${iconButton} -ml-2 lg:hidden`} onClick={close}>
        <BackIcon />
      </button>
      <h1 className="mr-auto text-xl font-semibold">{title}</h1>
      {children}
    </header>
  );
}
