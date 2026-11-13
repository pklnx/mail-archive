// The admin's Storage page (#397, part 4): the instance, the customer
// groups with Edit quota while more than one exists (#470 decision 10), the
// accounts, Count again, and the texts in German.
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ownAccountSize, type Usage } from "./api";
import { storageSize } from "./format";
import { applySettings } from "./i18n";
import { parseSize, StoragePage } from "./StoragePage";
import { storageTextFor } from "./storageText";
import { adminUsage, groupState, memberUsage } from "./test/usage";

const GiB = 1024 ** 3;
const st = storageTextFor("en");

let requests: { method: string; url: string; body: unknown }[];
/** The answer of PATCH /api/groups/{id}. */
let patchStatus: number;

beforeEach(() => {
  requests = [];
  patchStatus = 204;
  vi.spyOn(globalThis, "fetch").mockImplementation(async (url, init) => {
    const method = init?.method ?? "GET";
    requests.push({ method, url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined });
    if (method === "PATCH") {
      return patchStatus === 204
        ? new Response(null, { status: 204 })
        : new Response(JSON.stringify({ error: "the quota is above the instance quota" }), { status: patchStatus, headers: { "Content-Type": "application/json" } });
    }
    if (method === "POST" && url === "/api/usage/refresh") return new Response(null, { status: 202 });
    return new Response(null, { status: 404 });
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

/** Opens Edit quota from the group's row menu. */
function editQuota(group: string, text = st) {
  fireEvent.click(screen.getByRole("button", { name: text.groupActions(group) }));
  fireEvent.click(screen.getByRole("menuitem", { name: text.editQuota }));
}

function show(data: Usage | null, reload = vi.fn()) {
  render(<StoragePage usage={{ data, error: "", reload }} />);
  return reload;
}

describe("StoragePage", () => {
  it("shows the instance, the groups with their quotas and the accounts", () => {
    show(adminUsage({ ungrouped: { messages: 2, bytes: 1024 } }));
    expect(screen.getByRole("heading", { level: 1, name: "Storage" })).toBeTruthy();
    expect(screen.getByText(/Usage: 30 GiB of 50 GiB \(60 %\)/)).toBeTruthy();
    expect(screen.getByText(/counted .* the count runs every hour/)).toBeTruthy();
    expect(screen.getByText(st.upperBound, { exact: false })).toBeTruthy();
    expect(screen.getByText("200 GiB free of 500 GiB")).toBeTruthy();
    const groups = screen.getByRole("table", { name: st.groupsTitle });
    expect(within(groups).getByText("20 GiB of 40 GiB (50 %)")).toBeTruthy();
    expect(within(groups).getByText(st.withoutGroup)).toBeTruthy();
    expect(within(groups).getAllByRole("button", { name: /^Actions for group / })).toHaveLength(2);
    // A group bar per quota: Meier only, with its label for screen readers.
    expect(within(groups).getAllByRole("progressbar")).toHaveLength(1);
    const accounts = screen.getByRole("table", { name: st.accountsTitle });
    const rows = within(accounts).getAllByRole("row");
    // Largest first; the account not counted yet says so.
    expect(rows[1]!.textContent).toContain("buchhaltung");
    expect(within(accounts).getByText(st.notCounted)).toBeTruthy();
    expect(within(accounts).getByText("9.0 GiB")).toBeTruthy();
    expect(within(accounts).getAllByText("Meier")).toHaveLength(1);
  });

  it("says when group quotas add up to more than the instance quota", () => {
    const u = adminUsage();
    show({ ...u, groups: u.groups.map((g) => ({ ...g, quotaBytes: 40 * GiB })) });
    expect(screen.getByText(/The group quotas add up to 80 GiB, more than the instance quota of 50 GiB\./)).toBeTruthy();
  });

  it("shows no groups and no quota editing while one group exists, and its lower quota on the instance bar", () => {
    const one = adminUsage({ groups: [{ id: 1, name: "Default", messages: 12000, bytes: 30 * GiB, quotaBytes: 32 * GiB, state: "warn", stateSince: null }] });
    show(one);
    expect(screen.queryByRole("table", { name: st.groupsTitle })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Actions for group/ })).toBeNull();
    expect(screen.queryByText(st.upperBound, { exact: false })).toBeNull();
    // The group quota is lower than the instance's: the bar shows it.
    expect(screen.getByText(/Usage: 30 GiB of 32 GiB \(93 %\)/)).toBeTruthy();
    expect(screen.getByText(st.groupQuotaFirst("32 GiB"))).toBeTruthy();
    expect(screen.getByText(st.states.warn)).toBeTruthy();
    // No group column in the accounts table either.
    expect(within(screen.getByRole("table", { name: st.accountsTitle })).queryByText("Default")).toBeNull();
  });

  it("shows the instance figure without a bar when there is no quota", () => {
    show(adminUsage({ instance: { quotaBytes: null }, groups: [adminUsage().groups[0]!] }));
    expect(screen.getByText("30 GiB, no quota")).toBeTruthy();
    expect(screen.queryByRole("progressbar")).toBeNull();
  });

  it("marks a full group and the instance state", () => {
    show(groupState(adminUsage({ instance: { state: "disk_low" } }), "Meier", "overflow"));
    expect(screen.getByText(st.states.overflow)).toBeTruthy();
    expect(screen.getByText(st.states.disk_low)).toBeTruthy();
  });

  it("counts again on request and waits for the new count", async () => {
    const reload = show(adminUsage());
    fireEvent.click(screen.getByRole("button", { name: st.refresh }));
    await screen.findByText(st.refreshed);
    expect(requests).toEqual([{ method: "POST", url: "/api/usage/refresh", body: undefined }]);
    expect(reload).toHaveBeenCalled();
    expect((screen.getByRole("button", { name: st.refreshing }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("shows nothing of a member's view", () => {
    show(memberUsage());
    expect(screen.queryByRole("table")).toBeNull();
  });
});

describe("Edit quota", () => {
  const open = (group = "Meier") => {
    show(adminUsage());
    editQuota(group);
    return screen.getByRole("dialog");
  };

  it("refuses a quota above the instance quota or not above 0 before sending", async () => {
    const dialog = open();
    const size = within(dialog).getByLabelText(st.quotaSize);
    expect((size as HTMLInputElement).value).toBe("40");
    fireEvent.change(size, { target: { value: "60" } });
    expect(within(dialog).getByText(st.quotaAboveInstance("50 GiB"))).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: st.quotaSave }));
    fireEvent.change(size, { target: { value: "0" } });
    expect(within(dialog).getByText(st.quotaInvalid)).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: st.quotaSave }));
    expect(requests).toEqual([]);
  });

  it("refuses a quota above 1 EiB before sending when there is no instance quota", async () => {
    show(adminUsage({ instance: { quotaBytes: null } }));
    editQuota("Meier");
    const dialog = screen.getByRole("dialog");
    const size = within(dialog).getByLabelText(st.quotaSize);
    fireEvent.change(within(dialog).getByLabelText(st.quotaUnit), { target: { value: "TiB" } });
    // 8 000 000 TiB is about 8.8e18 bytes, whose thresholds would overflow on the server.
    fireEvent.change(size, { target: { value: "8000000" } });
    expect(within(dialog).getByText(st.quotaAboveMax("1024 PiB"))).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: st.quotaSave }));
    expect(requests).toEqual([]);
    // 1 EiB itself is taken.
    fireEvent.change(size, { target: { value: String(1024 * 1024) } });
    expect(within(dialog).queryByText(st.quotaAboveMax("1024 PiB"))).toBeNull();
    fireEvent.click(within(dialog).getByRole("button", { name: st.quotaSave }));
    await screen.findByText(st.quotaSet("Meier", "1024 PiB"));
    expect(requests).toEqual([{ method: "PATCH", url: "/api/groups/2", body: { quotaBytes: 2 ** 60 } }]);
  });

  it("says what a quota means for the group, and that it pauses at once", () => {
    const dialog = open();
    const size = within(dialog).getByLabelText(st.quotaSize);
    fireEvent.change(size, { target: { value: "24" } });
    expect(within(dialog).getByText(/With this quota it is at 83 %\. It is nearly full with this quota\./)).toBeTruthy();
    fireEvent.change(size, { target: { value: "16" } });
    expect(within(dialog).getByText(new RegExp(`With this quota it is at 125 %\\. ${st.quotaStops.slice(0, 20)}`))).toBeTruthy();
  });

  it("sets the quota in bytes, and reloads", async () => {
    const reload = vi.fn();
    render(<StoragePage usage={{ data: adminUsage(), error: "", reload }} />);
    editQuota("Default");
    const dialog = screen.getByRole("dialog");
    expect((within(dialog).getByRole("checkbox", { name: st.quotaNone }) as HTMLInputElement).checked).toBe(true);
    fireEvent.click(within(dialog).getByRole("checkbox", { name: st.quotaNone }));
    fireEvent.change(within(dialog).getByLabelText(st.quotaSize), { target: { value: "1,5" } });
    fireEvent.change(within(dialog).getByLabelText(st.quotaUnit), { target: { value: "TiB" } });
    fireEvent.click(within(dialog).getByRole("button", { name: st.quotaSave }));
    // 1.5 TiB is above the instance quota of 50 GiB.
    expect(requests).toEqual([]);
    fireEvent.change(within(dialog).getByLabelText(st.quotaUnit), { target: { value: "GiB" } });
    fireEvent.click(within(dialog).getByRole("button", { name: st.quotaSave }));
    await screen.findByText(st.quotaSet("Default", "1.5 GiB"));
    expect(requests).toEqual([{ method: "PATCH", url: "/api/groups/1", body: { quotaBytes: 1.5 * GiB } }]);
    expect(reload).toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("removes the quota with null", async () => {
    const dialog = open();
    fireEvent.click(within(dialog).getByRole("checkbox", { name: st.quotaNone }));
    fireEvent.click(within(dialog).getByRole("button", { name: st.quotaSave }));
    await screen.findByText(st.quotaRemoved("Meier"));
    expect(requests).toEqual([{ method: "PATCH", url: "/api/groups/2", body: { quotaBytes: null } }]);
  });

  it("shows the server's refusal in the dialog", async () => {
    patchStatus = 400;
    const dialog = open();
    fireEvent.change(within(dialog).getByLabelText(st.quotaSize), { target: { value: "30" } });
    fireEvent.click(within(dialog).getByRole("button", { name: st.quotaSave }));
    await waitFor(() => expect(within(dialog).getAllByRole("alert").some((a) => a.textContent?.includes("above the instance quota"))).toBe(true));
  });
});

describe("sizes", () => {
  it("parses a typed size with a point or a comma", () => {
    expect(parseSize("40", "GiB")).toBe(40 * GiB);
    expect(parseSize("1,5", "TiB")).toBe(1.5 * 1024 * GiB);
    expect(parseSize(" 512 ", "MiB")).toBe(512 * 1024 ** 2);
    for (const bad of ["", "-1", "1e3", "abc", "1.2.3"]) expect(parseSize(bad, "GiB")).toBeNaN();
  });

  it("writes sizes in binary units", () => {
    expect(storageSize(0)).toBe("0 B");
    expect(storageSize(1536)).toBe("1.5 KiB");
    expect(storageSize(50 * GiB)).toBe("50 GiB");
    expect(storageSize(2.25 * 1024 * GiB)).toBe("2.3 TiB");
  });

  it("finds an own account's size in either view", () => {
    expect(ownAccountSize(adminUsage(), "buchhaltung")).toEqual({ messages: 8000, bytes: 20 * GiB });
    // Another user's account of the same name is not the admin's.
    expect(ownAccountSize(adminUsage(), "info")).toBeNull();
    expect(ownAccountSize(memberUsage(), "info")).toEqual({ messages: 4000, bytes: 10 * GiB });
    expect(ownAccountSize({ state: "ok", accounts: [{ name: "x", messages: null, bytes: null }] }, "x")).toBeNull();
    expect(ownAccountSize(null, "x")).toBeNull();
  });
});

describe("German", () => {
  it("shows the page and the dialog in German", () => {
    applySettings({ language: "de", locale: "de" });
    try {
      const de = storageTextFor("de");
      show(adminUsage());
      expect(screen.getByRole("heading", { level: 1, name: "Speicher" })).toBeTruthy();
      expect(screen.getByText(/Belegt: 30 GiB von 50 GiB \(60 %\)/)).toBeTruthy();
      expect(screen.getByRole("heading", { name: de.groupsTitle })).toBeTruthy();
      editQuota("Meier", de);
      const dialog = screen.getByRole("dialog");
      expect(within(dialog).getByText(de.quotaTitle("Meier"))).toBeTruthy();
      fireEvent.change(within(dialog).getByLabelText(de.quotaSize), { target: { value: "24" } });
      expect(within(dialog).getByText(/Mit diesem Kontingent sind das 83 %\./)).toBeTruthy();
      expect(storageSize(1.5 * GiB)).toBe("1,5 GiB");
    } finally {
      applySettings({ language: "en", locale: "en" });
    }
  });
});
