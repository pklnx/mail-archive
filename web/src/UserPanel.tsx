import type { ComponentProps, FocusEvent, ForwardRefExoticComponent, MouseEvent, ReactNode, RefAttributes, RefObject } from "react";
import { HeaderGlobalAction, HeaderPanel, Switcher, SwitcherDivider, SwitcherItem } from "@carbon/react";
import { Keyboard, Logout, UserAvatar, type CarbonIconType } from "@carbon/icons-react";
import { isPlainClick } from "./clicks";
import { setTheme, useTheme } from "./theme";
import { themeIcon } from "./ThemeToggle";
import { t } from "./i18n";

// Carbon passes these props on, but its types lack them: HeaderGlobalAction
// and HeaderPanel take a blur listener (HeaderPanel also an id) on their
// element, HeaderGlobalAction an href (its Button then renders an <a>) and
// gives its click listener the event, and SwitcherItem renders the element
// given as `as` (a <button> here) through the UI Shell's Link.
const Action = HeaderGlobalAction as unknown as ForwardRefExoticComponent<
  Omit<ComponentProps<typeof HeaderGlobalAction>, "onClick"> & {
    href?: string;
    onClick: (e: MouseEvent<HTMLElement>) => void;
    onBlur?: (e: FocusEvent<HTMLElement>) => void;
  } & RefAttributes<HTMLButtonElement>
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

interface Entry {
  Icon: CarbonIconType;
  /** The item's text in the panel, and its name there. */
  text: string;
  href?: string;
  onClick: (e: MouseEvent<HTMLElement>) => void;
}

/**
 * The four user actions, in their order, for both forms: Profile,
 * Keyboard shortcuts, the light/dark switch and Log out.
 */
function useEntries(profile: () => void, shortcuts: () => void, logout: () => void): Entry[] {
  const next = useTheme() === "dark" ? "light" : "dark";
  return [
    {
      Icon: UserAvatar,
      text: t.profile,
      href: "/?view=profile",
      onClick: (e) => {
        // A Ctrl/Cmd, Shift or Alt click is left to the browser (new tab or window).
        if (!isPlainClick(e)) return;
        e.preventDefault();
        profile();
      },
    },
    { Icon: Keyboard, text: t.shortcuts, onClick: shortcuts },
    { Icon: themeIcon(next), text: t.switchTheme(next), onClick: () => setTheme(next) },
    { Icon: Logout, text: t.logOut, onClick: logout },
  ];
}

interface ActionsProps {
  name: string;
  shortcutsRef: RefObject<HTMLButtonElement | null>;
  profile: () => void;
  shortcuts: () => void;
  logout: () => void;
}

/**
 * The header's user actions as icons with tooltips (#488). App renders
 * these where the bar shows icons (breakpoints.ts, headerActions: a wide
 * window with hover), else UserPanel. Each has an explicit aria-label, the
 * same text as Carbon's tooltip, which opens on hover and on keyboard focus;
 * Profile's names the user. The theme's and Log out's tooltips are aligned
 * to their end, so they stay in the window.
 */
export function HeaderActions({ name, shortcutsRef, profile, shortcuts, logout }: ActionsProps) {
  return useEntries(profile, shortcuts, logout).map(({ Icon, text, href, onClick }, i) => (
    <Action
      key={i}
      ref={i === 1 ? shortcutsRef : undefined}
      href={href}
      aria-label={i ? text : t.profileOf(name)}
      tooltipAlignment={i > 1 ? "end" : undefined}
      onClick={onClick}
    >
      <Icon size={20} />
    </Action>
  ));
}

/**
 * The header's user action and its panel (Carbon UI Shell), where the bar
 * shows no icons (phones, touch screens, narrow windows): the user name,
 * Profile, Keyboard shortcuts, the light/dark switch and Log out, each with
 * its icon before its text. The open state, focusing the first item on
 * open and Escape are App's (its key listener runs first); the panel's
 * content is mounted only while open, so the collapsed panel holds nothing
 * to tab to.
 */
export function UserPanel({ name, id, open, actionRef, panelRef, toggle, close, profile, shortcuts, logout }: Props) {
  const entries = useEntries(profile, shortcuts, logout);
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
            {/* Flat, not in fragments: Switcher gives only its direct items their focus handling. */}
            {entries.flatMap(({ Icon, text, href, onClick }, i) => [
              i === 3 && <SwitcherDivider key="divider" />,
              <Item key={i} as={href ? undefined : "button"} href={href} aria-label={text} onClick={onClick}>
                {/* The icon before the text (#488); the item's name stays its text. */}
                <span className="app-user-panel__item">
                  <Icon size={16} />
                  <span>{text}</span>
                </span>
              </Item>,
            ])}
          </Switcher>
        )}
      </Panel>
    </>
  );
}
