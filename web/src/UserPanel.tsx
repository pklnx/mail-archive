import type { ComponentProps, FocusEvent, ForwardRefExoticComponent, MouseEvent, ReactNode, RefAttributes, RefObject } from "react";
import { HeaderGlobalAction, HeaderPanel, Switcher, SwitcherDivider, SwitcherItem } from "@carbon/react";
import { UserAvatar } from "@carbon/icons-react";
import { isPlainClick } from "./clicks";
import { setTheme, useTheme, type Theme } from "./theme";
import { t } from "./i18n";

// Carbon passes these props on, but its types lack them: HeaderGlobalAction
// and HeaderPanel take a blur listener (HeaderPanel also an id) on their
// element, and SwitcherItem renders the element given as `as` (a <button>
// here) through the UI Shell's Link.
const Action = HeaderGlobalAction as unknown as ForwardRefExoticComponent<
  ComponentProps<typeof HeaderGlobalAction> & { onBlur: (e: FocusEvent<HTMLElement>) => void } & RefAttributes<HTMLButtonElement>
>;
const Panel = HeaderPanel as unknown as ForwardRefExoticComponent<
  {
    id: string;
    className: string;
    expanded: boolean;
    addFocusListeners: false;
    onHeaderPanelFocus: () => void;
    onBlur: (e: FocusEvent<HTMLElement>) => void;
    children: ReactNode;
  } & RefAttributes<HTMLDivElement>
>;
const Item = SwitcherItem as unknown as (props: {
  as?: "button";
  href?: string;
  "aria-label": string;
  onClick: (e: MouseEvent<HTMLElement>) => void;
  children: ReactNode;
}) => ReactNode;

interface Props {
  name: string;
  id: string;
  open: boolean;
  actionRef: RefObject<HTMLButtonElement | null>;
  panelRef: RefObject<HTMLDivElement | null>;
  /** The header action was clicked. */
  toggle: () => void;
  /** Closes the panel; the focus goes back to the action only with returnFocus. */
  close: (returnFocus: boolean) => void;
  profile: () => void;
  shortcuts: () => void;
  logout: () => void;
}

/**
 * The header's user action and its panel (Carbon UI Shell): the user name,
 * Profile, Keyboard shortcuts, the light/dark switch and Log out. The open
 * state, focusing the first item on open and Escape are App's (its key
 * listener runs first); the panel's content is mounted only while open, so
 * the collapsed panel holds nothing to tab to.
 */
export function UserPanel({ name, id, open, actionRef, panelRef, toggle, close, profile, shortcuts, logout }: Props) {
  const theme = useTheme();
  const next: Theme = theme === "dark" ? "light" : "dark";
  const label = t.userPanelOf(name);
  // Only a keyboard Tab out of the panel or its action (Shift+Tab from the
  // first item passes the action) closes it here. A null target is a click
  // in WebKit or Firefox on macOS, which focus nothing on mousedown: closing
  // then would unmount the item before its click. Carbon's outside click
  // listener closes the panel for clicks elsewhere.
  const onBlur = (e: FocusEvent<HTMLElement>) => {
    const to = e.relatedTarget;
    if (open && to instanceof Element && !panelRef.current?.contains(to) && !actionRef.current?.contains(to)) close(false);
  };
  return (
    <>
      <Action
        ref={actionRef}
        aria-label={label}
        aria-expanded={open}
        aria-controls={id}
        isActive={open}
        tooltipAlignment="end"
        onClick={toggle}
        onBlur={onBlur}
      >
        <UserAvatar size={20} />
      </Action>
      <Panel
        ref={panelRef}
        id={id}
        className="app-user-panel"
        expanded={open}
        addFocusListeners={false}
        onHeaderPanelFocus={() => close(false)}
        onBlur={onBlur}
      >
        {open && (
          <Switcher aria-label={label} expanded>
            <li className="app-user-panel__name">{name}</li>
            <Item
              href="/?view=profile"
              aria-label={t.profile}
              onClick={(e) => {
                // A Ctrl/Cmd, Shift or Alt click is left to the browser (new tab or window).
                if (!isPlainClick(e)) return;
                e.preventDefault();
                profile();
              }}
            >
              {t.profile}
            </Item>
            <Item as="button" aria-label={t.shortcuts} onClick={shortcuts}>
              {t.shortcuts}
            </Item>
            <Item as="button" aria-label={t.switchTheme(next)} onClick={() => setTheme(next)}>
              {t.switchTheme(next)}
            </Item>
            <SwitcherDivider />
            <Item as="button" aria-label={t.logOut} onClick={logout}>
              {t.logOut}
            </Item>
          </Switcher>
        )}
      </Panel>
    </>
  );
}
