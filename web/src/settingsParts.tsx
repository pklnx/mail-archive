import { useLayoutEffect, useMemo, useRef, type MouseEvent, type ReactNode, type RefObject } from "react";
import { Breadcrumb, BreadcrumbItem, FeatureFlags, Link, OverflowMenu, Tile } from "@carbon/react";
import { isPlainClick } from "./clicks";
import { t } from "./i18n";

// The parts the settings pages (Accounts, Users, Retention) share: their
// lists are tables with a link per row to the entry's detail page (#285).
// Only these pages import this module, so it is not in the initial bundle.

/** A click handler for an in-app link: a plain click goes; Ctrl/Cmd click and the like are the browser's. */
const plain = (go: () => void) => (e: MouseEvent) => {
  if (!isPlainClick(e)) return;
  e.preventDefault();
  go();
};

/** A link to a place in the app: a real href, so it opens in a new tab too. */
export function AppLink({ href, go, className, children }: { href: string; go: () => void; className?: string; children: ReactNode }) {
  return (
    <Link href={href} className={className} onClick={plain(go)}>
      {children}
    </Link>
  );
}

/**
 * The head of a detail page: a breadcrumb back to the list, the title as the
 * page's h1 (the focus target of a page change) with its tags, the primary
 * actions and the overflow menu, and below them a helper line (children).
 * With both, the buttons wrap as a group and the menu stays at the row end,
 * on the line of the last button (#369).
 */
export function DetailHeader({
  parent,
  title,
  heading,
  tags,
  actions,
  menu,
  children,
}: {
  parent: { label: string; href: string; go: () => void };
  title: string;
  heading: RefObject<HTMLHeadingElement | null>;
  tags?: ReactNode;
  actions?: ReactNode;
  menu?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <>
      <Breadcrumb aria-label={t.carbon.breadcrumb} noTrailingSlash className="page__breadcrumb">
        <BreadcrumbItem href={parent.href} onClick={plain(parent.go)}>
          {parent.label}
        </BreadcrumbItem>
        <BreadcrumbItem isCurrentPage>{title}</BreadcrumbItem>
      </Breadcrumb>
      <header className="page-header">
        <div className="page-header__row">
          <div className="app-detail-title">
            <h1 ref={heading} tabIndex={-1} className="type-heading-04 app-page-title app-break-words">
              {title}
            </h1>
            {tags && <div className="app-detail-title__tags">{tags}</div>}
          </div>
          {(actions || menu) && (
            <div className={actions && menu ? "page-header__actions page-header__actions--menu" : "page-header__actions"}>
              {actions && menu ? <div className="page-header__buttons">{actions}</div> : actions}
              {menu}
            </div>
          )}
        </div>
        {children}
      </header>
    </>
  );
}

/** A row of tiles with a label and a value each: the summary of a detail page. */
export function SummaryTiles({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <dl className="app-summary">
      {items.map((item) => (
        <Tile key={item.label} className="app-summary__tile">
          <dt className="app-text-secondary app-text-small">{item.label}</dt>
          <dd className="app-summary__value">{item.value}</dd>
        </Tile>
      ))}
    </dl>
  );
}

/**
 * The overflow menu of a table row or a detail page. register keeps its
 * element (useMenuButtons), so the focus can go back to its button after a
 * confirmation that a menu item opened.
 */
export function RowMenu({ label, register, children }: { label: string; register?: (el: HTMLElement | null) => void; children: ReactNode }) {
  return (
    // The OverflowMenu of Carbon v12 (on Menu): the older one adds two tab
    // stops named "Focus sentinel" to every open menu.
    <FeatureFlags enableV12Overflowmenu>
      <OverflowMenu
        ref={register}
        label={label}
        size="lg"
        menuAlignment="bottom-end"
        tooltipAlignment="left"
        // Carbon keeps a mouse click from focusing the trigger, so the menu
        // would hand the focus back to whatever had it before (the page's h1,
        // say), and so would a confirmation opened from the menu. Focused
        // first, the trigger gets it back.
        onClickCapture={(e: MouseEvent<HTMLElement>) => {
          const trigger = e.currentTarget.querySelector<HTMLButtonElement>(".cds--overflow-menu");
          if (trigger?.contains(e.target as Node)) trigger.focus();
        }}
      >
        {children}
      </OverflowMenu>
    </FeatureFlags>
  );
}

/**
 * The menus of a page by key (a row's name), and their buttons. register
 * gives one ref callback per key for good: a new callback on each render
 * would make React detach the ref before a closing Modal asks for the button
 * (DangerModal returnFocus), and the button would not be found.
 */
export function useMenuButtons() {
  return useMemo(() => {
    const menus = new Map<string, HTMLElement>();
    const refs = new Map<string, (el: HTMLElement | null) => void>();
    return {
      register: (key: string) => {
        let ref = refs.get(key);
        if (!ref) {
          ref = (el) => {
            if (el) menus.set(key, el);
            else menus.delete(key);
          };
          refs.set(key, ref);
        }
        return ref;
      },
      button: (key: string) => menus.get(key)?.querySelector<HTMLButtonElement>("button") ?? null,
    };
  }, []);
}

/**
 * Moves the focus to the page's h1 when the page changes inside the app (a
 * link, Back or Forward, the end of an action), and never on the first page
 * shown, the one of a direct URL or a reload. page names the page shown; null
 * while it is not decided yet (the list is loading).
 */
export function usePageFocus(page: string | null, heading: RefObject<HTMLElement | null>) {
  const shown = useRef<string | null>(null);
  useLayoutEffect(() => {
    if (page === null) return;
    const before = shown.current;
    shown.current = page;
    if (before !== null && before !== page) heading.current?.focus();
  }, [page, heading]);
}
