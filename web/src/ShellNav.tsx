import { forwardRef, type ComponentType, type MouseEvent, type ReactNode } from "react";
import { SideNav, SideNavDivider, SideNavItems, SideNavLink } from "@carbon/react";
import { Archive, Email, RecentlyViewed, Settings, Time, UserAvatar, UserMultiple } from "@carbon/icons-react";
import { isPlainClick } from "./clicks";
import { formatState, type ViewState } from "./urlState";
import { t } from "./i18n";

interface Props {
  id: string;
  state: ViewState;
  admin: boolean;
  /** Messages no longer on any server; the link shows while there are any (or while it is open). */
  gone: number;
  /** lg and up: Carbon's rail, which expands on hover and focus. */
  rail: boolean;
  /** Below lg: the overlay opened by the header's menu button. */
  expanded: boolean;
  onClose: () => void;
  /** Goes to a place in the app (a patch of the URL state). */
  go: (patch: Partial<ViewState>) => void;
}

const empty: ViewState = { q: "", account: "", folder: "", group: "", gone: "", m: "", view: "", tab: "", id: "", add: "", import: "" };

/**
 * The shell's side navigation: the fixed sections of the app. Accounts and
 * folders are page navigation, in the mail view's folder column.
 */
export const ShellNav = forwardRef<HTMLElement, Props>(function ShellNav({ id, state, admin, gone, rail, expanded, onClose, go }, ref) {
  const mail = state.view === "";
  const goneOn = mail && state.gone === "1";
  const link = (patch: Partial<ViewState>, current: boolean, icon: ComponentType, label: ReactNode, title?: string) => (
    <SideNavLink
      renderIcon={icon}
      href={formatState({ ...empty, ...patch }) || "/"}
      isActive={current}
      aria-current={current ? "page" : undefined}
      title={title}
      onClick={(e: MouseEvent) => {
        // A Ctrl/Cmd, Shift or Alt click opens the place in a new tab or window.
        if (!isPlainClick(e)) return;
        e.preventDefault();
        // A section opens on its list, as its href does.
        go({ account: "", folder: "", gone: "", m: "", view: "", tab: "", id: "", add: "", import: "", ...patch });
        onClose();
      }}
    >
      {label}
    </SideNavLink>
  );
  return (
    <SideNav
      ref={ref}
      id={id}
      aria-label={t.mainNavigation}
      className="shell-nav"
      isRail={rail}
      expanded={expanded}
      // Below lg the menu button and Escape (App) open and close it; Carbon's
      // focus listeners would close it on the way to the menu button, which
      // then opens it again. On lg they expand the rail on focus.
      addFocusListeners={rail}
      onOverlayClick={onClose}
    >
      <SideNavItems>
        {link({}, mail && !goneOn && !state.account && !state.folder, Email, t.allMail)}
        {(gone > 0 || goneOn) &&
          link(
            { gone: "1" },
            goneOn,
            Archive,
            <>
              {t.onlyInArchive} <span className="shell-nav__count">{gone}</span>
            </>,
            t.onlyInArchiveHint,
          )}
        <SideNavDivider />
        {link({ view: "accounts" }, state.view === "accounts", Settings, t.navAccounts)}
        {admin && link({ view: "users" }, state.view === "users", UserMultiple, t.users)}
        {link({ view: "log" }, state.view === "log", RecentlyViewed, t.log)}
        {admin && link({ view: "retention" }, state.view === "retention", Time, t.retention)}
        {link({ view: "profile" }, state.view === "profile", UserAvatar, t.profile)}
      </SideNavItems>
    </SideNav>
  );
});
