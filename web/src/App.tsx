import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent } from "react";
import { Button, Header, HeaderGlobalBar, HeaderMenuButton, HeaderName, IconButton, InlineNotification, Popover, PopoverContent, Search } from "@carbon/react";
import { ArrowLeft, ChevronDown, ChevronUp, Filter, Folders } from "@carbon/icons-react";
import { isPlainClick } from "./clicks";
import { dialogOpen, ownedByWidget } from "./dialogs";
import { HealthBanner } from "./HealthBanner";
import { MessageList } from "./MessageList";
import { MessageView } from "./MessageView";
import { ActiveFilters, QuickFilters, editTarget, type FocusRequest } from "./SearchFilters";
import { hasPrefix, searchFilter } from "./searchSyntax";
import { SidebarPanel, drawerFocusTarget } from "./Sidebar";
import { ShellNav } from "./ShellNav";
import { UserPanel } from "./UserPanel";
import { lg, twoPane, useMediaQuery } from "./breakpoints";
import { noList, pager, type ListNav } from "./pager";
import { ProductMark } from "./ProductMark";
import { useAccounts } from "./useAccounts";
import { useFrameFocusClose } from "./useFrameFocusClose";
import { useCollapsed } from "./sidebarState";
import { railWidth, useSidebarLayout } from "./sidebarLayout";
import { useViewState } from "./urlState";
import { t } from "./i18n";
import { LazyView, lazyNamed } from "./lazyView";
import type { User } from "./api";

// The non-mail views and the shortcuts overview load on demand (#288); the
// mail view stays in the initial bundle. scripts/lazyChunks.mjs lists these
// chunks for the bundle check and the e2e suite.
const AccountsPage = lazyNamed(() => import("./AccountsPage"), "AccountsPage");
const UsersPage = lazyNamed(() => import("./UsersPage"), "UsersPage");
const RetentionPage = lazyNamed(() => import("./RetentionPage"), "RetentionPage");
const LogPage = lazyNamed(() => import("./LogPage"), "LogPage");
const ProfilePage = lazyNamed(() => import("./Profile"), "ProfilePage");
const ShortcutsModal = lazyNamed(() => import("./ShortcutsModal"), "ShortcutsModal");
// The filter form with its date picker (#286), mounted while its popover is open.
const FilterPanel = lazyNamed(() => import("./FilterPanel"), "FilterPanel");

interface Props {
  user: User;
  logout: () => void;
  /** Why the last Log out failed ("" if it did not): shown as an alert under the header. */
  logoutError: string;
}

export function App({ user, logout, logoutError }: Props) {
  const [state, update] = useViewState();
  const [query, setQuery] = useState(state.q);
  // Below lg the shell's SideNav (menuOpen) and the folder column
  // (foldersOpen) open as overlays, never both. From lg up both are always
  // on screen, the SideNav as Carbon's rail.
  const wide = useMediaQuery(lg);
  const [menuOpen, setMenuOpen] = useState(false);
  const [foldersOpen, setFoldersOpen] = useState(false);
  useEffect(() => {
    if (wide) {
      setMenuOpen(false);
      setFoldersOpen(false);
    }
  }, [wide]);
  // The filter button opens the filter form in a Carbon Popover; the set
  // filters show as tags either way. It is marked while one is set.
  const prefixed = hasPrefix(query) || state.group === "1";
  // Closed to start with on every width (maintainer decision on #286; it replaces #195's open panel).
  const [filtersOpen, setFiltersOpen] = useState(false);
  const filtersId = useId();
  const filtersButton = useRef<HTMLButtonElement>(null);
  // Whether the form's calendar is open right now (FilterPanel sets it while
  // mounted). Flatpickr's calendar lies outside the popover and overlaps
  // the date field, so a click into the field can end on the calendar: its
  // click event then targets body, and Carbon's outside click would close
  // the popover. A real outside click closes the calendar on mousedown first.
  const calendarOpen = useRef<() => boolean>(() => false);
  // Phones (one pane): the yes/no filters are one-tap quick filters and
  // have no tag of their own. Follows rotation.
  const phone = !useMediaQuery(twoPane);
  // "Edit filters" opens the panel and asks it to focus the first visible
  // tag's field; focusDone is the last request the panel has handled.
  const [focusRequest, setFocusRequest] = useState<FocusRequest>({ field: "from", n: 0 });
  const [focusDone, setFocusDone] = useState(0);
  const editFilters = () => {
    const field = editTarget(query, state.group === "1", phone);
    if (!field) return;
    // Its click does not reach Carbon's outside click listeners (ActiveFilters),
    // so the user panel's would not close it either.
    setUserOpen(false);
    setFiltersOpen(true);
    setFocusRequest((r) => ({ field, n: r.n + 1 }));
  };
  const [shortcuts, setShortcuts] = useState(false);
  // Where the focus goes back to when the overview closes: captured when the
  // user asks for it, before its chunk loads.
  const shortcutsOpener = useRef<Element | null>(null);
  const openShortcuts = () => {
    if (!shortcuts) shortcutsOpener.current = document.activeElement;
    setShortcuts(true);
  };
  const accounts = useAccounts();
  const collapsed = useCollapsed(user.name, accounts.data);
  const layout = useSidebarLayout(user.name);
  const asideId = useId();
  const navId = useId();
  // The header's user panel (UserPanel.tsx). Its state is here: the key
  // listener below needs it, and it hands over to and from the SideNav.
  const [userOpen, setUserOpen] = useState(false);
  const userId = useId();
  const userAction = useRef<HTMLButtonElement>(null);
  const userPanel = useRef<HTMLDivElement>(null);
  const closeUser = (returnFocus: boolean) => {
    setUserOpen(false);
    if (returnFocus) userAction.current?.focus();
  };
  // A click into the mail frame reaches neither popover's own close (#349).
  useFrameFocusClose(userOpen, () => closeUser(false));
  useFrameFocusClose(filtersOpen, () => setFiltersOpen(false));

  // Follow the URL when it changes from outside (back button).
  useEffect(() => setQuery(state.q), [state.q]);

  // Search as you type, without a history entry per keystroke. Only the
  // query changes here; an open message stays open.
  const typed = query.trim() ? query : "";
  useEffect(() => {
    if (typed === state.q) return;
    const t = window.setTimeout(() => update({ q: typed }, { replace: true }), 300);
    return () => window.clearTimeout(t);
  }, [typed, state.q, update]);

  const filter = {
    ...searchFilter(state.q),
    account: state.account,
    folder: state.folder,
    group: state.group === "1",
    gone: state.gone === "1",
  };
  const reading = state.m !== "";
  const page =
    state.view === "profile" || state.view === "log" || state.view === "accounts" || (user.admin && (state.view === "users" || state.view === "retention"));
  const mailView = !page;

  // Previous and next message in list order. Paging replaces the history
  // entry, so Back still returns to the list.
  const [nav, setNav] = useState<ListNav>(noList);
  const onNav = useCallback((n: ListNav) => setNav(n), []);
  const pos = pager(nav, state.m);
  const [advance, setAdvance] = useState(false);
  const go = useCallback((m: string) => update({ m }, { replace: true }), [update]);
  const next = () => {
    if (pos.next) go(pos.next);
    else if (pos.nextNeedsMore) {
      setAdvance(true);
      nav.loadMore();
    }
  };
  // After "next" at the end of a page: move on once the next page is there.
  useEffect(() => {
    if (!advance) return;
    const p = pager(nav, state.m);
    if (p.next) {
      setAdvance(false);
      go(p.next);
    } else if (!p.nextNeedsMore) setAdvance(false);
  }, [advance, nav, state.m, go]);

  // The list's title on phones: where the user is.
  const place =
    state.gone === "1"
      ? { title: t.onlyInArchive, sub: "" }
      : state.folder
        ? { title: state.folder, sub: state.account }
        : { title: state.account || t.allMail, sub: "" };
  const setGroup = (on: boolean) => update({ group: on ? "1" : "" }, { replace: true });
  const show = (view: string) => {
    update({ view, m: "" });
    setMenuOpen(false);
    setFoldersOpen(false);
  };
  // The folder overlay belongs to the mail view.
  useEffect(() => {
    if (!mailView) setFoldersOpen(false);
  }, [mailView]);

  // The overlays: focus moves into them and back to where it was, and the
  // rest of the page is inert meanwhile (below), so Tab stays inside. The
  // header stays usable next to the SideNav, so its menu button closes it.
  // The folder overlay is modal: the header is inert too, so nothing behind
  // it opens another overlay.
  const aside = useRef<HTMLElement>(null);
  const sideNav = useRef<HTMLElement>(null);
  const opener = useRef<HTMLElement | null>(null);
  const overlay = menuOpen ? "menu" : foldersOpen ? "folders" : "";
  useEffect(() => {
    if (overlay) {
      opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      const target = overlay === "menu" ? sideNav.current?.querySelector<HTMLElement>("a[href]") : drawerFocusTarget(aside.current);
      target?.focus();
    } else if (opener.current) {
      opener.current.focus();
      opener.current = null;
    }
  }, [overlay]);
  // The user panel: focus moves to its first item when it opens. Declared
  // after the overlay effect, so it runs after that one's focus return when
  // the panel replaces the SideNav (a child's effect would run before it).
  useEffect(() => {
    if (userOpen) userPanel.current?.querySelector<HTMLElement>(".cds--switcher__item-link")?.focus();
  }, [userOpen]);
  const hide = overlay !== "" || undefined;
  const closeFolders = useCallback(() => setFoldersOpen(false), []);
  // Escape closes the folder overlay. In the capture phase: Carbon's
  // TreeView keeps the keys it gets from bubbling.
  const foldersKey = (e: ReactKeyboardEvent) => {
    if (foldersOpen && e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      closeFolders();
    }
  };

  // Keyboard shortcuts (? lists them). In the capture phase, so they also
  // work while a folder in Carbon's TreeView has focus: the tree keeps its
  // keys from bubbling. That is before any component sees a key, so the
  // keys that belong to one are left alone explicitly (ownedByWidget):
  // fields (Carbon's Search included), open popovers, menus and dialogs,
  // Escape for an open tooltip and in the SideNav. Off while a dialog or an
  // overlay is open; the SideNav overlay closes on Escape.
  const search = useRef<HTMLInputElement>(null);
  const chord = useRef(0);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey || dialogOpen()) return;
      // The overview is not a dialog yet while its chunk loads, or if it
      // could not load (the notice then stands in for it): Escape closes it.
      if (shortcuts && e.key === "Escape") {
        setShortcuts(false);
        return;
      }
      if (menuOpen) {
        if (e.key === "Escape") setMenuOpen(false);
        return;
      }
      // Before ownedByWidget: an open tooltip of the panel's action would claim the Escape.
      if (userOpen) {
        if (e.key === "Escape") {
          e.preventDefault();
          closeUser(true);
        }
        return;
      }
      if (ownedByWidget(e)) {
        // Escape leaves a field or a select, as before; Carbon's Search clears itself first (below).
        const el = e.target instanceof HTMLElement ? e.target : null;
        // Not in the filter popover: Escape there closes its calendar, then the popover (FilterPanel.tsx).
        if (e.key === "Escape" && el && ["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName) && !el.closest(".cds--search, .cds--popover-content")) el.blur();
        return;
      }
      const mail = state.view === "";
      const afterG = Date.now() - chord.current < 1000;
      chord.current = 0;
      switch (e.key) {
        case "/":
          if (mail) {
            e.preventDefault();
            search.current?.focus();
          }
          break;
        case "?":
          openShortcuts();
          break;
        case "j":
          if (mail && reading) next();
          else if (mail && nav.ids[0]) update({ m: nav.ids[0] });
          break;
        case "k":
          if (mail && pos.prev) go(pos.prev);
          break;
        case "u":
        case "Escape":
          if (mail && reading) update({ m: "" });
          break;
        case "g":
          chord.current = Date.now();
          break;
        case "a":
          if (afterG) update({ account: "", folder: "", gone: "", m: "", view: "" });
          break;
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  });

  // Each message starts at its top: j/k and the conversation would open
  // the next one scrolled as far as the last (the pane, or the page on phones).
  const readerScroll = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (readerScroll.current) readerScroll.current.scrollTop = 0;
    // The page scrolls only on phones; on desktop this is a no-op today.
    if (document.scrollingElement) document.scrollingElement.scrollTop = 0;
  }, [state.m]);

  // A failed Log out: on phones the page may be scrolled down (a message is
  // open), so bring the alert under the header into view.
  useEffect(() => {
    if (logoutError && document.scrollingElement) document.scrollingElement.scrollTop = 0;
  }, [logoutError]);

  const gone = accounts.data?.accounts.reduce((n, a) => n + a.goneMessages, 0) ?? 0;
  const pageInert = hide;

  return (
    // While a message is open on a phone the page scrolls, not a box inside it (#200; layout.css).
    <div className={`app-shell${reading && mailView ? " app-shell--page-scroll" : ""}`}>
      {/* Usable next to the SideNav overlay (its menu button closes it); inert behind the folder overlay. */}
      {/* Header passes inert on to its <header>; its types only lack the attribute. */}
      <Header aria-label={t.appName} {...({ inert: foldersOpen || undefined } as object)}>
        {/* Below lg (Carbon's breakpoint) it opens the SideNav; Carbon hides it from lg up. */}
        <HeaderMenuButton
          aria-label={menuOpen ? t.hideMenu : t.showMenu}
          aria-controls={navId}
          aria-expanded={menuOpen}
          isActive={menuOpen}
          // Never from behind the folder overlay (inert there anyway): one overlay does not open another.
          onClick={() => {
            if (foldersOpen) return;
            // The SideNav replaces the user panel; its overlay effect moves the focus.
            setUserOpen(false);
            setMenuOpen((open) => !open);
          }}
        />
        <HeaderName
          className="app-header__name"
          href="/"
          prefix=""
          onClick={(e: MouseEvent) => {
            // All mail, as "g a" does. A Ctrl/Cmd, Shift or Alt click is left
            // to the browser, so it still opens "/" in a new tab or window.
            if (!isPlainClick(e)) return;
            e.preventDefault();
            update({ account: "", folder: "", gone: "", m: "", view: "" });
            setMenuOpen(false);
          }}
        >
          <ProductMark className="app-header__logo" />
          {t.appName}
        </HeaderName>
        <HeaderGlobalBar>
          <UserPanel
            name={user.name}
            id={userId}
            open={userOpen}
            actionRef={userAction}
            panelRef={userPanel}
            toggle={() => {
              if (userOpen) return closeUser(true);
              // The panel replaces the SideNav: no focus return to the menu button.
              opener.current = null;
              setMenuOpen(false);
              setUserOpen(true);
            }}
            close={closeUser}
            profile={() => {
              closeUser(true);
              show("profile");
            }}
            shortcuts={() => {
              closeUser(true);
              shortcutsOpener.current = userAction.current;
              setShortcuts(true);
            }}
            logout={() => {
              // Close the panel first, so on a phone it does not cover a failure alert.
              closeUser(true);
              logout();
            }}
          />
        </HeaderGlobalBar>
      </Header>
      <ShellNav
        // Carbon keeps its hover state across a change of isRail: a rail expanded by
        // hover or focus would stay an inert overlay when the window narrows below lg.
        key={wide ? "rail" : "overlay"}
        ref={sideNav}
        id={navId}
        state={state}
        admin={user.admin}
        gone={gone}
        rail={wide}
        expanded={menuOpen && !wide}
        onClose={() => setMenuOpen(false)}
        go={(patch) => {
          update(patch);
          setFoldersOpen(false);
        }}
      />
      <div className="app-shell__body">
      <div inert={pageInert}>
        {logoutError && (
          <InlineNotification
            kind="error"
            lowContrast
            hideCloseButton
            role="alert"
            statusIconDescription={t.carbon.status.error}
            subtitle={logoutError}
          />
        )}
        <HealthBanner
          data={accounts.data}
          admin={user.admin}
          openAccounts={() => update({ view: "accounts", m: "" })}
          openUsers={() => update({ view: "users", m: "" })}
        />
      </div>
      {state.view === "profile" ? (
        <main inert={pageInert} className="app-view">
          <LazyView key={state.view}>
            <ProfilePage name={user.name} />
          </LazyView>
        </main>
      ) : state.view === "users" && user.admin ? (
        <main inert={pageInert} className="app-view">
          <LazyView key={state.view}>
            <UsersPage />
          </LazyView>
        </main>
      ) : state.view === "retention" && user.admin ? (
        <main inert={pageInert} className="app-view">
          <LazyView key={state.view}>
            <RetentionPage />
          </LazyView>
        </main>
      ) : state.view === "log" ? (
        <main inert={pageInert} className="app-view">
          <LazyView key={state.view}>
            <LogPage admin={user.admin} open={(m) => update({ view: "", m })} />
          </LazyView>
        </main>
      ) : state.view === "accounts" ? (
        <main inert={pageInert} className="app-view">
          <LazyView key={state.view}>
            <AccountsPage
              admin={user.admin}
              accounts={accounts}
              renamed={(from, to) => {
                collapsed.rename(from, to);
                if (state.account === from) update({ account: to }, { replace: true });
              }}
              showGone={(account) => update({ account, folder: "", gone: "1", m: "", view: "" })}
            />
          </LazyView>
        </main>
      ) : (
        <div
          className={`app-view mail-view${reading ? " mail-view--reading" : ""}`}
          // Only the lg template uses it; no transition, so nothing animates on load or while dragging.
          style={{ "--sidebar-w": `${layout.collapsed ? railWidth : layout.width}rem` } as CSSProperties}
        >
          {/* Folder column from lg up; below lg an overlay, a modal dialog. */}
          <aside
            ref={aside}
            id={asideId}
            className={foldersOpen ? "folder-pane folder-pane--overlay" : "folder-pane"}
            inert={menuOpen || undefined}
            role={foldersOpen ? "dialog" : undefined}
            aria-modal={foldersOpen || undefined}
            aria-label={foldersOpen ? t.navFolders : undefined}
            onKeyDownCapture={foldersKey}
          >
            <SidebarPanel
              layout={layout}
              controls={asideId}
              state={state}
              accounts={accounts}
              collapsed={collapsed}
              select={(account, folder) => {
                update({ account, folder, gone: "", m: "", view: "" });
                setFoldersOpen(false);
              }}
              show={show}
              onClose={closeFolders}
            />
          </aside>
          {foldersOpen && <div aria-hidden className="folder-pane__scrim" onClick={closeFolders} />}

          <section inert={pageInert} className="mail-pane mail-pane--list">
            {/* Phones: where the user is, the search below at full width. Menu
                and the user panel are in the header. */}
            <div className="mail-place">
              <h1 className="mail-place__title">{place.title}</h1>
              {place.sub && <p className="mail-place__sub">{place.sub}</p>}
            </div>
            <div className="mail-toolbar">
              {/* Below lg: opens the folder column as an overlay. */}
              <div className="folder-pane__open">
                <IconButton
                  kind="ghost"
                  size="md"
                  align="bottom-start"
                  label={t.navFolders}
                  aria-haspopup="dialog"
                  aria-expanded={foldersOpen}
                  aria-controls={asideId}
                  // Never from behind the SideNav overlay (inert there anyway).
                  onClick={() => !menuOpen && setFoldersOpen(true)}
                >
                  <Folders />
                </IconButton>
              </div>
              <Search
                ref={search}
                size="md"
                labelText={t.searchMail}
                closeButtonLabelText={t.carbon.clearSearch}
                placeholder={
                  state.gone === "1" ? t.searchIn(t.onlyInArchive) : state.account ? t.searchIn(state.folder || state.account) : t.searchAll
                }
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                // Escape clears the field (Carbon); in an empty one it leaves the field.
                onKeyDown={(e) => {
                  if (e.key === "Escape" && !e.currentTarget.value) e.currentTarget.blur();
                }}
              />
              <Popover
                open={filtersOpen}
                align="bottom-end"
                autoAlign
                onRequestClose={() => !calendarOpen.current() && setFiltersOpen(false)}
              >
                <IconButton
                  ref={filtersButton}
                  kind="ghost"
                  size="md"
                  align="bottom-end"
                  label={t.filters}
                  className={prefixed ? "mail-toolbar__filters--set" : undefined}
                  aria-expanded={filtersOpen}
                  aria-controls={filtersId}
                  onClick={() => setFiltersOpen((open) => !open)}
                  // Carbon closes the popover on Escape only from inside its content.
                  onKeyDown={(e: ReactKeyboardEvent) => {
                    if (e.key === "Escape" && filtersOpen) {
                      e.preventDefault();
                      setFiltersOpen(false);
                    }
                  }}
                >
                  <Filter />
                </IconButton>
                <PopoverContent id={filtersId} className="filter-popover__content">
                  {/* PopoverContent is always in the DOM: mounted only while open, so the chunk loads on first open. */}
                  {filtersOpen && (
                    <LazyView inline>
                      <FilterPanel
                        id={filtersId}
                        query={query}
                        setQuery={setQuery}
                        group={state.group === "1"}
                        setGroup={setGroup}
                        focus={focusRequest.n > focusDone ? focusRequest : undefined}
                        onFocused={setFocusDone}
                        calendarOpen={calendarOpen}
                      />
                    </LazyView>
                  )}
                </PopoverContent>
              </Popover>
            </div>
            {phone && <QuickFilters query={query} setQuery={setQuery} group={state.group === "1"} setGroup={setGroup} />}
            <ActiveFilters
              query={query}
              setQuery={setQuery}
              group={state.group === "1"}
              setGroup={setGroup}
              quick={phone}
              onEdit={editFilters}
              fallbackFocus={filtersButton}
            />
            <div className="mail-pane__fill">
              <MessageList
                filter={filter}
                selected={state.m}
                onNav={onNav}
                clearSearch={() => {
                  setQuery("");
                  update({ q: "" }, { replace: true });
                }}
                searchEverywhere={state.account || state.gone === "1" ? () => update({ account: "", folder: "", gone: "" }) : undefined}
                // Commit a pending search first (same history entry), then open
                // the message as a new entry: the debounced update cannot close it
                // again, and Back returns to the search results.
                open={(m) => {
                  if (typed !== state.q) update({ q: typed }, { replace: true });
                  update({ m });
                }}
              />
            </div>
          </section>

          <main inert={pageInert} className="mail-pane mail-pane--reader">
            {reading ? (
              <>
                {/* Back on phones, previous and next everywhere (when the message is in the list). */}
                <div className={`reader-bar${pos.index < 0 ? " reader-bar--phone-only" : ""}`}>
                  <span className="reader-bar__back">
                    <IconButton kind="ghost" size="md" align="bottom-start" label={t.backToList} onClick={() => update({ m: "" })}>
                      <ArrowLeft />
                    </IconButton>
                  </span>
                  {pos.index >= 0 && (
                    <div className="reader-bar__pager">
                      <IconButton kind="ghost" size="md" align="bottom" label={t.previousMessage} disabled={!pos.prev} onClick={() => pos.prev && go(pos.prev)}>
                        <ChevronUp />
                      </IconButton>
                      <span className="reader-bar__position">{t.messagePosition(pos.index + 1, nav.ids.length, nav.more)}</span>
                      <IconButton kind="ghost" size="md" align="bottom-end" label={t.nextMessage} disabled={!pos.next && !pos.nextNeedsMore} onClick={next}>
                        <ChevronDown />
                      </IconButton>
                    </div>
                  )}
                </div>
                {/* From 48rem the one scroller of the message (layout.css); on phones the page scrolls.
                    A region in the tab order there, so the keyboard can scroll it. */}
                <div
                  ref={readerScroll}
                  className="mail-pane__fill reader-scroll"
                  {...(!phone && { tabIndex: 0, role: "region", "aria-label": t.readingPane })}
                >
                  <MessageView id={state.m} open={(m) => update({ m })} />
                </div>
              </>
            ) : (
              <div className="reader-empty">
                <ProductMark className="reader-empty__logo" />
                <p className="app-text-secondary">{t.selectMessage}</p>
                <Button kind="ghost" size="sm" className="reader-empty__shortcuts" onClick={openShortcuts}>
                  {t.shortcutsHint}&nbsp;<kbd className="kbd">?</kbd>
                </Button>
              </div>
            )}
          </main>
        </div>
      )}

      {shortcuts && (
        // No placeholder while it loads (a placeholder dialog would only flash); if it cannot
        // load, the notice floats in its place, and Escape or its close button closes it.
        <LazyView fallback={null} onClose={() => setShortcuts(false)}>
          <ShortcutsModal opener={shortcutsOpener.current} onClose={() => setShortcuts(false)} />
        </LazyView>
      )}
      </div>
    </div>
  );
}
