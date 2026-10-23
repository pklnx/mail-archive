import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { applySettings, messagesFor, t } from "./i18n";
import { ShellNav } from "./ShellNav";
import type { ViewState } from "./urlState";

const view = (patch: Partial<ViewState> = {}): ViewState => ({ q: "", account: "", folder: "", group: "", gone: "", m: "", view: "", add: "", edit: "", import: "", ...patch });

function nav(props: Partial<Parameters<typeof ShellNav>[0]> = {}) {
  const go = vi.fn();
  const onClose = vi.fn();
  render(<ShellNav id="nav" state={view()} admin={false} gone={0} rail expanded={false} onClose={onClose} go={go} {...props} />);
  return { go, onClose, nav: screen.getByRole("navigation", { name: t.mainNavigation }) };
}

afterEach(cleanup);

const names = (el: HTMLElement) => within(el).getAllByRole("link").map((l) => l.textContent);

describe("ShellNav", () => {
  it("shows Users and Retention only to admins", () => {
    const user = nav().nav;
    expect(names(user)).toEqual([t.allMail, t.navAccounts, t.log, t.profile]);
    cleanup();
    const admin = nav({ admin: true }).nav;
    expect(names(admin)).toEqual([t.allMail, t.navAccounts, t.users, t.log, t.retention, t.profile]);
  });

  it("is Carbon's rail from lg up and an overlay below", () => {
    const rail = nav().nav;
    expect(rail.className).toContain("cds--side-nav--rail");
    cleanup();
    const closed = nav({ rail: false }).nav;
    expect(closed.className).not.toContain("cds--side-nav--rail");
    expect(closed.className).not.toContain("cds--side-nav--expanded");
    // Carbon keeps a closed overlay out of the tab order.
    expect(closed.hasAttribute("inert")).toBe(true);
    cleanup();
    const open = nav({ rail: false, expanded: true }).nav;
    expect(open.className).toContain("cds--side-nav--expanded");
    expect(open.hasAttribute("inert")).toBe(false);
  });

  it("closes the overlay with a click on Carbon's scrim", () => {
    const { onClose } = nav({ rail: false, expanded: true });
    fireEvent.click(document.querySelector(".cds--side-nav__overlay-active")!);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("links each section with a real href and goes there on a plain click", () => {
    const { go, onClose, nav: el } = nav({ admin: true });
    const accounts = within(el).getByRole("link", { name: t.navAccounts });
    expect(accounts.getAttribute("href")).toBe("?view=accounts");
    expect(within(el).getByRole("link", { name: t.allMail }).getAttribute("href")).toBe("/");
    // A plain click stays in the app.
    expect(fireEvent.click(accounts)).toBe(false);
    expect(go).toHaveBeenCalledWith({ account: "", folder: "", gone: "", m: "", view: "accounts" });
    expect(onClose).toHaveBeenCalledTimes(1);
    // Ctrl-click is left to the browser (a new tab).
    expect(fireEvent.click(accounts, { ctrlKey: true })).toBe(true);
    expect(go).toHaveBeenCalledTimes(1);
  });

  it("marks the current section", () => {
    const { nav: el } = nav({ state: view({ view: "log" }) });
    expect(within(el).getByRole("link", { name: t.log }).getAttribute("aria-current")).toBe("page");
    expect(within(el).getByRole("link", { name: t.allMail }).getAttribute("aria-current")).toBeNull();
  });

  it("shows Only in archive with its count and hint while there is such mail", () => {
    const { go, nav: el } = nav({ gone: 3 });
    const link = within(el).getByRole("link", { name: `${t.onlyInArchive} 3` });
    expect(link.getAttribute("title")).toBe(t.onlyInArchiveHint);
    expect(link.getAttribute("href")).toBe("?gone=1");
    fireEvent.click(link);
    expect(go).toHaveBeenCalledWith({ account: "", folder: "", gone: "1", m: "", view: "" });
    cleanup();
    // Still there while it is open, so the current place does not vanish.
    const open = nav({ gone: 0, state: view({ gone: "1" }) }).nav;
    expect(within(open).getByRole("link", { name: `${t.onlyInArchive} 0` }).getAttribute("aria-current")).toBe("page");
    expect(within(open).getByRole("link", { name: t.allMail }).getAttribute("aria-current")).toBeNull();
    cleanup();
    expect(names(nav({ gone: 0 }).nav)).not.toContain(`${t.onlyInArchive} 0`);
  });

  it("speaks German", () => {
    applySettings({ language: "de", locale: "de" });
    try {
      const de = messagesFor("de");
      const el = nav({ admin: true, gone: 2 }).nav;
      expect(el.getAttribute("aria-label")).toBe(de.mainNavigation);
      expect(names(el)).toEqual([de.allMail, `${de.onlyInArchive} 2`, de.navAccounts, de.users, de.log, de.retention, de.profile]);
      expect(document.body.innerHTML).not.toMatch(/All mail|Accounts|Retention|Profile|Toggle/);
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });
});
