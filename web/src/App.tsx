import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent } from "react";
import { createPortal } from "react-dom";
import { Button, FeatureFlags, Header, HeaderGlobalAction, HeaderGlobalBar, HeaderMenuButton, HeaderName, IconButton, Modal, Search } from "@carbon/react";
import { ArrowLeft, ChevronDown, ChevronUp, Filter, Folders, Keyboard, Logout, UserAvatar } from "@carbon/icons-react";
import { AccountsPage } from "./AccountsPage";
import { isPlainClick } from "./clicks";
import { dialogOpen, ownedByWidget } from "./dialogs";
import { HealthBanner } from "./HealthBanner";
import { ProfilePage } from "./Profile";
import { UsersPage } from "./UsersPage";
import { MessageList } from "./MessageList";
import { LogPage } from "./LogPage";
import { RetentionPage } from "./RetentionPage";
import { MessageView } from "./MessageView";
import { ActiveFilters, SearchFilters } from "./SearchFilters";
import { hasPrefix, searchFilter } from "./searchSyntax";
import { SidebarPanel, drawerFocusTarget } from "./Sidebar";
import { ShellNav } from "./ShellNav";
import { ThemeToggle } from "./ThemeToggle";
import { lg, useMediaQuery } from "./breakpoints";
import { noList, pager, type ListNav } from "./pager";
import logo from "./logo.svg";
import { useAccounts } from "./useAccounts";
import { useCollapsed } from "./sidebarState";
import { railWidth, useSidebarLayout } from "./sidebarLayout";
import { useViewState } from "./urlState";
import { t } from "./i18n";
import type { User } from "./api";

interface Props {
  user: User;
  logout: () => void;
}

export function App({ user, logout }: Props) {
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
  // The filter button toggles the filter panel under the search; the set
  // filters show as tags either way. It is marked while one is set.
  const prefixed = hasPrefix(query) || state.group === "1";
  const [filtersOpen, setFiltersOpen] = useState(false);
  const filtersId = useId();
  const [shortcuts, setShortcuts] = useState(false);
  const accounts = useAccounts();
  const collapsed = useCollapsed(user.name, accounts.data);
  const layout = useSidebarLayout(user.name);
  const asideId = useId();
  const navId = useId();

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
      if (menuOpen) {
        if (e.key === "Escape") setMenuOpen(false);
        return;
      }
      if (ownedByWidget(e)) {
        // Escape leaves a field, as before; Carbon's Search clears itself first (below).
        const el = e.target instanceof HTMLElement ? e.target : null;
        if (e.key === "Escape" && el && ["INPUT", "TEXTAREA"].includes(el.tagName) && !el.closest(".cds--search")) el.blur();
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
          setShortcuts(true);
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

  const gone = accounts.data?.accounts.reduce((n, a) => n + a.goneMessages, 0) ?? 0;
  const pageInert = hide;

  return (
    <div className="app-shell">
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
          onClick={() => !foldersOpen && setMenuOpen((open) => !open)}
        />
        <HeaderName
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
          <img src={logo} alt="" className="app-header__logo" />
          {t.appName}
        </HeaderName>
        <HeaderGlobalBar>
          <span className="app-header__user" aria-hidden>
            {user.name}
          </span>
          <HeaderGlobalAction className="app-header__shortcuts" aria-label={t.shortcuts} onClick={() => setShortcuts(true)}>
            <Keyboard size={20} />
          </HeaderGlobalAction>
          <ThemeToggle header />
          <HeaderGlobalAction
            aria-label={t.profileOf(user.name)}
            isActive={state.view === "profile"}
            onClick={() => show("profile")}
          >
            <UserAvatar size={20} />
          </HeaderGlobalAction>
          <HeaderGlobalAction aria-label={t.logOut} tooltipAlignment="end" onClick={logout}>
            <Logout size={20} />
          </HeaderGlobalAction>
        </HeaderGlobalBar>
      </Header>
      <ShellNav
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
        <HealthBanner
          data={accounts.data}
          admin={user.admin}
          openAccounts={() => update({ view: "accounts", m: "" })}
          openUsers={() => update({ view: "users", m: "" })}
        />
      </div>
      {/* The views not converted to Carbon yet (#168 parts 5 and 6) carry
          .legacy each, for the reset in index.css; the converted markup
          (Accounts, the mail view) does not, so the reset never reaches
          Carbon's. */}
      {state.view === "profile" ? (
        <main inert={pageInert} className="legacy text-ink app-view">
          <ProfilePage name={user.name} close={() => update({ view: "" })} />
        </main>
      ) : state.view === "users" && user.admin ? (
        <main inert={pageInert} className="legacy text-ink app-view">
          <UsersPage close={() => update({ view: "" })} />
        </main>
      ) : state.view === "retention" && user.admin ? (
        <main inert={pageInert} className="legacy text-ink app-view">
          <RetentionPage close={() => update({ view: "" })} />
        </main>
      ) : state.view === "log" ? (
        <main inert={pageInert} className="legacy text-ink app-view">
          <LogPage admin={user.admin} close={() => update({ view: "" })} open={(m) => update({ view: "", m })} />
        </main>
      ) : state.view === "accounts" ? (
        <main inert={pageInert} className="app-view">
          <AccountsPage
            admin={user.admin}
            accounts={accounts}
            renamed={(from, to) => {
              collapsed.rename(from, to);
              if (state.account === from) update({ account: to }, { replace: true });
            }}
            showGone={(account) => update({ account, folder: "", gone: "1", m: "", view: "" })}
          />
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
                and theme are in the header. */}
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
              <IconButton
                kind="ghost"
                size="md"
                align="bottom-end"
                label={t.filters}
                className={prefixed ? "mail-toolbar__filters--set" : undefined}
                isSelected={filtersOpen}
                aria-expanded={filtersOpen}
                aria-controls={filtersId}
                onClick={() => setFiltersOpen((open) => !open)}
              >
                <Filter />
              </IconButton>
            </div>
            {filtersOpen && <SearchFilters id={filtersId} query={query} setQuery={setQuery} group={state.group === "1"} setGroup={setGroup} />}
            <ActiveFilters query={query} setQuery={setQuery} group={state.group === "1"} setGroup={setGroup} />
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
                <div className="mail-pane__fill">
                  <MessageView id={state.m} open={(m) => update({ m })} />
                </div>
              </>
            ) : (
              <div className="reader-empty">
                <img src={logo} alt="" className="reader-empty__logo" />
                <p className="app-text-secondary">{t.selectMessage}</p>
                <Button kind="ghost" size="sm" className="reader-empty__shortcuts" onClick={() => setShortcuts(true)}>
                  {t.shortcutsHint}&nbsp;<kbd className="kbd">?</kbd>
                </Button>
              </div>
            )}
          </main>
        </div>
      )}

      {shortcuts && <ShortcutsModal onClose={() => setShortcuts(false)} />}
      </div>
    </div>
  );
}

const shortcutKeys = () =>
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
 * and the focus goes back to where it was when it closes.
 */
function ShortcutsModal({ onClose }: { onClose: () => void }) {
  const opener = useRef<Element | null>(null);
  useLayoutEffect(() => {
    opener.current = document.activeElement;
    return () => {
      const el = opener.current;
      if (el instanceof HTMLElement && el.isConnected) el.focus();
    };
  }, []);
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
