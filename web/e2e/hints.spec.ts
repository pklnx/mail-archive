import { readFile } from "node:fs/promises";
import { createServer as createHTTPServer } from "node:http";
import { createServer as createTCPServer, type Server } from "node:net";
import { chromium, type BrowserContext, type Cookie } from "@playwright/test";
import { controlConnectPort, controlHTTPPort, hintConnectPort, hintHTTPPort, origin } from "./demo";
import { expect, test } from "./fixtures";
import { adminState } from "./global-setup";
import { frameOf, mailReady } from "./mailFrame";

// Resource hints in an HTML mail (GHSA-w6jg-ffvw-q4f5): <link rel=prerender>,
// preconnect and dns-prefetch make the browser send a request, open a
// connection or look up a name as soon as the mail is shown, which tells the
// sender that it was opened. So does a frame's src: the CSP blocks the
// navigation, but the browser has connected to the host before. The CSP does
// not govern the connection; the server renames rel, and src on <iframe> and
// <frame> (neutralizeHTML in internal/web), so neither does anything. The
// demo's hint check (tools/demo, hintCheck) holds the hints and frames,
// pointing at *.localhost names on this spec's listeners, where every
// connection, with or without a request, counts.
//
// Playwright's default launch never shows this: its headless shell sends no
// hint at all, and the full Chromium sends preconnect and DNS prefetch only
// in a persistent profile. So this test launches its own full Chromium with
// a persistent profile and background networking on, and the rest of the
// suite keeps Playwright's defaults. A positive control proves that hints
// fire in this browser, profile and sandboxed, CSP'd frame: once the mail is
// shown, the test adds three <link> elements to the mail's own document (from
// the parent page, through contentDocument), and waits for them. The mail's
// hints were parsed before, so they would have been issued first; any of
// them arriving fails the test at once.
//
// No network: every name outside localhost and *.localhost, IP literals
// included, resolves to ~NOTFOUND (--host-resolver-rules), so Chromium's own
// background traffic never leaves the machine. Chromium resolves *.localhost
// itself, without DNS, but logs the request in its net log, which shows the
// DNS prefetch. The test reads the net log at the end and checks that every
// connection went to loopback, every name was a loopback one or mapped, and
// no name of the mail's hosts shows anywhere (a preconnect to a *.localhost
// name logs no resolver request, only its socket's group and URL).
//
// One UDP "connect" is not to loopback: the resolver's IPv6 reachability
// check calls connect() on a UDP socket to 2001:4860:4860::8888, port 443,
// only to learn whether the system has a route there. A UDP connect()
// sends no packet. Chromium makes it whenever it resolves a name, *.localhost
// included, and no flag turns it off; on a host without IPv6 it fails before
// the net log records it. The check below lets exactly that through: that
// address, on a socket the reachability check opened, which sent no byte.

/** Each address of 127.0.0.0/8 or ::1, with its port. */
const loopbackAddress = /^(127\.\d+\.\d+\.\d+|\[::1\]):\d+$/;
/** A name the resolver rule lets through, or the name it maps all others to. */
const loopbackName = /^(localhost|[a-z0-9.-]+\.localhost|~notfound)$/;
/** The mail's host names (tools/demo, hintCheck). */
const mailHost = /hint-[a-z0-9-]*\.localhost/g;
/** The address of the resolver's IPv6 reachability check (net/dns/host_resolver_manager.cc). */
const ipv6ProbeAddress = "[2001:4860:4860::8888]:443";

/**
 * Listens on port on both loopback addresses: Chromium tries [::1] for
 * *.localhost too. A host without IPv6 has no [::1] to listen on, and then
 * Chromium has none to connect to either.
 */
async function listenBoth(make: () => Server, port: number): Promise<Server[]> {
  const listen = (host: string) =>
    new Promise<Server | null>((resolve, reject) => {
      const s = make();
      s.once("error", (err: NodeJS.ErrnoException) =>
        host === "::1" && (err.code === "EAFNOSUPPORT" || err.code === "EADDRNOTAVAIL") ? resolve(null) : reject(err),
      ).listen(port, host, () => resolve(s));
    });
  const servers = [await listen("127.0.0.1"), await listen("::1")];
  return servers.filter((s): s is Server => s !== null);
}

/** The host name (no scheme, no port) of a resolver request's host. */
function hostName(host: string): string {
  return host.replace(/^[a-z][a-z0-9+.-]*:\/\//, "").replace(/:\d+$/, "").replace(/^\[(.*)\]$/, "$1");
}

interface NetLogEvent {
  type: number;
  source: { id: number };
  params?: {
    host?: string;
    address?: string;
    address_list?: string[];
    remote_address?: string;
    source_dependency?: { id: number };
  };
}

/**
 * The net log's resolver requests (host names), connect addresses, the mail's
 * host names wherever they appear, and the IPv6 reachability checks' UDP
 * connects that sent nothing. A log cut off by the end of the process is
 * closed first.
 */
async function readNetLog(path: string): Promise<{ names: string[]; addresses: string[]; mailNames: string[]; probes: number }> {
  const text = await readFile(path, "utf8");
  let log: { constants: { logEventTypes: Record<string, number> }; events: NetLogEvent[] };
  try {
    log = JSON.parse(text);
  } catch {
    log = JSON.parse(text.replace(/,?\s*$/, "") + "]}");
  }
  const type = log.constants.logEventTypes;
  // Each source's parent (a socket's owner), the sources of reachability
  // checks, and the sources that sent bytes.
  const parent = new Map<number, number>();
  const checks = new Set<number>();
  const sent = new Set<number>();
  for (const e of log.events) {
    const dep = e.params?.source_dependency?.id;
    if (dep !== undefined && !parent.has(e.source.id)) parent.set(e.source.id, dep);
    if (e.type === type.HOST_RESOLVER_MANAGER_IPV6_REACHABILITY_CHECK) checks.add(e.source.id);
    if (e.type === type.UDP_BYTES_SENT || e.type === type.SOCKET_BYTES_SENT) sent.add(e.source.id);
  }
  /** The source and its owners, up to the request that made it. */
  const chain = (id: number) => {
    const ids = [id];
    for (let p = parent.get(id); p !== undefined && ids.length < 8 && !ids.includes(p); p = parent.get(p)) ids.push(p);
    return ids;
  };
  const isProbe = (e: NetLogEvent, a: string) => {
    if (e.type !== type.UDP_CONNECT || a !== ipv6ProbeAddress) return false;
    const ids = chain(e.source.id);
    return ids.some((id) => checks.has(id)) && !ids.some((id) => sent.has(id));
  };
  const names: string[] = [];
  const addresses: string[] = [];
  const mailNames = new Set<string>();
  let probes = 0;
  for (const e of log.events) {
    const p = e.params ?? {};
    for (const m of JSON.stringify(p).match(mailHost) ?? []) mailNames.add(m);
    if (e.type === type.HOST_RESOLVER_MANAGER_REQUEST && p.host) names.push(hostName(p.host));
    if (e.type === type.TCP_CONNECT || e.type === type.TCP_CONNECT_ATTEMPT || e.type === type.UDP_CONNECT) {
      for (const a of [p.address, p.remote_address, ...(p.address_list ?? [])]) {
        if (!a) continue;
        if (isProbe(e, a)) probes++;
        else addresses.push(a);
      }
    }
  }
  return { names, addresses, mailNames: [...mailNames], probes };
}

test("a mail's resource hints send nothing, with background networking on", async ({ browserName }, testInfo) => {
  // Chromium's net log and launch flags; the webkit project does not run this spec.
  test.skip(browserName !== "chromium", "Chromium only");
  // Launching a second browser with a fresh profile takes a few seconds.
  test.setTimeout(90_000);
  // Each listener reports what reaches it. Any connection to one of the
  // mail's ports, with or without a request, settles mailHint at once.
  const mailHits: string[] = [];
  let reportMail!: (hit: string) => void;
  const mailHint = new Promise<string>((resolve) => (reportMail = resolve));
  const mail = (hit: string) => {
    mailHits.push(hit);
    reportMail(hit);
  };
  let controlPrerender!: () => void;
  const prerendered = new Promise<void>((resolve) => (controlPrerender = resolve));
  let controlPreconnect!: () => void;
  const preconnected = new Promise<void>((resolve) => (controlPreconnect = resolve));

  const mailListener = (port: number) =>
    listenBoth(
      () =>
        createTCPServer((socket) => {
          mail(`connect to ${port}`);
          // The request line, if one comes, names the hint.
          socket.once("data", (d) => mailHits.push(`  ${d.toString("latin1").split("\r\n")[0]}`));
          socket.on("error", () => {});
          socket.end();
        }),
      port,
    );
  const servers = [
    ...(await mailListener(hintHTTPPort)),
    ...(await mailListener(hintConnectPort)),
    ...(await listenBoth(
      () =>
        createHTTPServer((req, res) => {
          if ((req.headers.host ?? "").startsWith("ctl-prerender.localhost")) controlPrerender();
          res.writeHead(200, { "content-type": "text/html" }).end("<p>control</p>");
        }),
      controlHTTPPort,
    )),
    ...(await listenBoth(
      () =>
        createTCPServer((socket) => {
          controlPreconnect();
          socket.on("error", () => {}).end();
        }),
      controlConnectPort,
    )),
  ];

  const netLog = testInfo.outputPath("netlog.json");
  let context: BrowserContext | undefined;
  try {
    context = await chromium.launchPersistentContext(testInfo.outputPath("profile"), {
      // The full Chromium: the headless shell sends no hint.
      channel: "chromium",
      executablePath: process.env.CHROMIUM_PATH || undefined,
      ignoreDefaultArgs: ["--disable-background-networking"],
      args: ["--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE *.localhost", "--no-proxy-server", `--log-net-log=${netLog}`],
      baseURL: origin,
      locale: "en-US",
      timezoneId: "Europe/Berlin",
    });
    // A persistent context has no storageState: the admin session's cookies.
    const state = JSON.parse(await readFile(adminState, "utf8")) as { cookies: Cookie[] };
    await context.addCookies(state.cookies);
    const page = context.pages()[0] ?? (await context.newPage());

    await page.goto("/?q=periscope");
    const list = page.getByRole("list", { name: "Messages" });
    await expect(list.getByRole("button")).toHaveCount(1);
    await list.getByRole("button").click();
    await expect(page.getByRole("article").getByRole("heading", { level: 1 })).toHaveText("Request check: hints that stay silent");
    await mailReady(page, /periscope check/);

    // The controls, in the order dns-prefetch, preconnect, prerender: hints
    // of one document are issued in order, so a later one arriving implies
    // the earlier ones were issued.
    await frameOf(page).evaluate(
      (f: HTMLIFrameElement, { connect, http }) => {
        const doc = f.contentDocument!;
        for (const [rel, href] of [
          ["dns-prefetch", "//ctl-dns.localhost/"],
          ["preconnect", `http://ctl-preconnect.localhost:${connect}/`],
          ["prerender", `http://ctl-prerender.localhost:${http}/control`],
        ]) {
          const link = doc.createElement("link");
          link.rel = rel!;
          link.href = href!;
          doc.head.appendChild(link);
        }
      },
      { connect: controlConnectPort, http: controlHTTPPort },
    );
    const first = await Promise.race([Promise.all([preconnected, prerendered]).then(() => null), mailHint]);
    expect(first, "a resource hint of the mail reached its listener").toBeNull();
  } finally {
    // Closing flushes the net log.
    await context?.close();
    await Promise.all(servers.map((s) => new Promise((resolve) => s.close(resolve))));
  }
  // A hint that arrived late fails the test as well.
  expect(mailHits).toEqual([]);

  const { names, addresses, mailNames, probes } = await readNetLog(netLog);
  // The control's DNS prefetch shows: the check below is not vacuous.
  expect(names).toContain("ctl-dns.localhost");
  expect(mailNames, "the mail's hosts in the net log").toEqual([]);
  testInfo.annotations.push({ type: "IPv6 reachability checks", description: String(probes) });
  // No network: every name was a loopback one or mapped to ~NOTFOUND, and
  // every connection went to loopback.
  expect(names.filter((n) => !loopbackName.test(n))).toEqual([]);
  expect(addresses.length).toBeGreaterThan(0);
  expect(addresses.filter((a) => !loopbackAddress.test(a))).toEqual([]);
});
