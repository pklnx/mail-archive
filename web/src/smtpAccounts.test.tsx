import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Account, AccountsResponse } from "./api";
import { addressList } from "./accountParts";
import { AccountsPage } from "./AccountsPage";
import { t } from "./i18n";
import { findAlert } from "./test/alerts";

function smtpAccount(more: Partial<Account> = {}): Account {
  return {
    name: "journal", kind: "smtp", enabled: true, removed: false, purging: false, purge: null, host: "", port: 0, tls: "tls", username: "",
    includedFolders: [], excludedFolders: [], syncInterval: "default", folders: [], goneMessages: 0, lastReconciledAt: null,
    addresses: ["archive@archive.local"], lastReceivedAt: null, fill: null, fills: [], firstReceived: null, sharedWith: [],
    sync: { state: "idle", lastRun: null, failureStreak: 0, failingSince: null, health: "ok", syncEvery: null, nextAt: null },
    ...more,
  };
}

const data = (accounts: Account[] = [], smtp: AccountsResponse["smtp"] = {}): AccountsResponse => ({
  accounts, manage: true, shared: [], syncInterval: "6h0m0s", syncIntervalMin: "5m0s", reconcileInterval: "", alertAfter: 3, smtp,
});

let requests: { method: string; url: string; body: unknown }[];
let answer: () => Response;

beforeEach(() => {
  requests = [];
  answer = () => new Response(JSON.stringify({ name: "journal" }), { status: 201, headers: { "Content-Type": "application/json" } });
  vi.spyOn(globalThis, "fetch").mockImplementation(async (url, init) => {
    requests.push({ method: init?.method ?? "GET", url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined });
    return answer();
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  // The pages under Accounts live in the URL.
  window.history.replaceState(null, "", "/");
});

function page(admin: boolean, d = data()) {
  const reload = vi.fn();
  window.history.replaceState(null, "", "/?view=accounts");
  render(<AccountsPage accounts={{ data: d, error: "", reload }} admin={admin} renamed={() => {}} showGone={() => {}} showAccount={() => {}} />);
  return reload;
}

describe("adding an smtp account", () => {
  it("offers the source only to admins", () => {
    page(false);
    fireEvent.click(screen.getByRole("button", { name: t.addAccount }));
    expect(screen.queryByText(t.sourceChoice)).toBeNull();
    expect(screen.getByLabelText(t.fieldHost)).toBeTruthy();
  });

  it("creates the account and shows the mail server setup", async () => {
    const reload = page(true, data([], null));
    fireEvent.click(screen.getByRole("button", { name: t.addAccount }));
    fireEvent.click(screen.getByLabelText(new RegExp(t.sourceSMTP)));
    expect(screen.getByText(new RegExp("The SMTP server is off|Der SMTP-Server ist aus"))).toBeTruthy();
    fireEvent.change(screen.getByLabelText(t.fieldName), { target: { value: "journal" } });
    fireEvent.change(screen.getByLabelText(`${t.fieldAddresses} 1`), { target: { value: "Archive@Archive.local" } });
    fireEvent.click(screen.getByRole("button", { name: t.addAddress }));
    fireEvent.change(screen.getByLabelText(`${t.fieldAddresses} 2`), { target: { value: "  " } });
    fireEvent.click(screen.getByRole("button", { name: t.save }));
    expect(await screen.findByText(t.smtpAccountAdded("journal"))).toBeTruthy();
    expect(requests).toEqual([{ method: "POST", url: "/api/accounts", body: { kind: "smtp", name: "journal", addresses: ["Archive@Archive.local"] } }]);
    expect(reload).toHaveBeenCalled();
    expect(screen.getByText(/archive\.local\s+smtp:\[127\.0\.0\.1\]:2525/)).toBeTruthy();
    expect(screen.queryByText(t.sourceChoice)).toBeNull();
  });

  it("shows the server's error, which names a taken address", async () => {
    answer = () => new Response(JSON.stringify({ error: "x@archive.local: another smtp account receives for this address" }), { status: 409 });
    page(true);
    fireEvent.click(screen.getByRole("button", { name: t.addAccount }));
    fireEvent.click(screen.getByLabelText(new RegExp(t.sourceSMTP)));
    fireEvent.change(screen.getByLabelText(t.fieldName), { target: { value: "journal" } });
    fireEvent.change(screen.getByLabelText(`${t.fieldAddresses} 1`), { target: { value: "x@archive.local" } });
    fireEvent.click(screen.getByRole("button", { name: t.save }));
    expect((await findAlert()).textContent).toContain("x@archive.local");
  });
});

describe("the smtp account's page", () => {
  function detail(admin: boolean, more: Partial<Account> = {}) {
    answer = () => new Response(null, { status: 204 });
    window.history.replaceState(null, "", "/?view=accounts&id=journal");
    render(<AccountsPage accounts={{ data: data([smtpAccount(more)]), error: "", reload: () => {} }} admin={admin} renamed={() => {}} showGone={() => {}} showAccount={() => {}} />);
  }

  it("lets the owner rename but not change addresses", async () => {
    detail(false);
    fireEvent.click(screen.getByRole("button", { name: t.edit }));
    expect((screen.getByLabelText(`${t.fieldAddresses} 1`) as HTMLInputElement).disabled).toBe(true);
    expect(screen.getByText(t.addressesAdminOnly)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(t.fieldName), { target: { value: "journal2" } });
    fireEvent.click(screen.getByRole("button", { name: t.save }));
    await waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0]).toEqual({ method: "PATCH", url: "/api/accounts/journal", body: { name: "journal2" } });
  });

  it("lets admins replace the addresses", async () => {
    detail(true);
    fireEvent.click(screen.getByRole("button", { name: t.edit }));
    fireEvent.click(screen.getByRole("button", { name: t.addAddress }));
    fireEvent.change(screen.getByLabelText(`${t.fieldAddresses} 2`), { target: { value: "more@archive.local" } });
    fireEvent.click(screen.getByRole("button", { name: t.removeAddress("archive@archive.local") }));
    fireEvent.click(screen.getByRole("button", { name: t.save }));
    await waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0]!.body).toEqual({ addresses: ["more@archive.local"] });
  });

  it("shows the mail server setup in its own section", () => {
    detail(false, { addresses: ["journal@example.com"] });
    expect(screen.getByText(t.setupNotDedicated("archive@archive.local"))).toBeTruthy();
    // The section is the region; the panel inside it is none of its own.
    expect(screen.getAllByRole("region", { name: t.mailServerSetup })).toHaveLength(1);
  });
});

describe("addressList", () => {
  it("drops empty entries", () => {
    expect(addressList([" a@b.local ", "", "  "])).toEqual(["a@b.local"]);
  });
});
